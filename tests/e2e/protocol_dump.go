//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
	asrproto "github.com/WEIFENG2333/voxgate/internal/proto"
)

const (
	wireOpus = "opus"
	wirePCM  = "pcm"

	defaultSilenceFrames = 150
	probeTimeout         = 90 * time.Second
	finalWaitTimeout     = 30 * time.Second
)

type config struct {
	AudioPath          string
	CredentialPath     string
	SessionAudioFormat string
	WireAudioFormat    string
	MaxFrames          int
	FramesPerRequest   int
	SilenceFrames      int
	Realtime           bool
	ConcurrentRead     bool
	ProbeContinuation  bool
}

func main() {
	cfg := parseFlags()
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.AudioPath, "audio", "tests/audio/zh_5s.wav", "audio file to send")
	flag.StringVar(&cfg.CredentialPath, "credential-path", "", "credential cache path")
	flag.StringVar(&cfg.SessionAudioFormat, "session-audio-format", asr.AudioFormatSpeechOpus, "StartSession audio_info.format")
	flag.StringVar(&cfg.WireAudioFormat, "wire-audio-format", wireOpus, "TaskRequest audio_data encoding: opus|pcm")
	flag.IntVar(&cfg.MaxFrames, "max-frames", 0, "limit 20ms audio frames; 0 sends the whole file")
	flag.IntVar(&cfg.FramesPerRequest, "frames-per-request", 1, "20ms PCM frames per TaskRequest")
	flag.IntVar(&cfg.SilenceFrames, "silence-frames", defaultSilenceFrames, "silence frames after each utterance for continuation probe")
	flag.BoolVar(&cfg.Realtime, "realtime", false, "sleep according to logical audio duration between TaskRequests")
	flag.BoolVar(&cfg.ConcurrentRead, "concurrent-read", false, "read upstream responses while sending audio")
	flag.BoolVar(&cfg.ProbeContinuation, "probe-continuation", false, "send two utterances in one session without FinishSession between them")
	flag.Parse()

	if cfg.CredentialPath == "" {
		cfg.CredentialPath = fmt.Sprintf("%s/voxgate-protocol-dump-%d.json", os.TempDir(), time.Now().UnixNano())
	}
	if cfg.FramesPerRequest <= 0 {
		cfg.FramesPerRequest = 1
	}
	if cfg.WireAudioFormat == wireOpus {
		// Opus packets already have codec framing. Do not concatenate multiple
		// encoded packets into one TaskRequest unless the upstream protocol
		// explicitly documents that shape.
		cfg.FramesPerRequest = 1
	}
	return cfg
}

func run(ctx context.Context, cfg config) error {
	emit("probe.start", map[string]any{
		"audio":                cfg.AudioPath,
		"credential_path":      cfg.CredentialPath,
		"session_audio_format": cfg.SessionAudioFormat,
		"wire_audio_format":    cfg.WireAudioFormat,
		"frames_per_request":   cfg.FramesPerRequest,
		"realtime":             cfg.Realtime,
		"concurrent_read":      cfg.ConcurrentRead,
		"probe_continuation":   cfg.ProbeContinuation,
	})

	creds, err := loadCredentials(ctx, cfg)
	if err != nil {
		return err
	}
	src, err := audio.ConvertFile(ctx, cfg.AudioPath)
	if err != nil {
		return err
	}
	defer src.Close()

	conn, err := dial(ctx, creds)
	if err != nil {
		return err
	}
	defer conn.Close()

	requestID := uuid.NewString()
	probe := &sessionProbe{
		cfg:       cfg,
		conn:      conn,
		creds:     creds,
		requestID: requestID,
		sender:    newAudioSender(cfg),
	}
	if err := probe.startSession(); err != nil {
		return err
	}
	if cfg.ProbeContinuation {
		return probe.runContinuation(src)
	}
	return probe.runSingle(ctx, src)
}

func loadCredentials(ctx context.Context, cfg config) (asr.Credentials, error) {
	manager := asr.CredentialManager{Path: cfg.CredentialPath, UserAgent: asr.DefaultUserAgent}
	creds, err := manager.Ensure(ctx, true)
	if err != nil {
		return asr.Credentials{}, err
	}
	emit("credentials", map[string]any{
		"device_id":  creds.DeviceID,
		"install_id": creds.InstallID,
		"has_token":  creds.Token != "",
		"cdid_len":   len(creds.CDID),
	})
	return creds, nil
}

func dial(ctx context.Context, creds asr.Credentials) (*websocket.Conn, error) {
	u, err := url.Parse(asr.WebSocketURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("aid", strconv.Itoa(asr.AID))
	q.Set("device_id", creds.DeviceID)
	u.RawQuery = q.Encode()

	header := http.Header{}
	header.Set("User-Agent", asr.DefaultUserAgent)
	header.Set("proto-version", "v2")
	header.Set("x-custom-keepalive", "true")
	emit("ws.connect.request", map[string]any{"url": u.String(), "headers": header})

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if err != nil {
		if resp != nil {
			emit("ws.connect.error", map[string]any{"status": resp.Status, "headers": resp.Header})
		}
		return nil, err
	}
	if resp != nil {
		emit("ws.connect.response", map[string]any{"status": resp.Status, "headers": resp.Header})
	}
	return conn, nil
}

type sessionProbe struct {
	cfg       config
	conn      *websocket.Conn
	creds     asr.Credentials
	requestID string
	sender    *audioSender
}

func (p *sessionProbe) startSession() error {
	p.send(asr.MethodStartTask, asrproto.Request{
		Token:       p.creds.Token,
		ServiceName: asr.ServiceNameASR,
		MethodName:  asr.MethodStartTask,
		RequestID:   p.requestID,
	})
	if _, _, err := p.read(asr.MethodStartTask); err != nil {
		return err
	}

	payload, _ := json.Marshal(map[string]any{
		"audio_info":              map[string]any{"channel": asr.UpstreamChannels, "format": p.cfg.SessionAudioFormat, "sample_rate": asr.UpstreamSampleRate},
		"enable_punctuation":      true,
		"enable_speech_rejection": false,
		"extra": map[string]any{
			"app_name": "com.android.chrome", "cell_compress_rate": 8, "did": p.creds.DeviceID,
			"enable_asr_threepass": true, "enable_asr_twopass": true, "input_mode": "tool",
		},
	})
	p.send(asr.MethodStartSession, asrproto.Request{
		Token:       p.creds.Token,
		ServiceName: asr.ServiceNameASR,
		MethodName:  asr.MethodStartSession,
		Payload:     string(payload),
		RequestID:   p.requestID,
	})
	_, _, err := p.read(asr.MethodStartSession)
	return err
}

func (p *sessionProbe) runSingle(ctx context.Context, src *audio.Source) error {
	readDone := make(chan struct{})
	if p.cfg.ConcurrentRead {
		go func() {
			defer close(readDone)
			p.readUntilTerminal("recv")
		}()
	}

	start := time.Now().UnixMilli()
	frames, err := p.sender.sendSource(p.conn, src, p.requestID, start, 0, p.cfg.MaxFrames, true)
	if err != nil {
		return err
	}
	if frames > 0 {
		if _, err := p.sender.sendSilence(p.conn, p.requestID, start, frames, 1, asrproto.FrameStateLast); err != nil {
			return err
		}
	}
	p.send(asr.MethodFinishSession, asrproto.Request{
		Token:       p.creds.Token,
		ServiceName: asr.ServiceNameASR,
		MethodName:  asr.MethodFinishSession,
		RequestID:   p.requestID,
	})
	emit("audio.sent", map[string]any{"frames": frames, "duration_seconds": src.Duration().Seconds()})

	if p.cfg.ConcurrentRead {
		select {
		case <-readDone:
		case <-ctx.Done():
			emit("recv.error", map[string]any{"error": ctx.Err().Error()})
		}
		return nil
	}
	p.readUntilTerminal("recv")
	return nil
}

func (p *sessionProbe) runContinuation(src *audio.Source) error {
	start := time.Now().UnixMilli()
	frameIndex := 0

	firstFrames, err := p.sender.sendSource(p.conn, src.Clone(), p.requestID, start, frameIndex, p.cfg.MaxFrames, true)
	if err != nil {
		return err
	}
	frameIndex += firstFrames
	silenceFrames, err := p.sender.sendSilence(p.conn, p.requestID, start, frameIndex, p.cfg.SilenceFrames, asrproto.FrameStateMiddle)
	if err != nil {
		return err
	}
	frameIndex += silenceFrames
	emit("continuation.first_audio_sent", map[string]any{"frames_total": frameIndex})
	first, err := p.readUntilFinal("continuation.first")
	if err != nil {
		emit("continuation.first.error", map[string]any{"error": err.Error()})
		return nil
	}
	emit("continuation.first.final", map[string]any{"text": first})

	secondFrames, err := p.sender.sendSource(p.conn, src.Clone(), p.requestID, start, frameIndex, p.cfg.MaxFrames, false)
	if err != nil {
		emit("continuation.second_send.error", map[string]any{"error": err.Error()})
		return nil
	}
	frameIndex += secondFrames
	silenceFrames, err = p.sender.sendSilence(p.conn, p.requestID, start, frameIndex, p.cfg.SilenceFrames, asrproto.FrameStateMiddle)
	if err != nil {
		emit("continuation.second_silence.error", map[string]any{"error": err.Error()})
		return nil
	}
	frameIndex += silenceFrames
	emit("continuation.second_audio_sent", map[string]any{"frames_total": frameIndex})
	second, err := p.readUntilFinal("continuation.second")
	if err != nil {
		emit("continuation.second.error", map[string]any{"error": err.Error()})
		return nil
	}
	emit("continuation.second.final", map[string]any{"text": second})

	p.send(asr.MethodFinishSession, asrproto.Request{
		Token:       p.creds.Token,
		ServiceName: asr.ServiceNameASR,
		MethodName:  asr.MethodFinishSession,
		RequestID:   p.requestID,
	})
	emit("probe.done", map[string]any{"mode": "continuation", "frames": frameIndex})
	return nil
}

type audioSender struct {
	wireFormat       string
	framesPerRequest int
	realtime         bool
	encoder          *audio.OpusEncoder
}

func newAudioSender(cfg config) *audioSender {
	s := &audioSender{
		wireFormat:       cfg.WireAudioFormat,
		framesPerRequest: cfg.FramesPerRequest,
		realtime:         cfg.Realtime,
	}
	if s.framesPerRequest <= 0 {
		s.framesPerRequest = 1
	}
	switch s.wireFormat {
	case wireOpus:
		enc, err := audio.NewOpusEncoder()
		if err != nil {
			log.Fatal(err)
		}
		s.encoder = enc
		s.framesPerRequest = 1
	case wirePCM:
	default:
		log.Fatalf("unsupported wire audio format %q", s.wireFormat)
	}
	return s
}

func (s *audioSender) sendSource(conn *websocket.Conn, src *audio.Source, requestID string, timestamp int64, startFrame, maxFrames int, first bool) (int, error) {
	sent := 0
	for {
		if maxFrames > 0 && sent >= maxFrames {
			break
		}
		chunk, frames, err := nextChunk(src, s.framesPerRequest, maxFrames, sent)
		if err != nil {
			return sent, err
		}
		if frames == 0 {
			break
		}
		state := asrproto.FrameStateMiddle
		if first && sent == 0 {
			state = asrproto.FrameStateFirst
		}
		if err := s.sendChunk(conn, requestID, timestamp, startFrame+sent, chunk, state); err != nil {
			return sent, err
		}
		s.sleep(frames)
		sent += frames
	}
	return sent, nil
}

func nextChunk(src *audio.Source, framesPerRequest, maxFrames, sent int) ([]byte, int, error) {
	var chunk []byte
	for len(chunk)/audio.BytesPerFrame < framesPerRequest {
		if maxFrames > 0 && sent+len(chunk)/audio.BytesPerFrame >= maxFrames {
			break
		}
		pcm, ok, err := src.NextFrame()
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			break
		}
		chunk = append(chunk, pcm...)
	}
	return chunk, len(chunk) / audio.BytesPerFrame, nil
}

func (s *audioSender) sendSilence(conn *websocket.Conn, requestID string, timestamp int64, startFrame, count int, state asrproto.FrameState) (int, error) {
	sent := 0
	for sent < count {
		frames := s.framesPerRequest
		if remaining := count - sent; remaining < frames {
			frames = remaining
		}
		chunk := make([]byte, frames*audio.BytesPerFrame)
		if err := s.sendChunk(conn, requestID, timestamp, startFrame+sent, chunk, state); err != nil {
			return sent, err
		}
		s.sleep(frames)
		sent += frames
	}
	return sent, nil
}

func (s *audioSender) sendChunk(conn *websocket.Conn, requestID string, timestamp int64, frameIndex int, pcm []byte, state asrproto.FrameState) error {
	audioData := pcm
	if s.wireFormat == wireOpus {
		if len(pcm) != audio.BytesPerFrame {
			return fmt.Errorf("opus TaskRequest must contain one 20ms PCM frame before encoding, got %d bytes", len(pcm))
		}
		opusFrame, err := s.encoder.EncodePCMFrame(pcm)
		if err != nil {
			return err
		}
		audioData = opusFrame
	}
	payload, _ := json.Marshal(map[string]any{"extra": map[string]any{}, "timestamp_ms": timestamp + int64(frameIndex*20)})
	req := asrproto.Request{ServiceName: asr.ServiceNameASR, MethodName: asr.MethodTaskRequest, Payload: string(payload), AudioData: audioData, RequestID: requestID, FrameState: state}
	if frameIndex < 3 || frameIndex%100 == 0 || state == asrproto.FrameStateLast {
		emit(asr.MethodTaskRequest+".request", requestSummary(req))
	}
	return conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalRequest(req))
}

func (s *audioSender) sleep(frames int) {
	if s.realtime {
		time.Sleep(time.Duration(frames*audio.FrameDurationMS) * time.Millisecond)
	}
}

func (p *sessionProbe) readUntilFinal(stage string) (string, error) {
	deadline := time.Now().Add(finalWaitTimeout)
	_ = p.conn.SetReadDeadline(deadline)
	defer p.conn.SetReadDeadline(time.Time{})
	for {
		resp, _, err := p.read(stage)
		if err != nil {
			return "", err
		}
		if isTerminal(resp.MessageType) {
			return "", fmt.Errorf("unexpected %s: %s", resp.MessageType, resp.StatusMessage)
		}
		text, final := finalText(resp.ResultJSON)
		if final {
			return text, nil
		}
	}
}

func (p *sessionProbe) readUntilTerminal(stage string) {
	for {
		resp, raw, err := p.read(stage)
		if err != nil {
			emit(stage+".error", map[string]any{"error": err.Error()})
			return
		}
		if isTerminal(resp.MessageType) {
			emit("probe.done", map[string]any{"last_message_type": resp.MessageType, "raw_len": len(raw)})
			return
		}
	}
}

func isTerminal(messageType string) bool {
	return messageType == asr.MessageSessionFinished || messageType == asr.MessageTaskFailed || messageType == asr.MessageSessionFailed
}

func finalText(resultJSON string) (string, bool) {
	var r struct {
		Results []struct {
			Text          string `json:"text"`
			IsInterim     *bool  `json:"is_interim"`
			IsVADFinished bool   `json:"is_vad_finished"`
			Extra         struct {
				NonstreamResult bool `json:"nonstream_result"`
			} `json:"extra"`
		} `json:"results"`
	}
	if resultJSON == "" || json.Unmarshal([]byte(resultJSON), &r) != nil {
		return "", false
	}
	var text string
	isInterim := true
	vadFinished := false
	nonstream := false
	for _, item := range r.Results {
		text += item.Text
		if item.IsInterim != nil {
			isInterim = *item.IsInterim
		}
		vadFinished = item.IsVADFinished
		nonstream = item.Extra.NonstreamResult
	}
	return text, text != "" && (nonstream || (!isInterim && vadFinished))
}

func (p *sessionProbe) send(stage string, req asrproto.Request) {
	emit(stage+".request", requestSummary(req))
	if err := p.conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalRequest(req)); err != nil {
		log.Fatal(err)
	}
}

func (p *sessionProbe) read(stage string) (asrproto.Response, []byte, error) {
	for {
		mt, data, err := p.conn.ReadMessage()
		if err != nil {
			return asrproto.Response{}, nil, err
		}
		if mt != websocket.BinaryMessage {
			emit(stage+".nonbinary", map[string]any{"message_type": mt, "len": len(data)})
			continue
		}
		resp, err := asrproto.UnmarshalResponse(data)
		if err != nil {
			return asrproto.Response{}, data, err
		}
		emit(stage+".response", map[string]any{
			"known":           resp,
			"wire_fields":     decodeWireFields(data),
			"result_json":     decodeResultJSON(resp.ResultJSON),
			"result_json_len": len(resp.ResultJSON),
			"raw_len":         len(data),
		})
		return resp, data, nil
	}
}

func requestSummary(req asrproto.Request) map[string]any {
	return map[string]any{
		"fields":      decodeWireFields(asrproto.MarshalRequest(req)),
		"service":     req.ServiceName,
		"method":      req.MethodName,
		"request_id":  req.RequestID,
		"has_token":   req.Token != "",
		"payload":     decodeResultJSON(req.Payload),
		"payload_len": len(req.Payload),
		"audio_len":   len(req.AudioData),
		"frame_state": req.FrameState,
	}
}

func decodeResultJSON(s string) any {
	if s == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}

var emitMu sync.Mutex

func emit(event string, data map[string]any) {
	data["event"] = event
	data["ts"] = time.Now().Format(time.RFC3339Nano)
	emitMu.Lock()
	defer emitMu.Unlock()
	_ = json.NewEncoder(os.Stdout).Encode(data)
}

func decodeWireFields(data []byte) []map[string]any {
	var fields []map[string]any
	for off := 0; off < len(data); {
		tag, n, ok := readVarint(data, off)
		if !ok {
			fields = append(fields, map[string]any{"offset": off, "error": "bad_tag"})
			return fields
		}
		off = n
		field := int(tag >> 3)
		wire := int(tag & 7)
		entry := map[string]any{"field": field, "wire": wire}
		switch wire {
		case 0:
			v, n, ok := readVarint(data, off)
			if !ok {
				entry["error"] = "bad_varint"
				fields = append(fields, entry)
				return fields
			}
			entry["value"] = v
			off = n
		case 2:
			l, n, ok := readVarint(data, off)
			if !ok || n+int(l) > len(data) {
				entry["error"] = "bad_length"
				fields = append(fields, entry)
				return fields
			}
			entry["len"] = int(l)
			off = n + int(l)
		case 1:
			entry["len"] = 8
			off += 8
		case 5:
			entry["len"] = 4
			off += 4
		default:
			entry["error"] = "unsupported_wire"
			fields = append(fields, entry)
			return fields
		}
		fields = append(fields, entry)
	}
	return fields
}

func readVarint(data []byte, off int) (uint64, int, bool) {
	var v uint64
	for shift := uint(0); shift < 64; shift += 7 {
		if off >= len(data) {
			return 0, off, false
		}
		c := data[off]
		off++
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v, off, true
		}
	}
	return 0, off, false
}
