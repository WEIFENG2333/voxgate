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
		text, err := c.run(ctx, requestID, source, encoder, opts, events)
		if err != nil {
			events <- Event{Type: EventError, RequestID: requestID, Error: &ErrorPayload{Code: "asr_error", Message: err.Error()}}
			return
		}
		events <- Event{Type: EventTranscriptDone, RequestID: requestID, Text: text, Duration: source.Duration().Seconds()}
	}()
	return events, nil
}

func (c Client) run(ctx context.Context, requestID string, source PCMFrameSource, encoder PCMFrameEncoder, opts Options, events chan<- Event) (string, error) {
	userAgent := c.Config.UserAgent
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	manager := CredentialManager{Path: c.Config.CredentialPath, UserAgent: userAgent, HTTP: c.Config.HTTP}
	creds, err := manager.Ensure(ctx, false)
	if err != nil {
		return "", err
	}
	text, retryable, err := c.runWithCreds(ctx, creds, requestID, source, encoder, opts, events)
	if err == nil {
		return text, nil
	}
	if !retryable {
		return "", err
	}
	// Handshake failures are usually credential-related: try a token refresh
	// first, then fall back to a fresh device identity if the service still
	// rejects the session.
	creds, refreshErr := manager.Ensure(ctx, true)
	if refreshErr == nil {
		text, retryable, err = c.runWithCreds(ctx, creds, requestID, source, encoder, opts, events)
		if err == nil {
			return text, nil
		}
		if !retryable {
			return "", err
		}
	}
	creds, reissueErr := manager.Reissue(ctx)
	if reissueErr != nil {
		return "", err
	}
	text, _, err = c.runWithCreds(ctx, creds, requestID, source, encoder, opts, events)
	return text, err
}

func (c Client) runWithCreds(ctx context.Context, creds Credentials, requestID string, source PCMFrameSource, encoder PCMFrameEncoder, opts Options, events chan<- Event) (string, bool, error) {
	stats := newSessionStats(requestID, opts.RequestTimeout)
	wsURL := c.Config.WebSocketURL
	if wsURL == "" {
		wsURL = WebSocketURL
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		return "", false, err
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
		return "", true, stats.wrap("dial upstream websocket", err)
	}
	defer conn.Close()
	trace := newFrameTrace(c.Config.TraceWriter)
	send := func(req asrproto.Request) error { return sendPBTrace(conn, trace, req) }
	read := func() (asrproto.Response, error) { return readPBTrace(conn, trace) }
	if err := send(asrproto.Request{Token: creds.Token, ServiceName: ServiceNameASR, MethodName: MethodStartTask, RequestID: requestID}); err != nil {
		return "", true, stats.wrap("send StartTask", err)
	}
	resp, err := read()
	if err != nil {
		return "", true, stats.wrap("read StartTask response", err)
	}
	stats.received(resp, source.Duration())
	if resp.MessageType == MessageTaskFailed {
		return "", true, fmt.Errorf("StartTask failed (code=%d): %s", resp.StatusCode, resp.StatusMessage)
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
		return "", true, stats.wrap("send StartSession", err)
	}
	resp, err = read()
	if err != nil {
		return "", true, stats.wrap("read StartSession response", err)
	}
	stats.received(resp, source.Duration())
	if resp.MessageType == MessageSessionFailed {
		return "", true, fmt.Errorf("StartSession failed (code=%d): %s", resp.StatusCode, resp.StatusMessage)
	}
	events <- Event{Type: EventSessionStarted, RequestID: requestID}

	// Send and receive concurrently so partial transcripts are forwarded while
	// audio is still being uploaded. This is the core streaming path.
	sendErr := make(chan error, 1)
	recvDone := make(chan recvResult, 1)
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
		case <-recvDone:
		case <-time.After(time.Second):
		}
	}
	go func() {
		sendErr <- c.sendAudio(ctx, conn, trace, stats, requestID, creds.Token, source, encoder, opts.Realtime)
	}()
	go func() {
		text, err := c.recv(ctx, conn, trace, stats, requestID, source, events, done, finishedSending)
		recvDone <- recvResult{text: text, err: err}
	}()
	select {
	case err := <-sendErr:
		if err != nil {
			stopRecv()
			waitRecv()
			return "", false, stats.wrap("send audio", err)
		}
		close(finishedSending)
		// After FinishSession, bound the wait for the backend's final result or
		// session terminal response.
		_ = conn.SetReadDeadline(time.Now().Add(finishSessionWaitTimeout))
		select {
		case recv := <-recvDone:
			return recv.text, false, stats.wrap("receive final response", recv.err)
		case <-ctx.Done():
			stopRecv()
			waitRecv()
			return "", false, stats.wrap("wait for final response", ctx.Err())
		}
	case recv := <-recvDone:
		if recv.err != nil {
			return "", false, stats.wrap("receive upstream response", recv.err)
		}
		stopRecv()
		return recv.text, false, nil
	case <-ctx.Done():
		stopRecv()
		waitRecv()
		return "", false, stats.wrap("transcription session", ctx.Err())
	}
}

type recvResult struct {
	text string
	err  error
}

func (c Client) sendAudio(ctx context.Context, conn *websocket.Conn, trace *frameTrace, stats *sessionStats, requestID, token string, source PCMFrameSource, encoder PCMFrameEncoder, realtime bool) error {
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
		stats.sentFrame(frameIndex, source.Duration())
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
		stats.sentFrame(frameIndex+1, source.Duration())
	}
	return sendPBTrace(conn, trace, asrproto.Request{Token: token, ServiceName: ServiceNameASR, MethodName: MethodFinishSession, RequestID: requestID})
}

func (c Client) recv(ctx context.Context, conn *websocket.Conn, trace *frameTrace, stats *sessionStats, requestID string, source PCMFrameSource, events chan<- Event, done <-chan struct{}, finishedSending <-chan struct{}) (string, error) {
	state := newTranscriptState(requestID)
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-done:
			return state.text(), nil
		default:
		}
		resp, err := readPBTrace(conn, trace)
		if err != nil {
			select {
			case <-finishedSending:
				if isTimeout(err) {
					if state.hasTranscript() {
						return state.text(), nil
					}
					return "", err
				}
			default:
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				if !state.hasTranscript() {
					return "", fmt.Errorf("websocket closed normally before any transcript result")
				}
				return state.text(), nil
			}
			return "", err
		}
		stats.received(resp, source.Duration())
		switch resp.MessageType {
		case MessageTaskFailed, MessageSessionFailed:
			return "", fmt.Errorf("%s (code=%d): %s", resp.MessageType, resp.StatusCode, resp.StatusMessage)
		case MessageSessionFinished:
			return state.text(), nil
		}
		// Recognition payloads arrive as JSON nested inside the protobuf frame.
		// Parser classification keeps the transport code independent from the
		// upstream's three-pass result details.
		parsed, err := ParseResultJSON(resp.ResultJSON)
		if err != nil || parsed.Kind == ParsedNoop {
			continue
		}
		if parsed.Kind == ParsedVADStart {
			state.startUtterance()
			events <- Event{Type: EventVADStart, RequestID: requestID, TimestampMS: int64(time.Since(start) / time.Millisecond), Results: parsed.Results, Extra: &parsed.Extra, Raw: parsed.Raw}
			continue
		}
		switch parsed.Kind {
		case ParsedInterim:
			state.emitPartial(events, parsed)
		case ParsedDefinite:
			state.emitPartial(events, parsed)
		case ParsedStable:
			state.emitStable(events, parsed, source.Duration().Seconds())
		}
	}
}

type transcriptState struct {
	requestID       string
	stableSeq       int
	revision        int
	displaySnapshot string
	lastStableText  string
}

func newTranscriptState(requestID string) *transcriptState {
	return &transcriptState{requestID: requestID}
}

func (s *transcriptState) hasTranscript() bool {
	return s.displaySnapshot != "" || s.lastStableText != ""
}

func (s *transcriptState) text() string {
	if s.displaySnapshot != "" {
		return s.displaySnapshot
	}
	return s.lastStableText
}

func (s *transcriptState) startUtterance() {
	// VAD is useful as a marker, but upstream may keep revising the same
	// composing transcript across multiple VAD slices.
}

func (s *transcriptState) emitPartial(events chan<- Event, parsed ParsedResult) {
	s.emitDisplay(events, parsed, 0)
}

func (s *transcriptState) emitStable(events chan<- Event, parsed ParsedResult, duration float64) {
	s.emitDisplay(events, parsed, 0)
	stableSnapshot := displayText(parsed)
	if stableSnapshot == "" || stableSnapshot == s.lastStableText {
		return
	}
	s.lastStableText = stableSnapshot
	events <- s.stableEvent(parsed, stableSnapshot, duration)
}

func (s *transcriptState) emitDisplay(events chan<- Event, parsed ParsedResult, duration float64) {
	snapshot := displayText(parsed)
	if snapshot == "" || snapshot == s.displaySnapshot {
		return
	}
	s.revision++
	delta, appendOnly := textDelta(s.displaySnapshot, snapshot)
	s.displaySnapshot = snapshot
	if appendOnly {
		events <- s.event(EventTranscriptDelta, parsed, delta, snapshot, duration)
		return
	}
	events <- s.event(EventTranscriptUpdate, parsed, snapshot, snapshot, duration)
}

func displayText(parsed ParsedResult) string {
	if parsed.Snapshot != "" {
		return parsed.Snapshot
	}
	return parsed.Text
}

func (s *transcriptState) event(kind EventType, parsed ParsedResult, text, snapshot string, duration float64) Event {
	ev := Event{
		Type:         kind,
		RequestID:    s.requestID,
		Text:         text,
		Snapshot:     snapshot,
		Revision:     s.revision,
		IsInterim:    kind != EventTranscriptDone && parsed.Kind != ParsedStable,
		Start:        parsed.Start,
		End:          parsed.End,
		AudioStartMS: secondsToMillis(parsed.Start),
		AudioEndMS:   secondsToMillis(parsed.End),
		Duration:     duration,
		Results:      parsed.Results,
		Extra:        &parsed.Extra,
		Raw:          parsed.Raw,
	}
	if kind == EventTranscriptDelta {
		ev.Delta = text
	}
	return ev
}

func (s *transcriptState) stableEvent(parsed ParsedResult, snapshot string, duration float64) Event {
	s.stableSeq++
	return Event{
		Type:         EventSegmentStable,
		RequestID:    s.requestID,
		Text:         snapshot,
		Snapshot:     snapshot,
		UtteranceID:  fmt.Sprintf("seg_%06d", s.stableSeq-1),
		Revision:     s.revision,
		Start:        parsed.Start,
		End:          parsed.End,
		AudioStartMS: secondsToMillis(parsed.Start),
		AudioEndMS:   secondsToMillis(parsed.End),
		Duration:     duration,
		Results:      parsed.Results,
		Extra:        &parsed.Extra,
		Raw:          parsed.Raw,
	}
}

func textDelta(previous, next string) (string, bool) {
	if previous == "" {
		return next, true
	}
	if strings.HasPrefix(next, previous) {
		return strings.TrimPrefix(next, previous), true
	}
	return "", false
}

func secondsToMillis(seconds float64) int64 {
	if seconds <= 0 {
		return 0
	}
	return int64(seconds * 1000)
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
