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
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	asrproto "github.com/WEIFENG2333/voxgate/internal/proto"
)

type ClientConfig struct {
	CredentialPath string
	UserAgent      string
	WebSocketURL   string        // 留空用默认端点 WebSocketURL；可设 EndpointWS / EndpointQUIC
	AudioFormat    string        // 留空=speech_opus(默认)；可设 AudioFormatRaw 直接发 PCM(免 opus)
	Device         DeviceProfile // 设备画像，留空用 DefaultDevice
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
		events <- Event{Type: EventStreamStarted, RequestID: requestID}
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
	stats := newSessionStats(requestID, opts.RequestTimeout)
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
	header.Set("proto-version", ProtoVersion)
	dialer := c.Config.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}

	// The upstream protocol is a two-step handshake: StartTask authorizes the
	// task, then StartSession declares audio format and recognition options.
	conn, _, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return true, stats.wrap("dial upstream websocket", err)
	}
	defer conn.Close()
	trace := newFrameTrace(c.Config.TraceWriter)
	send := func(req asrproto.Request) error { return sendPBTrace(conn, trace, req) }
	read := func() (asrproto.Response, error) { return readPBTrace(conn, trace) }
	if err := send(asrproto.Request{Token: creds.Token, ServiceName: ServiceNameASR, MethodName: MethodStartTask, RequestID: requestID}); err != nil {
		return true, stats.wrap("send StartTask", err)
	}
	resp, err := read()
	if err != nil {
		return true, stats.wrap("read StartTask response", err)
	}
	stats.received(resp, source.Duration())
	if resp.MessageType == MessageTaskFailed {
		return true, fmt.Errorf("StartTask failed (code=%d): %s", resp.StatusCode, resp.StatusMessage)
	}
	device := c.Config.Device
	if device.Model == "" {
		device = DefaultDevice
	}
	sess := DefaultSessionConfig(creds.DeviceID, device)
	sess.EnablePunctuation = opts.EnablePunctuation
	sess.EnableASRThreePass = opts.EnableThreePass
	sess.EnableASRTwoPass = opts.EnableTwoPass
	sess.Context = opts.Prompt
	if c.Config.AudioFormat != "" {
		sess.AudioFormat = c.Config.AudioFormat
	}
	sess.ApplyDynamic(creds.Dynamic) // settings 下发的 asr_config 动态覆盖
	sessionPayload, _ := json.Marshal(sess.ToPayload())
	if err := send(asrproto.Request{Token: creds.Token, ServiceName: ServiceNameASR, MethodName: MethodStartSession, Payload: string(sessionPayload), RequestID: requestID}); err != nil {
		return true, stats.wrap("send StartSession", err)
	}
	resp, err = read()
	if err != nil {
		return true, stats.wrap("read StartSession response", err)
	}
	stats.received(resp, source.Duration())
	if resp.MessageType == MessageSessionFailed {
		return true, fmt.Errorf("StartSession failed (code=%d): %s", resp.StatusCode, resp.StatusMessage)
	}

	// Send and receive concurrently so partial transcripts are forwarded while
	// audio is still being uploaded. This is the core streaming path.
	sendErr := make(chan error, 1)
	recvErr := make(chan error, 1)
	done := make(chan struct{})
	finishedSending := make(chan struct{})
	var closeDone sync.Once
	stopRecv := func() {
		closeDone.Do(func() {
			close(done)
			_ = conn.Close()
		})
	}
	waitRecv := func() {
		select {
		case <-recvErr:
		case <-time.After(time.Second):
		}
	}
	go func() {
		sendErr <- c.sendAudio(ctx, conn, trace, stats, requestID, creds.Token, source, encoder)
	}()
	go func() {
		recvErr <- c.recv(ctx, conn, trace, stats, requestID, source, events, done, finishedSending)
	}()
	select {
	case err := <-sendErr:
		if err != nil {
			// send 写失败常因服务端已收完音频、识别完成后主动关闭连接（broken pipe）。
			// 优先看 recv：已拿到完整结果则视为成功，忽略 send 错误。
			select {
			case rerr := <-recvErr:
				stopRecv()
				if rerr != nil {
					return false, stats.wrap("receive upstream response", rerr)
				}
				return false, nil
			case <-time.After(finishSessionWaitTimeout):
				stopRecv()
				waitRecv()
				return false, stats.wrap("send audio", err)
			}
		}
		close(finishedSending)
		// After FinishSession the backend may close without an explicit final
		// message. Bound the wait and let recv synthesize transcript.final.
		_ = conn.SetReadDeadline(time.Now().Add(finishSessionWaitTimeout))
		select {
		case err := <-recvErr:
			return false, stats.wrap("receive final response", err)
		case <-ctx.Done():
			stopRecv()
			waitRecv()
			return false, stats.wrap("wait for final response", ctx.Err())
		}
	case err := <-recvErr:
		if err != nil {
			return false, stats.wrap("receive upstream response", err)
		}
		stopRecv()
		return false, nil
	case <-ctx.Done():
		stopRecv()
		waitRecv()
		return false, stats.wrap("transcription session", ctx.Err())
	}
}

// sendAudio 流式发送音频帧：中间帧 payload 为空对象，末帧带 {"finish_audio":true}；
// format=raw 直接发 PCM，否则 opus 编码。末帧标志靠一帧 lookahead 放到最后一帧。
func (c Client) sendAudio(ctx context.Context, conn *websocket.Conn, trace *frameTrace, stats *sessionStats, requestID, token string, source PCMFrameSource, encoder PCMFrameEncoder) error {
	raw := c.Config.AudioFormat == AudioFormatRaw
	frameIndex := 0

	sendFrame := func(pcm []byte, state asrproto.FrameState, finish bool) error {
		audio := pcm
		if !raw {
			var err error
			if audio, err = encoder.EncodePCMFrame(pcm); err != nil {
				return err
			}
		}
		flags := map[string]any{}
		if finish {
			flags["finish_audio"] = true // 末帧标志
		}
		payload, _ := json.Marshal(flags)
		if err := sendPBTrace(conn, trace, asrproto.Request{
			ServiceName: ServiceNameASR, MethodName: MethodTaskRequest,
			Payload: string(payload), AudioData: audio, RequestID: requestID, FrameState: state,
		}); err != nil {
			return err
		}
		frameIndex++
		stats.sentFrame(frameIndex, source.Duration())
		return nil
	}

	var prev []byte
	havePrev := false
	for {
		pcm, ok, err := source.NextFrame()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if havePrev { // prev 不是末帧，按 首/中 帧发送
			state := asrproto.FrameStateMiddle
			if frameIndex == 0 {
				state = asrproto.FrameStateFirst
			}
			if err := sendFrame(prev, state, false); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		prev = append([]byte(nil), pcm...) // 拷贝，NextFrame 可能复用底层缓冲
		havePrev = true
	}
	if havePrev { // 最后一帧：末帧标志 finish_audio
		if err := sendFrame(prev, asrproto.FrameStateLast, true); err != nil {
			return err
		}
	}
	return sendPBTrace(conn, trace, asrproto.Request{Token: token, ServiceName: ServiceNameASR, MethodName: MethodFinishSession, RequestID: requestID})
}

func (c Client) recv(ctx context.Context, conn *websocket.Conn, trace *frameTrace, stats *sessionStats, requestID string, source PCMFrameSource, events chan<- Event, done <-chan struct{}, finishedSending <-chan struct{}) error {
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
				// send 已完成后读到任何错误（超时、连接被关、异常断开）都意味着会话结束；
				// 期间已收到过结果就合成最终事件。
				if lastText != "" {
					events <- Event{Type: EventTranscriptCompleted, RequestID: requestID, Text: lastText, Duration: source.Duration().Seconds()}
					return nil
				}
				if isTimeout(err) {
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
				events <- Event{Type: EventTranscriptCompleted, RequestID: requestID, Text: lastText, Duration: source.Duration().Seconds()}
				return nil
			}
			return err
		}
		stats.received(resp, source.Duration())
		switch resp.MessageType {
		case MessageTaskFailed, MessageSessionFailed:
			return fmt.Errorf("%s (code=%d): %s", resp.MessageType, resp.StatusCode, resp.StatusMessage)
		case MessageSessionFinished:
			if finalEmitted {
				return nil
			}
			events <- Event{Type: EventTranscriptCompleted, RequestID: requestID, Text: lastText, Duration: source.Duration().Seconds()}
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
		case ParsedDelta:
			events <- Event{Type: EventTranscriptDelta, RequestID: requestID, Text: parsed.Text, IsInterim: parsed.IsInterim, Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
		case ParsedFinal:
			events <- Event{Type: EventTranscriptCompleted, RequestID: requestID, Text: parsed.Text, Duration: source.Duration().Seconds(), Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
			finalEmitted = true
			lastText = ""
		}
	}
}

type sessionStats struct {
	mu              sync.Mutex
	requestID       string
	requestTimeout  time.Duration
	started         time.Time
	framesSent      int
	audioDuration   time.Duration
	lastRecvAgoFrom time.Time
	lastMessageType string
	lastTextLen     int
}

func newSessionStats(requestID string, timeout time.Duration) *sessionStats {
	return &sessionStats{requestID: requestID, requestTimeout: timeout, started: time.Now()}
}

func (s *sessionStats) sentFrame(count int, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.framesSent = count
	s.audioDuration = duration
}

func (s *sessionStats) received(resp asrproto.Response, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audioDuration = duration
	s.lastRecvAgoFrom = time.Now()
	s.lastMessageType = string(resp.MessageType)
	s.lastTextLen = resultTextLen(resp.ResultJSON)
}

func (s *sessionStats) wrap(stage string, err error) error {
	if err == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := []string{
		fmt.Sprintf("stage=%s", stage),
		fmt.Sprintf("request_id=%s", s.requestID),
		fmt.Sprintf("elapsed=%s", time.Since(s.started).Round(time.Millisecond)),
		fmt.Sprintf("audio_duration=%s", s.audioDuration.Round(time.Millisecond)),
		fmt.Sprintf("frames_sent=%d", s.framesSent),
	}
	if s.requestTimeout > 0 {
		fields = append(fields, fmt.Sprintf("request_timeout=%s", s.requestTimeout))
	}
	if s.lastMessageType != "" {
		fields = append(fields, fmt.Sprintf("last_message_type=%s", s.lastMessageType))
		if !s.lastRecvAgoFrom.IsZero() {
			fields = append(fields, fmt.Sprintf("last_recv_ago=%s", time.Since(s.lastRecvAgoFrom).Round(time.Millisecond)))
		}
	}
	if s.lastTextLen > 0 {
		fields = append(fields, fmt.Sprintf("last_text_len=%d", s.lastTextLen))
	}
	return fmt.Errorf("%w (%s)", err, strings.Join(fields, ", "))
}

func resultTextLen(resultJSON string) int {
	parsed, err := ParseResultJSON(resultJSON)
	if err != nil {
		return 0
	}
	return len([]rune(parsed.Text))
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
