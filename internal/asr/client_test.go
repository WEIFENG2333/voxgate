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

func TestClientStreamsMultipleStableResultsInOneSession(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, msg, _ := conn.ReadMessage()
		req, _ := asrproto.UnmarshalResponse(nil)
		_ = req
		_ = msg
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskStarted", StatusMessage: "OK"}))
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionStarted", StatusMessage: "OK"}))
		_, _, _ = conn.ReadMessage()
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "第一段", "is_interim": true, "index": 0}}})
		_, _, _ = conn.ReadMessage()
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "第一段。", "is_interim": false, "is_vad_finished": true, "index": 0, "extra": map[string]any{"nonstream_result": true}}}})
		_, _, _ = conn.ReadMessage()
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "第二段", "is_interim": true, "index": 1}}})
		_, _, _ = conn.ReadMessage()
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "第二段。", "is_interim": false, "is_vad_finished": true, "index": 1, "extra": map[string]any{"nonstream_result": true}}}})
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionFinished", StatusMessage: "OK"}))
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
	var stable []string
	var done string
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
		if ev.Type == EventSegmentStable {
			stable = append(stable, ev.Text)
		}
		if ev.Type == EventTranscriptDone {
			done = ev.Text
		}
	}
	want := []string{"第一段。", "第二段。"}
	if len(stable) != len(want) {
		t.Fatalf("stable = %#v, want %#v", stable, want)
	}
	for i := range want {
		if stable[i] != want[i] {
			t.Fatalf("stable = %#v, want %#v", stable, want)
		}
	}
	if done != "第二段。" {
		t.Fatalf("done = %q, want latest snapshot", done)
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
		if ev.Type == EventSegmentStable {
			doneDuration = ev.Duration
		}
	}
	if doneDuration != 2 {
		t.Fatalf("done duration = %.0f, want 2", doneDuration)
	}
}

func TestTranscriptStateNormalizesMultiResultSnapshotsToDeltas(t *testing.T) {
	state := newTranscriptState("req_test")
	events := make(chan Event, 16)

	firstStable := mustParseResult(t, `{"results":[{"text":"你好呀。","is_interim":false,"is_vad_finished":true,"extra":{"nonstream_result":true}}]}`)
	state.emitStable(events, firstStable, 2.2)

	secondPartial := mustParseResult(t, `{
		"results":[
			{"text":"你好呀。我觉得今天的天气不错","end_time":32.082,"is_interim":true,"index":0},
			{"text":"我觉得今天的天气不错","start_time":30.062,"end_time":32.082,"is_interim":true,"index":0}
		]
	}`)
	state.emitPartial(events, secondPartial)

	secondStable := mustParseResult(t, `{
		"results":[
			{"text":"你好呀。我觉得今天的天气不错。","end_time":33.032,"is_interim":true,"index":0},
			{"text":"我觉得今天的天气不错。","start_time":17.702,"end_time":33.032,"is_interim":false,"is_vad_finished":true,"index":0,"extra":{"nonstream_result":true}}
		]
	}`)
	state.emitStable(events, secondStable, 33.3)

	duplicateCumulativeStable := mustParseResult(t, `{"results":[{"text":"你好呀，我觉得今天的天气不错。","end_time":43.872,"is_interim":false,"is_vad_finished":true,"extra":{"nonstream_result":true}}]}`)
	state.emitStable(events, duplicateCumulativeStable, 44.3)
	close(events)

	var deltas []string
	var stable []string
	var stableSnapshots []string
	for ev := range events {
		switch ev.Type {
		case EventTranscriptDelta:
			deltas = append(deltas, ev.Delta)
		case EventSegmentStable:
			stable = append(stable, ev.Text)
			stableSnapshots = append(stableSnapshots, ev.Snapshot)
		}
	}
	if got := strings.Join(deltas, ""); got != "你好呀。我觉得今天的天气不错。" {
		t.Fatalf("delta stream = %q", got)
	}
	wantStable := []string{"你好呀。", "你好呀。我觉得今天的天气不错。", "你好呀，我觉得今天的天气不错。"}
	if len(stable) != len(wantStable) {
		t.Fatalf("stable = %#v, want %#v", stable, wantStable)
	}
	for i := range wantStable {
		if stable[i] != wantStable[i] {
			t.Fatalf("stable = %#v, want %#v", stable, wantStable)
		}
	}
	wantSnapshots := []string{"你好呀。", "你好呀。我觉得今天的天气不错。", "你好呀，我觉得今天的天气不错。"}
	for i := range wantSnapshots {
		if stableSnapshots[i] != wantSnapshots[i] {
			t.Fatalf("stable snapshots = %#v, want %#v", stableSnapshots, wantSnapshots)
		}
	}
}

func TestTranscriptStateKeepsCumulativeDisplayEditableAcrossStableResults(t *testing.T) {
	state := newTranscriptState("req_test")
	events := make(chan Event, 32)

	for _, payload := range []string{
		`{"results":[{"text":"最近","start_time":0.792,"end_time":1.382,"is_interim":true},{"text":"最近","start_time":0.792,"end_time":1.382,"is_interim":true}]}`,
		`{"results":[{"text":"最近我","start_time":0.792,"end_time":1.782,"is_interim":true},{"text":"最近我","start_time":0.792,"end_time":1.782,"is_interim":true}]}`,
		`{"results":[{"text":"最近我在使用 Unsopee 的 CloudCall。","end_time":5.012,"is_interim":true,"extra":{"nonstream_result":true}},{"text":"最近我在使用 Unsopee 的 CloudCall。","end_time":5.012,"is_interim":true,"extra":{"nonstream_result":true}}]}`,
		`{"results":[{"text":"最近我在使用 Unsopee 的 CloudCall。确实","end_time":5.862004,"is_interim":true},{"text":"最近我在使用 Unsopee 的 CloudCall。","end_time":5.012,"is_interim":true,"extra":{"nonstream_result":true}},{"text":"确实","start_time":5.192,"end_time":5.862004,"is_interim":true}]}`,
		`{"results":[{"text":"最近我在使用 Unsopee 的 CloudCall，确实","end_time":6.812,"is_interim":true,"extra":{"nonstream_result":true}},{"text":"最近我在使用 Unsopee 的 CloudCall，确实","end_time":6.812,"is_interim":true,"extra":{"nonstream_result":true}}]}`,
		`{"results":[{"text":"最近我在使用 Unsopee 的 CloudCall，确实感觉挺好用的呢。","end_time":9.352,"is_interim":true,"extra":{"nonstream_result":true}},{"text":"最近我在使用 Unsopee 的 CloudCall，确实感觉挺好用的呢。","end_time":9.352,"is_interim":true,"extra":{"nonstream_result":true}}]}`,
	} {
		parsed := mustParseResult(t, payload)
		if parsed.Kind == ParsedStable {
			state.emitStable(events, parsed, 9.7)
		} else {
			state.emitPartial(events, parsed)
		}
	}
	close(events)

	var snapshots []string
	var updates []string
	for ev := range events {
		switch ev.Type {
		case EventTranscriptDelta:
			snapshots = append(snapshots, ev.Snapshot)
		case EventTranscriptUpdate:
			updates = append(updates, ev.Snapshot)
			snapshots = append(snapshots, ev.Snapshot)
		}
	}
	if got := snapshots[len(snapshots)-1]; got != "最近我在使用 Unsopee 的 CloudCall，确实感觉挺好用的呢。" {
		t.Fatalf("display snapshot = %q", got)
	}
	foundCorrection := false
	for _, update := range updates {
		if update == "最近我在使用 Unsopee 的 CloudCall，确实" {
			foundCorrection = true
		}
	}
	if !foundCorrection {
		t.Fatalf("updates = %#v, want punctuation correction update", updates)
	}
	if state.text() != "最近我在使用 Unsopee 的 CloudCall，确实感觉挺好用的呢。" {
		t.Fatalf("state text = %q", state.text())
	}
}

func TestTranscriptStateEmitsUpdateForNonAppendRevision(t *testing.T) {
	state := newTranscriptState("req_test")
	events := make(chan Event, 8)

	state.emitPartial(events, mustParseResult(t, `{"results":[{"text":"天气不","is_interim":true}]}`))
	state.emitPartial(events, mustParseResult(t, `{"results":[{"text":"天气很好","is_interim":true}]}`))
	state.emitStable(events, mustParseResult(t, `{"results":[{"text":"天气很好。","is_interim":false,"is_vad_finished":true,"extra":{"nonstream_result":true}}]}`), 3)
	close(events)

	var got []Event
	for ev := range events {
		got = append(got, ev)
	}
	if len(got) != 4 {
		t.Fatalf("events = %#v", got)
	}
	if got[0].Type != EventTranscriptDelta || got[0].Delta != "天气不" {
		t.Fatalf("first event = %#v, want append delta", got[0])
	}
	if got[1].Type != EventTranscriptUpdate || got[1].Snapshot != "天气很好" {
		t.Fatalf("revision event = %#v, want snapshot update", got[1])
	}
	if got[2].Type != EventTranscriptDelta || got[2].Delta != "。" {
		t.Fatalf("punctuation event = %#v, want append delta", got[2])
	}
	if got[3].Type != EventSegmentStable || got[3].Text != "天气很好。" {
		t.Fatalf("stable event = %#v", got[3])
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
	var done string
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
		if ev.Type == EventTranscriptDone {
			done = ev.Text
		}
	}
	if done != "刷新后成功" {
		t.Fatalf("bad done text: %q", done)
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
	var done string
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
		if ev.Type == EventTranscriptDone {
			done = ev.Text
		}
	}
	if done != "重新注册后成功" {
		t.Fatalf("bad done text: %q", done)
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
