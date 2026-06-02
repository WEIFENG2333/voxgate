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

func TestClientStreamsMultipleFinalsInOneSession(t *testing.T) {
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
		// 持续 drain 客户端音频/结束帧；结果推送与帧数解耦（贴近真实异步流式）。
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "第一段", "is_interim": true, "index": 0}}})
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "第一段。", "is_interim": false, "is_vad_finished": true, "index": 0, "extra": map[string]any{"nonstream_result": true}}}})
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "第二段", "is_interim": true, "index": 1}}})
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "第二段。", "is_interim": false, "is_vad_finished": true, "index": 1, "extra": map[string]any{"nonstream_result": true}}}})
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
	var finals []string
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
		if ev.Type == EventTranscriptCompleted {
			finals = append(finals, ev.Text)
		}
	}
	want := []string{"第一段。", "第二段。"}
	if len(finals) != len(want) {
		t.Fatalf("finals = %#v, want %#v", finals, want)
	}
	for i := range want {
		if finals[i] != want[i] {
			t.Fatalf("finals = %#v, want %#v", finals, want)
		}
	}
	_ = os.Remove(credPath)
}

func TestClientReportsFinalDurationAfterAudioIsSent(t *testing.T) {
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
		if ev.Type == EventTranscriptCompleted {
			doneDuration = ev.Duration
		}
	}
	if doneDuration != 2 {
		t.Fatalf("done duration = %.0f, want 2", doneDuration)
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

func TestClientFinishesEmptyResultAfterFinishSessionTimeout(t *testing.T) {
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
	var doneSeen bool
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
		if ev.Type == EventTranscriptCompleted {
			doneSeen = true
			if ev.Text != "" {
				t.Fatalf("done text = %q, want empty", ev.Text)
			}
		}
	}
	if !doneSeen {
		t.Fatal("did not receive transcript done")
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
		if ev.Type == EventTranscriptCompleted {
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
	if taskRequests.Load() != 2 {
		t.Fatalf("task requests = %d, want 2 (last frame carries finish_audio, no silence frame)", taskRequests.Load())
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
		if ev.Type == EventTranscriptCompleted {
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
	if taskRequests.Load() != 2 {
		t.Fatalf("task requests = %d, want 2 (last frame carries finish_audio, no silence frame)", taskRequests.Load())
	}
	creds, err := LoadCredentials(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if creds.DeviceID != "new-device" || creds.Token != BuiltinASRAppKey {
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
