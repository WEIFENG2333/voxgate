package server

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	asrproto "github.com/WEIFENG2333/voxgate/internal/proto"
)

func TestTranscriptionsJSONWithMockWebSocket(t *testing.T) {
	wsURL, closeWS := mockASRServer(t, "你好世界")
	defer closeWS()
	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := asr.SaveCredentials(credPath, asr.Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{CredentialPath: credPath, WebSocketURL: wsURL, RequestTimeout: 10 * time.Second})

	body, contentType := multipartBody(t, "test.wav", minimalWAV())
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["text"] != "你好世界" {
		t.Fatalf("bad text: %q", got["text"])
	}
}

func TestTranscriptionsIgnoresModelValue(t *testing.T) {
	wsURL, closeWS := mockASRServer(t, "模型名被忽略")
	defer closeWS()
	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := asr.SaveCredentials(credPath, asr.Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{CredentialPath: credPath, WebSocketURL: wsURL, RequestTimeout: 10 * time.Second})

	body, contentType := multipartBody(t, "test.wav", minimalWAV(), field{"model", "whisper-1"})
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "模型名被忽略") {
		t.Fatalf("bad body: %s", rec.Body.String())
	}
}

func TestTranscriptionsSSEWithMockWebSocket(t *testing.T) {
	wsURL, closeWS := mockASRServer(t, "流式文本")
	defer closeWS()
	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := asr.SaveCredentials(credPath, asr.Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{CredentialPath: credPath, WebSocketURL: wsURL, RequestTimeout: 10 * time.Second})

	body, contentType := multipartBody(t, "test.wav", minimalWAV(), field{"stream", "true"})
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "transcript.text.done") || !strings.Contains(rec.Body.String(), "流式文本") {
		t.Fatalf("bad sse: %s", rec.Body.String())
	}
}

func TestRealtimeTranscriptionWebSocketWithMockBackend(t *testing.T) {
	wsURL, closeWS := mockASRServer(t, "实时文本")
	defer closeWS()
	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := asr.SaveCredentials(credPath, asr.Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{CredentialPath: credPath, WebSocketURL: wsURL, RequestTimeout: 10 * time.Second})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/v1/realtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var msg map[string]any
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg["type"] != "session.created" {
		t.Fatalf("first event = %v", msg)
	}
	if err := conn.WriteJSON(map[string]any{"type": "session.update", "session": map[string]any{"type": "transcription"}}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg["type"] != "session.updated" {
		t.Fatalf("session update event = %v", msg)
	}
	pcm := minimalPCM()
	if err := conn.WriteJSON(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)}); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg["type"] != "input_audio_buffer.committed" {
		t.Fatalf("commit event = %v", msg)
	}
	for {
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatal(err)
		}
		if msg["type"] == "conversation.item.input_audio_transcription.completed" {
			if msg["transcript"] != "实时文本" {
				t.Fatalf("bad completed event: %v", msg)
			}
			return
		}
		if msg["type"] == "conversation.item.input_audio_transcription.failed" || msg["type"] == "error" {
			t.Fatalf("unexpected realtime error: %v", msg)
		}
	}
}

func TestRealtimeTranscriptionStreamsBeforeCommit(t *testing.T) {
	wsURL, closeWS := mockASRStreamingServer(t)
	defer closeWS()
	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := asr.SaveCredentials(credPath, asr.Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{CredentialPath: credPath, WebSocketURL: wsURL, RequestTimeout: 10 * time.Second})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/v1/realtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var msg map[string]any
	_ = conn.ReadJSON(&msg)
	pcm := minimalPCM()
	if err := conn.WriteJSON(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		err := conn.ReadJSON(&msg)
		if err != nil {
			var netErr interface{ Timeout() bool }
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			t.Fatal(err)
		}
		if msg["type"] == "conversation.item.input_audio_transcription.delta" {
			return
		}
	}
	t.Fatal("did not receive transcription delta before commit")
}

func TestRealtimeTranscriptionAutoContinuesAfterUpstreamDone(t *testing.T) {
	wsURL, closeWS := mockASRAutoCompleteServer(t, []string{"第一段", "第二段"})
	defer closeWS()
	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := asr.SaveCredentials(credPath, asr.Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{CredentialPath: credPath, WebSocketURL: wsURL, RequestTimeout: 10 * time.Second})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/v1/realtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var msg map[string]any
	_ = conn.ReadJSON(&msg)

	pcm := minimalPCM()
	if err := conn.WriteJSON(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)}); err != nil {
		t.Fatal(err)
	}
	first := readRealtimeCompleted(t, conn)
	if first["item_id"] != "item_000000" || first["transcript"] != "第一段" {
		t.Fatalf("first completed event = %v", first)
	}

	if err := conn.WriteJSON(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)}); err != nil {
		t.Fatal(err)
	}
	second := readRealtimeCompleted(t, conn)
	if second["item_id"] != "item_000000" || second["transcript"] != "第二段" {
		t.Fatalf("second completed event = %v", second)
	}
}

func TestRealtimeTranscriptionRollsLongRunningItem(t *testing.T) {
	oldMax := realtimeMaxItemDuration
	realtimeMaxItemDuration = 50 * time.Millisecond
	defer func() { realtimeMaxItemDuration = oldMax }()

	wsURL, closeWS := mockASRServer(t, "滚动完成")
	defer closeWS()
	credPath := filepath.Join(t.TempDir(), "creds.json")
	if err := asr.SaveCredentials(credPath, asr.Credentials{DeviceID: "1", CDID: "c", Token: "t", TokenUpdatedAtMS: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{CredentialPath: credPath, WebSocketURL: wsURL, RequestTimeout: 10 * time.Second})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpSrv.URL, "http")+"/v1/realtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var msg map[string]any
	_ = conn.ReadJSON(&msg)

	pcm := minimalPCM()
	if err := conn.WriteJSON(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := conn.WriteJSON(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)}); err != nil {
		t.Fatal(err)
	}
	first := readRealtimeCompleted(t, conn)
	if first["item_id"] != "item_000000" {
		t.Fatalf("first completed event = %v", first)
	}
	if err := conn.WriteJSON(map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
		t.Fatal(err)
	}
	second := readRealtimeCompleted(t, conn)
	if second["item_id"] != "item_000001" {
		t.Fatalf("second completed event = %v", second)
	}
}

func TestTranscriptionsRejectsUnsupportedFormatBeforeASR(t *testing.T) {
	srv := New(Config{RequestTimeout: 10 * time.Second})
	body, contentType := multipartBody(t, "test.wav", minimalWAV(), field{"response_format", "xml"})
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported_response_format") {
		t.Fatalf("bad error: %s", rec.Body.String())
	}
}

func TestTranscriptionsRejectsSubtitleStreamFormat(t *testing.T) {
	srv := New(Config{RequestTimeout: 10 * time.Second})
	body, contentType := multipartBody(t, "test.wav", minimalWAV(), field{"stream", "true"}, field{"response_format", "srt"})
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported_response_format") {
		t.Fatalf("bad error: %s", rec.Body.String())
	}
}

func TestTranscriptionsDecodeErrorIsStableOpenAIError(t *testing.T) {
	srv := New(Config{RequestTimeout: 10 * time.Second})
	body, contentType := multipartBody(t, "bad.wav", []byte("not an audio file"))
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got.Error.Code != "audio_decode_failed" || got.Error.Type != "invalid_request_error" {
		t.Fatalf("bad error shape: %+v body=%s", got.Error, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "ffmpeg") {
		t.Fatalf("response leaked decoder internals: %s", rec.Body.String())
	}
}

func TestHealthRequiresGET(t *testing.T) {
	srv := New(Config{})
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func mockASRServer(t *testing.T, finalText string) (string, func()) {
	t.Helper()
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
			req, _ := parseRequestMethod(data)
			if req == "FinishSession" {
				payload, _ := json.Marshal(map[string]any{"results": []map[string]any{{"text": finalText, "is_interim": false, "is_vad_finished": true, "extra": map[string]any{"nonstream_result": true}}}})
				_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{ResultJSON: string(payload)}))
				return
			}
		}
	}))
	return "ws" + strings.TrimPrefix(srv.URL, "http"), srv.Close
}

func mockASRStreamingServer(t *testing.T) (string, func()) {
	t.Helper()
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
			req, _ := parseRequestMethod(data)
			if req == "TaskRequest" {
				payload, _ := json.Marshal(map[string]any{"results": []map[string]any{{"text": "早期增量", "is_interim": true}}})
				_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{ResultJSON: string(payload)}))
				return
			}
		}
	}))
	return "ws" + strings.TrimPrefix(srv.URL, "http"), srv.Close
}

func mockASRAutoCompleteServer(t *testing.T, finalTexts []string) (string, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		index := 0
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "TaskStarted", StatusMessage: "OK"}))
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: "SessionStarted", StatusMessage: "OK"}))
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			req, _ := parseRequestMethod(data)
			if req == "TaskRequest" {
				if index >= len(finalTexts) {
					index = len(finalTexts) - 1
				}
				payload, _ := json.Marshal(map[string]any{"results": []map[string]any{{"text": finalTexts[index], "is_interim": false, "is_vad_finished": true, "extra": map[string]any{"nonstream_result": true}}}})
				_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{ResultJSON: string(payload)}))
				index++
			}
		}
	}))
	return "ws" + strings.TrimPrefix(srv.URL, "http"), srv.Close
}

func readRealtimeCompleted(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		var msg map[string]any
		err := conn.ReadJSON(&msg)
		if err != nil {
			var netErr interface{ Timeout() bool }
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			t.Fatal(err)
		}
		switch msg["type"] {
		case "conversation.item.input_audio_transcription.completed":
			return msg
		case "error", "conversation.item.input_audio_transcription.failed":
			t.Fatalf("unexpected realtime error: %v", msg)
		}
	}
	t.Fatal("did not receive realtime completed event")
	return nil
}

func parseRequestMethod(data []byte) (string, error) {
	// Minimal decoder for request field 5 used only by this test.
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

type field struct{ key, value string }

func multipartBody(t *testing.T, filename string, file []byte, fields ...field) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(file); err != nil {
		t.Fatal(err)
	}
	hasModel := false
	for _, f := range fields {
		if f.key == "model" {
			hasModel = true
			break
		}
	}
	if !hasModel {
		_ = w.WriteField("model", "voxgate")
	}
	for _, f := range fields {
		_ = w.WriteField(f.key, f.value)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, w.FormDataContentType()
}

func minimalWAV() []byte {
	const sampleRate = 16000
	const samples = 1600
	dataSize := samples * 2
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+dataSize))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(dataSize))
	b.Write(make([]byte, dataSize))
	return b.Bytes()
}

func minimalPCM() []byte {
	const samples = 1600
	return make([]byte, samples*2)
}
