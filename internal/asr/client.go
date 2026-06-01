package asr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	asrproto "github.com/WEIFENG2333/voxgate/internal/proto"
)

type ClientConfig struct {
	CredentialPath string
	UserAgent      string
	WebSocketURL   string
	HTTP           *http.Client
	Dialer         *websocket.Dialer
	TraceWriter    io.Writer
}

type Client struct {
	Config ClientConfig
}

var finishSessionWaitTimeout = 15 * time.Second

type PCMFrameEncoder interface {
	EncodePCMFrame([]byte) ([]byte, error)
	Close() error
}

type PCMFrameSource interface {
	NextFrame() ([]byte, bool, error)
	Duration() time.Duration
	Close() error
}

// Transcribe starts a transcription session and returns events as soon as the
// upstream sends them. Audio sending and response reading run concurrently.
func (c Client) Transcribe(ctx context.Context, source PCMFrameSource, encoder PCMFrameEncoder, opts Options) (<-chan Event, error) {
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		defer source.Close()
		defer encoder.Close()
		if opts.RequestTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, opts.RequestTimeout)
			defer cancel()
		}
		requestID := uuid.NewString()
		events <- Event{Type: EventTaskStarted, RequestID: requestID}
		if err := c.run(ctx, requestID, source, encoder, opts, events); err != nil {
			events <- Event{Type: EventError, RequestID: requestID, Error: &ErrorPayload{Code: "asr_error", Message: err.Error()}}
			return
		}
		events <- Event{Type: EventStreamDone, RequestID: requestID, Duration: source.Duration().Seconds()}
	}()
	return events, nil
}

func (c Client) run(ctx context.Context, requestID string, source PCMFrameSource, encoder PCMFrameEncoder, opts Options, events chan<- Event) error {
	userAgent := c.Config.UserAgent
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	manager := CredentialManager{Path: c.Config.CredentialPath, UserAgent: userAgent, HTTP: c.Config.HTTP}
	creds, err := manager.Ensure(ctx, false)
	if err != nil {
		return err
	}
	retryable, err := c.runWithCreds(ctx, creds, requestID, source, encoder, opts, events)
	if err == nil {
		return nil
	}
	if !retryable {
		return err
	}
	// Handshake failures are usually credential-related: try a token refresh
	// first, then fall back to a fresh device identity if the service still
	// rejects the session.
	creds, refreshErr := manager.Ensure(ctx, true)
	if refreshErr == nil {
		retryable, err = c.runWithCreds(ctx, creds, requestID, source, encoder, opts, events)
		if err == nil {
			return nil
		}
		if !retryable {
			return err
		}
	}
	creds, reissueErr := manager.Reissue(ctx)
	if reissueErr != nil {
		return err
	}
	_, err = c.runWithCreds(ctx, creds, requestID, source, encoder, opts, events)
	return err
}

func (c Client) runWithCreds(ctx context.Context, creds Credentials, requestID string, source PCMFrameSource, encoder PCMFrameEncoder, opts Options, events chan<- Event) (bool, error) {
	wsURL := c.Config.WebSocketURL
	if wsURL == "" {
		wsURL = WebSocketURL
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		return false, err
	}
	q := u.Query()
	q.Set("aid", strconv.Itoa(AID))
	q.Set("device_id", creds.DeviceID)
	u.RawQuery = q.Encode()
	header := http.Header{}
	userAgent := c.Config.UserAgent
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	header.Set("User-Agent", userAgent)
	header.Set("proto-version", "v2")
	header.Set("x-custom-keepalive", "true")
	dialer := c.Config.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	// The upstream protocol is a two-step handshake: StartTask authorizes the
	// task, then StartSession declares audio format and recognition options.
	conn, _, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return true, err
	}
	defer conn.Close()
	trace := newFrameTrace(c.Config.TraceWriter)
	send := func(req asrproto.Request) error { return sendPBTrace(conn, trace, req) }
	read := func() (asrproto.Response, error) { return readPBTrace(conn, trace) }
	if err := send(asrproto.Request{Token: creds.Token, ServiceName: ServiceNameASR, MethodName: MethodStartTask, RequestID: requestID}); err != nil {
		return true, err
	}
	resp, err := read()
	if err != nil {
		return true, err
	}
	if resp.MessageType == MessageTaskFailed {
		return true, fmt.Errorf("StartTask failed (code=%d): %s", resp.StatusCode, resp.StatusMessage)
	}
	sessionPayload, _ := json.Marshal(map[string]any{
		"audio_info":              map[string]any{"channel": UpstreamChannels, "format": AudioFormatSpeechOpus, "sample_rate": UpstreamSampleRate},
		"enable_punctuation":      opts.EnablePunctuation,
		"enable_speech_rejection": false,
		"extra": map[string]any{
			"app_name": "com.android.chrome", "cell_compress_rate": 8, "did": creds.DeviceID,
			"enable_asr_threepass": opts.EnableThreePass, "enable_asr_twopass": opts.EnableTwoPass, "input_mode": "tool",
		},
	})
	if err := send(asrproto.Request{Token: creds.Token, ServiceName: ServiceNameASR, MethodName: MethodStartSession, Payload: string(sessionPayload), RequestID: requestID}); err != nil {
		return true, err
	}
	resp, err = read()
	if err != nil {
		return true, err
	}
	if resp.MessageType == MessageSessionFailed {
		return true, fmt.Errorf("StartSession failed (code=%d): %s", resp.StatusCode, resp.StatusMessage)
	}
	events <- Event{Type: EventSessionStarted, RequestID: requestID}

	// Send and receive concurrently so partial transcripts are forwarded while
	// audio is still being uploaded. This is the core streaming path.
	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)
	done := make(chan struct{})
	finishedSending := make(chan struct{})
	go func() {
		sendErr <- c.sendAudio(ctx, conn, trace, requestID, creds.Token, source, encoder, opts.Realtime)
	}()
	go func() {
		recvErr <- c.recv(ctx, conn, trace, requestID, source, events, done, finishedSending)
	}()
	select {
	case err := <-sendErr:
		if err != nil {
			return false, err
		}
		close(finishedSending)
		// After FinishSession the backend may close without an explicit final
		// message. Bound the wait and let recv synthesize transcript.final.
		_ = conn.SetReadDeadline(time.Now().Add(finishSessionWaitTimeout))
		select {
		case err := <-recvErr:
			return false, err
		case <-ctx.Done():
			return false, ctx.Err()
		}
	case err := <-recvErr:
		if err != nil {
			return false, err
		}
		close(done)
		return false, nil
	case <-ctx.Done():
		close(done)
		return false, ctx.Err()
	}
}

func (c Client) sendAudio(ctx context.Context, conn *websocket.Conn, trace *frameTrace, requestID, token string, source PCMFrameSource, encoder PCMFrameEncoder, realtime bool) error {
	timestamp := time.Now().UnixMilli()
	frameIndex := 0
	for {
		pcm, ok, err := source.NextFrame()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		opusFrame, err := encoder.EncodePCMFrame(pcm)
		if err != nil {
			return err
		}
		state := asrproto.FrameStateMiddle
		if frameIndex == 0 {
			state = asrproto.FrameStateFirst
		}
		// Upstream timestamps are logical audio time, not wall-clock send time.
		payload, _ := json.Marshal(map[string]any{"extra": map[string]any{}, "timestamp_ms": timestamp + int64(frameIndex*20)})
		if err := sendPBTrace(conn, trace, asrproto.Request{ServiceName: ServiceNameASR, MethodName: MethodTaskRequest, Payload: string(payload), AudioData: opusFrame, RequestID: requestID, FrameState: state}); err != nil {
			return err
		}
		frameIndex++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// File inputs can be sent as fast as possible; live/realtime inputs must
		// preserve capture pace so the upstream VAD sees natural timing.
		if realtime {
			time.Sleep(time.Duration(UpstreamFrameDurationMS) * time.Millisecond)
		}
	}
	if frameIndex > 0 {
		silence := make([]byte, UpstreamBytesPerFrame)
		opusFrame, err := encoder.EncodePCMFrame(silence)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"extra": map[string]any{}, "timestamp_ms": timestamp + int64(frameIndex*20)})
		if err := sendPBTrace(conn, trace, asrproto.Request{ServiceName: ServiceNameASR, MethodName: MethodTaskRequest, Payload: string(payload), AudioData: opusFrame, RequestID: requestID, FrameState: asrproto.FrameStateLast}); err != nil {
			return err
		}
	}
	return sendPBTrace(conn, trace, asrproto.Request{Token: token, ServiceName: ServiceNameASR, MethodName: MethodFinishSession, RequestID: requestID})
}

func (c Client) recv(ctx context.Context, conn *websocket.Conn, trace *frameTrace, requestID string, source PCMFrameSource, events chan<- Event, done <-chan struct{}, finishedSending <-chan struct{}) error {
	lastText := ""
	finalEmitted := false
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return nil
		default:
		}
		resp, err := readPBTrace(conn, trace)
		if err != nil {
			select {
			case <-finishedSending:
				if finalEmitted {
					return nil
				}
				if isTimeout(err) {
					events <- Event{Type: EventTranscriptFinal, RequestID: requestID, Text: lastText, Duration: source.Duration().Seconds()}
					return nil
				}
			default:
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				if finalEmitted {
					return nil
				}
				if lastText == "" {
					return fmt.Errorf("websocket closed normally before any transcript result")
				}
				events <- Event{Type: EventTranscriptFinal, RequestID: requestID, Text: lastText, Duration: source.Duration().Seconds()}
				return nil
			}
			return err
		}
		switch resp.MessageType {
		case MessageTaskFailed, MessageSessionFailed:
			return fmt.Errorf("%s (code=%d): %s", resp.MessageType, resp.StatusCode, resp.StatusMessage)
		case MessageSessionFinished:
			if finalEmitted {
				return nil
			}
			events <- Event{Type: EventTranscriptFinal, RequestID: requestID, Text: lastText, Duration: source.Duration().Seconds()}
			return nil
		}
		// Recognition payloads arrive as JSON nested inside the protobuf frame.
		// Parser classification keeps the transport code independent from the
		// upstream's three-pass result details.
		parsed, err := ParseResultJSON(resp.ResultJSON)
		if err != nil || parsed.Kind == ParsedNoop {
			continue
		}
		if parsed.Kind == ParsedVADStart {
			events <- Event{Type: EventVADStart, RequestID: requestID, TimestampMS: int64(time.Since(start) / time.Millisecond), Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
			continue
		}
		lastText = parsed.Text
		switch parsed.Kind {
		case ParsedInterim:
			events <- Event{Type: EventTranscriptDelta, RequestID: requestID, Text: parsed.Text, IsInterim: true, Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
		case ParsedDefinite:
			events <- Event{Type: EventTranscriptDelta, RequestID: requestID, Text: parsed.Text, Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
		case ParsedFinal:
			events <- Event{Type: EventTranscriptFinal, RequestID: requestID, Text: parsed.Text, Duration: source.Duration().Seconds(), Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
			finalEmitted = true
			lastText = ""
		}
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return err != nil && (errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()))
}

func sendPB(conn *websocket.Conn, req asrproto.Request) error {
	return sendPBTrace(conn, nil, req)
}

func readPB(conn *websocket.Conn) (asrproto.Response, error) {
	return readPBTrace(conn, nil)
}

func sendPBTrace(conn *websocket.Conn, trace *frameTrace, req asrproto.Request) error {
	raw := asrproto.MarshalRequest(req)
	trace.sent(req, raw)
	return conn.WriteMessage(websocket.BinaryMessage, raw)
}

func readPBTrace(conn *websocket.Conn, trace *frameTrace) (asrproto.Response, error) {
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return asrproto.Response{}, err
		}
		if mt == websocket.BinaryMessage {
			resp, err := asrproto.UnmarshalResponse(data)
			trace.received(mt, data, resp, err)
			return resp, err
		}
		trace.received(mt, data, asrproto.Response{}, nil)
	}
}
