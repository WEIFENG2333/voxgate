package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/WEIFENG2333/ime-asr/internal/asr"
	asrproto "github.com/WEIFENG2333/ime-asr/internal/proto"
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
	_ = w.WriteField("model", "ime-asr")
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
