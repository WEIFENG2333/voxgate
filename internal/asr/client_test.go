package asr

import (
	"context"
	"encoding/json"
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

func TestClientMockWebSocketThreePassWithReset(t *testing.T) {
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
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "今天天气真好我们出去玩", "is_interim": true}}})
		_, _, _ = conn.ReadMessage()
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "明天继续", "is_interim": true}}})
		_, _, _ = conn.ReadMessage()
		sendResult(t, conn, map[string]any{"results": []map[string]any{{"text": "明天继续", "is_interim": false, "is_vad_finished": true, "extra": map[string]any{"nonstream_result": true}}}})
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
	var done string
	for ev := range events {
		if ev.Type == EventError {
			t.Fatalf("event error: %+v", ev.Error)
		}
		if ev.Type == EventTranscriptDone {
			done = ev.Text
		}
	}
	if done != "今天天气真好我们出去玩明天继续" {
		t.Fatalf("bad done text: %q", done)
	}
	_ = os.Remove(credPath)
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
