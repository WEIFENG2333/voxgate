package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	asrproto "github.com/WEIFENG2333/voxgate/internal/proto"
)

type fakeSource struct {
	frames [][]byte
	i      int
}

func TestSessionStatsWrapAddsDiagnosticContext(t *testing.T) {
	stats := newSessionStats("req_test", 2*time.Second)
	stats.sentFrame(3, 60*time.Millisecond)
	err := stats.wrap("receive upstream response", context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wrapped error does not preserve deadline exceeded: %v", err)
	}
	out := err.Error()
	for _, want := range []string{"stage=receive upstream response", "request_id=req_test", "frames_sent=3", "audio_duration=60ms", "request_timeout=2s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("error missing %q: %s", want, out)
		}
	}
}

func (f *fakeSource) NextFrame() ([]byte, bool, error) {
	if f.i >= len(f.frames) {
		return nil, false, nil
	}
	p := f.frames[f.i]
	f.i++
	return p, true, nil
}
func (f *fakeSource) Duration() time.Duration { return time.Second }
func (f *fakeSource) Close() error            { return nil }

type fakeEncoder struct{}

func (fakeEncoder) EncodePCMFrame(p []byte) ([]byte, error) { return []byte{1, 2, 3}, nil }
func (fakeEncoder) Close() error                            { return nil }

type dynamicDurationSource struct {
	frames [][]byte
	i      atomic.Int32
}

func (s *dynamicDurationSource) NextFrame() ([]byte, bool, error) {
	i := int(s.i.Load())
	if i >= len(s.frames) {
		return nil, false, nil
	}
	s.i.Add(1)
	return s.frames[i], true, nil
}

func (s *dynamicDurationSource) Duration() time.Duration {
	return time.Duration(s.i.Load()) * time.Second
}

func (s *dynamicDurationSource) Close() error { return nil }

func TestClientStreamsCumulativeFullText(t *testing.T) {
	upgrader := websocket.Upgrader{}
	// The upstream sends the whole transcript so far in every frame, growing and
	// revising it in place; the client forwards each snapshot as a partial.
	snapshots := []string{"今天", "今天天气真不错。", "今天天气真不错。我们"}
	final := "今天天气真不错，我们一起去公园散步吧。"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskStarted", StatusMessage: "OK"}))
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionStarted", StatusMessage: "OK"}))
		// Pace sends to received audio frames (one growing snapshot per frame) and
		// only finish on FinishSession, so the connection is never closed while the
		// client is still sending (which would break its send pipe).
		next := 0
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			method, _ := parseTestRequestMethod(data)
			if method == "FinishSession" {
				sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": final, "is_interim": false, "is_vad_finished": true, "index": 0, "extra": map[string]any{"nonstream_result": true}}}})
				_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionFinished", StatusMessage: "OK"}))
				return
			}
			if method == "TaskRequest" && next < len(snapshots) {
				sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": snapshots[next], "is_interim": true, "index": 0}}})
				next++
			}
		}
	}))
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := SaveCredentials(credPath, Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + srv.URL[len("http"):]
	client := Client{Config: ClientConfig{CredentialPath: credPath, WebSocketURL: wsURL}}
	events, err := client.Transcribe(context.Background(), &fakeSource{frames: [][]byte{{0}, {0}}}, fakeEncoder{}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var partials []string
	var done string
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
		if ev.Type == EventTranscriptPartial {
			partials = append(partials, ev.Text)
		}
		if ev.Type == EventTranscriptDone {
			done = ev.Text
		}
	}
	// Partials are the distinct snapshots, forwarded verbatim and in order.
	want := append(append([]string{}, snapshots...), final)
	if len(partials) != len(want) {
		t.Fatalf("partials = %#v, want %#v", partials, want)
	}
	for i := range want {
		if partials[i] != want[i] {
			t.Fatalf("partials = %#v, want %#v", partials, want)
		}
	}
	// done carries the final full text.
	if done != final {
		t.Fatalf("done = %q, want %q", done, final)
	}
	_ = os.Remove(credPath)
}

func TestClientReportsStableDurationAfterAudioIsSent(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskStarted", StatusMessage: "OK"}))
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionStarted", StatusMessage: "OK"}))
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			method, _ := parseTestRequestMethod(data)
			if method == "FinishSession" {
				sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "完成", "is_interim": false, "is_vad_finished": true, "extra": map[string]any{"nonstream_result": true}}}})
				_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionFinished", StatusMessage: "OK"}))
				return
			}
		}
	}))
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := SaveCredentials(credPath, Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	client := Client{Config: ClientConfig{CredentialPath: credPath, WebSocketURL: "ws" + strings.TrimPrefix(srv.URL, "http")}}
	src := &dynamicDurationSource{frames: [][]byte{{0}, {0}}}
	events, err := client.Transcribe(context.Background(), src, fakeEncoder{}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var doneDuration float64
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
		if ev.Type == EventTranscriptDone {
			doneDuration = ev.Duration
		}
	}
	if doneDuration != 2 {
		t.Fatalf("done duration = %.0f, want 2", doneDuration)
	}
}

func drainEvents(events chan Event) []Event {
	close(events)
	var out []Event
	for ev := range events {
		out = append(out, ev)
	}
	return out
}

func TestTranscriptStateEmitsCumulativePartials(t *testing.T) {
	state := newTranscriptState("req")
	events := make(chan Event, 16)
	state.emit(events, mustParseResult(t, `{"results":[{"text":"今天","is_interim":true,"index":0}]}`))
	state.emit(events, mustParseResult(t, `{"results":[{"text":"今天天气","is_interim":true,"index":0}]}`))
	state.emit(events, mustParseResult(t, `{"results":[{"text":"今天天气真不错。","is_interim":false,"is_vad_finished":true,"index":0}]}`))
	got := drainEvents(events)

	// Every frame is forwarded as a cumulative partial; there are no finals.
	want := []string{"今天", "今天天气", "今天天气真不错。"}
	if len(got) != len(want) {
		t.Fatalf("events = %#v", got)
	}
	for i, w := range want {
		if got[i].Type != EventTranscriptPartial || got[i].Text != w {
			t.Fatalf("event[%d] = %#v, want partial %q", i, got[i], w)
		}
	}
	// The latest snapshot is the final full text delivered by done.
	if state.text() != "今天天气真不错。" {
		t.Fatalf("tail = %q, want latest snapshot", state.text())
	}
}

func TestTranscriptStateForwardsSnapshotsVerbatim(t *testing.T) {
	state := newTranscriptState("req")
	events := make(chan Event, 16)
	state.emit(events, mustParseResult(t, `{"results":[{"text":"今天天气真不错。","is_interim":false,"is_vad_finished":true,"index":0}]}`))
	state.emit(events, mustParseResult(t, `{"results":[{"text":"今天天气真不错。我们出去走走吧。","is_interim":false,"is_vad_finished":true,"index":0}]}`))
	got := drainEvents(events)

	// Each snapshot is forwarded verbatim — the client never concatenates.
	if len(got) != 2 {
		t.Fatalf("events = %#v, want two partials", got)
	}
	if got[0].Text != "今天天气真不错。" || got[1].Text != "今天天气真不错。我们出去走走吧。" {
		t.Fatalf("texts = %q,%q, want the two snapshots verbatim", got[0].Text, got[1].Text)
	}
	if state.text() != "今天天气真不错。我们出去走走吧。" {
		t.Fatalf("tail = %q, want latest snapshot", state.text())
	}
}

func TestTranscriptStateDedupesUnchangedPartials(t *testing.T) {
	state := newTranscriptState("req")
	events := make(chan Event, 8)
	state.emit(events, mustParseResult(t, `{"results":[{"text":"今天","is_interim":true,"index":0}]}`))
	state.emit(events, mustParseResult(t, `{"results":[{"text":"今天","is_interim":true,"index":0}]}`))
	if got := drainEvents(events); len(got) != 1 {
		t.Fatalf("events = %#v, want one (duplicate partial suppressed)", got)
	}
}

func TestTranscriptStateDoneTailIsLatestSnapshot(t *testing.T) {
	state := newTranscriptState("req")
	events := make(chan Event, 8)
	state.emit(events, mustParseResult(t, `{"results":[{"text":"今天天气真不错。","is_interim":false,"is_vad_finished":true,"index":0}]}`))
	state.emit(events, mustParseResult(t, `{"results":[{"text":"今天天气真不错。还没说完","is_interim":true,"index":0}]}`))
	drainEvents(events)
	if state.text() != "今天天气真不错。还没说完" {
		t.Fatalf("tail = %q, want the latest cumulative snapshot", state.text())
	}
}

func mustParseResult(t *testing.T, s string) ParsedResult {
	t.Helper()
	got, err := ParseResultJSON(s)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestClientStartSessionUsesConfiguredAudioFormat(t *testing.T) {
	gotFormat := make(chan string, 1)
	gotStrongDDC := make(chan bool, 1)
	gotFinalExtra := make(chan map[string]bool, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskStarted", StatusMessage: "OK"}))
		_, data, _ := conn.ReadMessage()
		payload, _ := parseTestRequestPayload(data)
		var session struct {
			AudioInfo struct {
				Format string `json:"format"`
			} `json:"audio_info"`
			Extra struct {
				StrongDDC bool `json:"strong_ddc"`
			} `json:"extra"`
		}
		_ = json.Unmarshal([]byte(payload), &session)
		gotFormat <- session.AudioInfo.Format
		gotStrongDDC <- session.Extra.StrongDDC
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionStarted", StatusMessage: "OK"}))
		var lastTaskPayload string
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			method, _ := parseTestRequestMethod(data)
			if method == "TaskRequest" {
				lastTaskPayload, _ = parseTestRequestPayload(data)
			}
			if method == "FinishSession" {
				var task struct {
					Extra map[string]bool `json:"extra"`
				}
				_ = json.Unmarshal([]byte(lastTaskPayload), &task)
				gotFinalExtra <- task.Extra
				sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "完成", "is_interim": false, "is_vad_finished": true, "extra": map[string]any{"nonstream_result": true}}}})
				_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionFinished", StatusMessage: "OK"}))
				return
			}
		}
	}))
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := SaveCredentials(credPath, Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	client := Client{Config: ClientConfig{CredentialPath: credPath, WebSocketURL: "ws" + strings.TrimPrefix(srv.URL, "http"), AudioFormat: AudioFormatRaw}}
	events, err := client.Transcribe(context.Background(), &fakeSource{frames: [][]byte{{0}}}, fakeEncoder{}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
	}
	if format := <-gotFormat; format != AudioFormatRaw {
		t.Fatalf("StartSession audio format = %q, want %q", format, AudioFormatRaw)
	}
	if strongDDC := <-gotStrongDDC; !strongDDC {
		t.Fatal("StartSession strong_ddc = false, want true")
	}
	finalExtra := <-gotFinalExtra
	if !finalExtra["force_asr_twopass"] || !finalExtra["finish_audio"] {
		t.Fatalf("final TaskRequest extra = %#v, want force_asr_twopass and finish_audio", finalExtra)
	}
}

func TestClientTraceRecordsRawWebSocketFrames(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskStarted", StatusMessage: "OK"}))
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionStarted", StatusMessage: "OK"}))
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			method, _ := parseTestRequestMethod(data)
			if method == "FinishSession" {
				sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "完成", "is_interim": false, "is_vad_finished": true, "extra": map[string]any{"nonstream_result": true}}}})
				_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionFinished", StatusMessage: "OK"}))
				return
			}
		}
	}))
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := SaveCredentials(credPath, Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	var trace bytes.Buffer
	client := Client{Config: ClientConfig{CredentialPath: credPath, WebSocketURL: "ws" + strings.TrimPrefix(srv.URL, "http"), TraceWriter: &trace}}
	events, err := client.Transcribe(context.Background(), &fakeSource{frames: [][]byte{{0}}}, fakeEncoder{}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
	}
	out := trace.String()
	for _, want := range []string{`"direction":"send"`, `"direction":"recv"`, `"raw_base64":`, `"method_name":"StartTask"`, `"message_type":"TaskStarted"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("trace missing %s:\n%s", want, out)
		}
	}
}

func TestClientReportsErrorForEmptyResultAfterFinishSessionTimeout(t *testing.T) {
	oldTimeout := finishSessionWaitTimeout
	finishSessionWaitTimeout = 50 * time.Millisecond
	defer func() { finishSessionWaitTimeout = oldTimeout }()

	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskStarted", StatusMessage: "OK"}))
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionStarted", StatusMessage: "OK"}))
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			method, _ := parseTestRequestMethod(data)
			if method == "FinishSession" {
				time.Sleep(500 * time.Millisecond)
				return
			}
		}
	}))
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := SaveCredentials(credPath, Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	client := Client{Config: ClientConfig{CredentialPath: credPath, WebSocketURL: "ws" + strings.TrimPrefix(srv.URL, "http")}}
	events, err := client.Transcribe(context.Background(), &fakeSource{frames: [][]byte{{0}}}, fakeEncoder{}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var errorSeen bool
	for ev := range events {
		if ev.Type == EventError {
			errorSeen = true
		}
	}
	if !errorSeen {
		t.Fatal("did not receive error for empty timeout")
	}
}

func TestClientRetriesHandshakeAfterTokenRefreshWithoutConsumingAudio(t *testing.T) {
	var connections atomic.Int32
	var taskRequests atomic.Int32
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		n := connections.Add(1)
		_, _, _ = conn.ReadMessage()
		if n == 1 {
			_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskFailed", StatusCode: 401, StatusMessage: "expired"}))
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskStarted", StatusMessage: "OK"}))
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionStarted", StatusMessage: "OK"}))
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			method, _ := parseTestRequestMethod(data)
			if method == "TaskRequest" {
				taskRequests.Add(1)
			}
			if method == "FinishSession" {
				sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "刷新后成功", "is_interim": false, "is_vad_finished": true, "extra": map[string]any{"nonstream_result": true}}}})
				_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionFinished", StatusMessage: "OK"}))
				return
			}
		}
	}))
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := SaveCredentials(credPath, Credentials{DeviceID: "1", CDID: "c", Token: "expired", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	client := Client{Config: ClientConfig{
		CredentialPath: credPath,
		WebSocketURL:   "ws" + strings.TrimPrefix(srv.URL, "http"),
		HTTP:           &http.Client{Transport: tokenRoundTripper{}},
	}}
	src := &fakeSource{frames: [][]byte{{0}, {0}}}
	events, err := client.Transcribe(context.Background(), src, fakeEncoder{}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
		if ev.Type == EventTranscriptDone && ev.Text != "" {
			text = ev.Text
		}
	}
	if text != "刷新后成功" {
		t.Fatalf("bad transcript text: %q", text)
	}
	if connections.Load() != 2 {
		t.Fatalf("connections = %d, want 2", connections.Load())
	}
	if src.i != 2 {
		t.Fatalf("source consumed %d frames, want 2", src.i)
	}
	if taskRequests.Load() != 3 {
		t.Fatalf("task requests = %d, want 3 including final silence frame", taskRequests.Load())
	}
}

func TestClientReissuesDeviceAfterRepeatedHandshakeFailure(t *testing.T) {
	var connections atomic.Int32
	var taskRequests atomic.Int32
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		n := connections.Add(1)
		_, _, _ = conn.ReadMessage()
		switch n {
		case 1, 2:
			_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskStarted", StatusMessage: "OK"}))
			_, _, _ = conn.ReadMessage()
			_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionFailed", StatusCode: 50700000, StatusMessage: "service discovery failure"}))
			return
		case 3:
			if got := r.URL.Query().Get("device_id"); got != "new-device" {
				t.Errorf("device_id = %q, want new-device", got)
			}
			_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskStarted", StatusMessage: "OK"}))
			_, _, _ = conn.ReadMessage()
			_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionStarted", StatusMessage: "OK"}))
			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				method, _ := parseTestRequestMethod(data)
				if method == "TaskRequest" {
					taskRequests.Add(1)
				}
				if method == "FinishSession" {
					sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "重新注册后成功", "is_interim": false, "is_vad_finished": true, "extra": map[string]any{"nonstream_result": true}}}})
					_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionFinished", StatusMessage: "OK"}))
					return
				}
			}
		default:
			t.Errorf("unexpected connection %d", n)
		}
	}))
	defer srv.Close()

	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := SaveCredentials(credPath, Credentials{DeviceID: "old-device", CDID: "old-cdid", Token: "old-token", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	client := Client{Config: ClientConfig{
		CredentialPath: credPath,
		WebSocketURL:   "ws" + strings.TrimPrefix(srv.URL, "http"),
		HTTP: &http.Client{Transport: httpClientFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "/service/2/device_register/") {
				return jsonResponse(req, `{"device_id_str":"new-device","install_id":"new-install"}`), nil
			}
			return jsonResponse(req, `{"data":{"settings":{"asr_config":{"app_key":"new-token"}}}}`), nil
		})},
	}}
	src := &fakeSource{frames: [][]byte{{0}, {0}}}
	events, err := client.Transcribe(context.Background(), src, fakeEncoder{}, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
		if ev.Type == EventTranscriptDone && ev.Text != "" {
			text = ev.Text
		}
	}
	if text != "重新注册后成功" {
		t.Fatalf("bad transcript text: %q", text)
	}
	if connections.Load() != 3 {
		t.Fatalf("connections = %d, want 3", connections.Load())
	}
	if src.i != 2 {
		t.Fatalf("source consumed %d frames, want 2", src.i)
	}
	if taskRequests.Load() != 3 {
		t.Fatalf("task requests = %d, want 3 including final silence frame", taskRequests.Load())
	}
	creds, err := LoadCredentials(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if creds.DeviceID != "new-device" || creds.Token != "new-token" {
		t.Fatalf("credentials not reissued: %+v", creds)
	}
}

func sendResult(t *testing.T, conn *websocket.Conn, v map[string]any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{ResultJSON: string(data)})); err != nil {
		t.Fatal(err)
	}
}

func parseTestRequestMethod(data []byte) (string, error) {
	for i := 0; i < len(data)-1; i++ {
		if data[i] == 0x2a {
			l := int(data[i+1])
			if i+2+l <= len(data) {
				return string(data[i+2 : i+2+l]), nil
			}
		}
	}
	return "", nil
}

func parseTestRequestPayload(data []byte) (string, error) {
	for i := 0; i < len(data)-1; i++ {
		if data[i] == 0x32 {
			l, n := readTestVarint(data[i+1:])
			if n > 0 && i+1+n+int(l) <= len(data) {
				start := i + 1 + n
				return string(data[start : start+int(l)]), nil
			}
		}
	}
	return "", nil
}

func readTestVarint(data []byte) (uint64, int) {
	var v uint64
	for i, b := range data {
		v |= uint64(b&0x7f) << (7 * i)
		if b < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

type tokenRoundTripper struct{}

func (tokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return jsonResponse(req, `{"data":{"settings":{"asr_config":{"app_key":"fresh"}}}}`), nil
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (f httpClientFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
