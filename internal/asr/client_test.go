package asr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	asrproto "github.com/WEIFENG2333/ime-asr/internal/proto"
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

func sendResult(t *testing.T, conn *websocket.Conn, v map[string]any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{ResultJSON: string(data)})); err != nil {
		t.Fatal(err)
	}
}
