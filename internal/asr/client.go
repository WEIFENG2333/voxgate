package asr

import (
	"context"
	"encoding/json"
	"fmt"
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
}

type Client struct {
	Config ClientConfig
}

type PCMFrameEncoder interface {
	EncodePCMFrame([]byte) ([]byte, error)
	Close() error
}

type PCMFrameSource interface {
	NextFrame() ([]byte, bool, error)
	Duration() time.Duration
	Close() error
}

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
		}
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
	conn, _, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return true, err
	}
	defer conn.Close()
	if err := sendPB(conn, asrproto.Request{Token: creds.Token, ServiceName: "ASR", MethodName: "StartTask", RequestID: requestID}); err != nil {
		return true, err
	}
	resp, err := readPB(conn)
	if err != nil {
		return true, err
	}
	if resp.MessageType == "TaskFailed" {
		return true, fmt.Errorf("StartTask failed (code=%d): %s", resp.StatusCode, resp.StatusMessage)
	}
	sessionPayload, _ := json.Marshal(map[string]any{
		"audio_info":              map[string]any{"channel": 1, "format": "speech_opus", "sample_rate": 16000},
		"enable_punctuation":      opts.EnablePunctuation,
		"enable_speech_rejection": false,
		"extra": map[string]any{
			"app_name": "com.android.chrome", "cell_compress_rate": 8, "did": creds.DeviceID,
			"enable_asr_threepass": opts.EnableThreePass, "enable_asr_twopass": opts.EnableTwoPass, "input_mode": "tool",
		},
	})
	if err := sendPB(conn, asrproto.Request{Token: creds.Token, ServiceName: "ASR", MethodName: "StartSession", Payload: string(sessionPayload), RequestID: requestID}); err != nil {
		return true, err
	}
	resp, err = readPB(conn)
	if err != nil {
		return true, err
	}
	if resp.MessageType == "SessionFailed" {
		return true, fmt.Errorf("StartSession failed (code=%d): %s", resp.StatusCode, resp.StatusMessage)
	}
	events <- Event{Type: EventSessionStarted, RequestID: requestID}

	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		sendErr <- c.sendAudio(ctx, conn, requestID, creds.Token, source, encoder, opts.Realtime)
	}()
	go func() {
		recvErr <- c.recv(ctx, conn, requestID, source, events, done)
	}()
	select {
	case err := <-sendErr:
		if err != nil {
			return false, err
		}
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

func (c Client) sendAudio(ctx context.Context, conn *websocket.Conn, requestID, token string, source PCMFrameSource, encoder PCMFrameEncoder, realtime bool) error {
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
		payload, _ := json.Marshal(map[string]any{"extra": map[string]any{}, "timestamp_ms": timestamp + int64(frameIndex*20)})
		if err := sendPB(conn, asrproto.Request{ServiceName: "ASR", MethodName: "TaskRequest", Payload: string(payload), AudioData: opusFrame, RequestID: requestID, FrameState: state}); err != nil {
			return err
		}
		frameIndex++
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if realtime {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if frameIndex > 0 {
		silence := make([]byte, 640)
		opusFrame, err := encoder.EncodePCMFrame(silence)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"extra": map[string]any{}, "timestamp_ms": timestamp + int64(frameIndex*20)})
		if err := sendPB(conn, asrproto.Request{ServiceName: "ASR", MethodName: "TaskRequest", Payload: string(payload), AudioData: opusFrame, RequestID: requestID, FrameState: asrproto.FrameStateLast}); err != nil {
			return err
		}
	}
	return sendPB(conn, asrproto.Request{Token: token, ServiceName: "ASR", MethodName: "FinishSession", RequestID: requestID})
}

func (c Client) recv(ctx context.Context, conn *websocket.Conn, requestID string, source PCMFrameSource, events chan<- Event, done <-chan struct{}) error {
	var agg SegmentResetAggregator
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return nil
		default:
		}
		resp, err := readPB(conn)
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				if agg.Text() == "" {
					return fmt.Errorf("websocket closed normally before any transcript result")
				}
				events <- Event{Type: EventTranscriptDone, RequestID: requestID, Text: agg.Text(), Duration: source.Duration().Seconds()}
				return nil
			}
			return err
		}
		switch resp.MessageType {
		case "TaskFailed", "SessionFailed":
			return fmt.Errorf("%s (code=%d): %s", resp.MessageType, resp.StatusCode, resp.StatusMessage)
		case "SessionFinished":
			text := agg.Text()
			events <- Event{Type: EventTranscriptDone, RequestID: requestID, Text: text, Duration: source.Duration().Seconds()}
			return nil
		}
		parsed, err := ParseResultJSON(resp.ResultJSON)
		if err != nil || parsed.Kind == ParsedNoop {
			continue
		}
		if parsed.Kind == ParsedVADStart {
			events <- Event{Type: EventVADStart, RequestID: requestID, TimestampMS: int64(time.Since(start) / time.Millisecond), Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
			continue
		}
		full, reset, seg := agg.Update(parsed.Text)
		if reset {
			events <- Event{Type: EventTranscriptSegment, RequestID: requestID, Text: seg.Text, SegmentIndex: seg.Index, Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
		}
		switch parsed.Kind {
		case ParsedInterim:
			events <- Event{Type: EventTranscriptDelta, RequestID: requestID, Text: full, IsInterim: true, Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
		case ParsedDefinite:
			events <- Event{Type: EventTranscriptDelta, RequestID: requestID, Text: full, Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
		case ParsedFinal:
			text := agg.Final(parsed.Text)
			events <- Event{Type: EventTranscriptDone, RequestID: requestID, Text: text, Duration: source.Duration().Seconds(), Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
			return nil
		}
	}
}

func sendPB(conn *websocket.Conn, req asrproto.Request) error {
	return conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalRequest(req))
}

func readPB(conn *websocket.Conn) (asrproto.Response, error) {
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return asrproto.Response{}, err
		}
		if mt == websocket.BinaryMessage {
			return asrproto.UnmarshalResponse(data)
		}
	}
}
