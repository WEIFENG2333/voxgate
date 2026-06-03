//go:build ignore

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
	asrproto "github.com/WEIFENG2333/voxgate/internal/proto"
)

const expectedText = "甚至出现交易几乎停滞的情况，甚至。"

type fixtureFrame struct {
	MessageType   string          `json:"message_type"`
	StatusCode    int32           `json:"status_code"`
	StatusMessage string          `json:"status_message"`
	ResultJSON    json.RawMessage `json:"result_json"`
}

type upstreamSession struct {
	Methods       []string
	AudioFormat   string
	AudioFrames   int
	FirstAudioLen int
	FirstState    asrproto.FrameState
	LastStateSeen bool
}

type fakeUpstream struct {
	srv     *httptest.Server
	frames  []fixtureFrame
	mu      sync.Mutex
	records []upstreamSession
}

func main() {
	bin := flag.String("bin", "bin/voxgate", "voxgate binary")
	audioPath := flag.String("audio", "tests/audio/zh_5s.wav", "speech sample")
	fixturePath := flag.String("fixture", "tests/e2e/fixtures/upstream_ime_zh_5s.jsonl", "upstream response fixture")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := runHarness(ctx, *bin, *audioPath, *fixturePath); err != nil {
		log.Fatal(err)
	}
	fmt.Println("E2E harness passed")
}

func runHarness(ctx context.Context, bin, audioPath, fixturePath string) error {
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("binary %q is not available: %w", bin, err)
	}
	frames, err := loadFixture(fixturePath)
	if err != nil {
		return err
	}
	upstream := newFakeUpstream(frames)
	defer upstream.Close()

	dir, err := os.MkdirTemp("", "voxgate-e2e-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	credPath := filepath.Join(dir, "credentials.json")
	if err := asr.SaveCredentials(credPath, asr.Credentials{
		DeviceID:         "e2e-device",
		CDID:             "e2e-cdid",
		Token:            "e2e-token",
		TokenUpdatedAtMS: time.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	configPath := filepath.Join(dir, "config.yaml")
	configData := fmt.Sprintf("credential_path: %q\nasr:\n  websocket_url: %q\n  audio_format: pcm\nserver:\n  request_timeout: 30s\n", credPath, upstream.URL())
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		return err
	}

	if err := runCLIFormats(ctx, bin, configPath, audioPath, dir); err != nil {
		return err
	}
	if err := runStdinStream(ctx, bin, configPath, audioPath); err != nil {
		return err
	}
	if err := runServerCases(ctx, bin, configPath, audioPath); err != nil {
		return err
	}
	return upstream.AssertSessions()
}

func loadFixture(path string) ([]fixtureFrame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var frames []fixtureFrame
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var frame fixtureFrame
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		frames = append(frames, frame)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("%s: empty fixture", path)
	}
	return frames, nil
}

func newFakeUpstream(frames []fixtureFrame) *fakeUpstream {
	f := &fakeUpstream{frames: frames}
	upgrader := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		f.handleConn(conn)
	}))
	return f
}

func (f *fakeUpstream) URL() string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http")
}

func (f *fakeUpstream) Close() {
	f.srv.Close()
}

func (f *fakeUpstream) handleConn(conn *websocket.Conn) {
	var rec upstreamSession
	resultFrames := f.resultFrames()
	resultIndex := 0
	sendControl := func(messageType string) {
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: messageType, StatusMessage: "OK"}))
	}
	sendResult := func() {
		if resultIndex >= len(resultFrames) {
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{ResultJSON: string(resultFrames[resultIndex].ResultJSON)}))
		resultIndex++
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			f.record(rec)
			return
		}
		req, err := asrproto.UnmarshalRequest(data)
		if err != nil {
			f.record(rec)
			return
		}
		rec.Methods = append(rec.Methods, req.MethodName)
		switch req.MethodName {
		case asr.MethodStartTask:
			if req.ServiceName != asr.ServiceNameASR || req.Token == "" || req.RequestID == "" {
				f.record(rec)
				_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: asr.MessageTaskFailed, StatusCode: 45000001, StatusMessage: "bad StartTask"}))
				return
			}
			sendControl(asr.MessageTaskStarted)
		case asr.MethodStartSession:
			rec.AudioFormat = parseAudioFormat(req.Payload)
			sendControl(asr.MessageSessionStarted)
		case asr.MethodTaskRequest:
			rec.AudioFrames++
			if rec.FirstAudioLen == 0 {
				rec.FirstAudioLen = len(req.AudioData)
				rec.FirstState = req.FrameState
			}
			if req.FrameState == asrproto.FrameStateLast {
				rec.LastStateSeen = true
			}
			if (rec.AudioFrames == 1 || rec.AudioFrames == 12) && resultIndex < len(resultFrames)-1 {
				sendResult()
			}
		case asr.MethodFinishSession:
			for resultIndex < len(resultFrames) {
				sendResult()
			}
			sendControl(asr.MessageSessionFinished)
			f.record(rec)
			return
		default:
			f.record(rec)
			_ = conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalResponse(asrproto.Response{MessageType: asr.MessageSessionFailed, StatusCode: 45000001, StatusMessage: "unexpected method"}))
			return
		}
	}
}

func (f *fakeUpstream) resultFrames() []fixtureFrame {
	var out []fixtureFrame
	for _, frame := range f.frames {
		if len(frame.ResultJSON) > 0 {
			out = append(out, frame)
		}
	}
	return out
}

func (f *fakeUpstream) record(rec upstreamSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, rec)
}

func (f *fakeUpstream) AssertSessions() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.records) < 10 {
		return fmt.Errorf("upstream sessions = %d, want at least 10", len(f.records))
	}
	for i, rec := range f.records {
		if rec.AudioFormat != asr.AudioFormatRaw {
			return fmt.Errorf("session %d audio format = %q, want %q", i, rec.AudioFormat, asr.AudioFormatRaw)
		}
		if rec.FirstAudioLen != audio.BytesPerFrame {
			return fmt.Errorf("session %d first audio bytes = %d, want %d", i, rec.FirstAudioLen, audio.BytesPerFrame)
		}
		if rec.FirstState != asrproto.FrameStateFirst {
			return fmt.Errorf("session %d first frame state = %d, want %d", i, rec.FirstState, asrproto.FrameStateFirst)
		}
		if !rec.LastStateSeen {
			return fmt.Errorf("session %d did not send a final audio frame", i)
		}
		if !sameMethodPrefix(rec.Methods, []string{asr.MethodStartTask, asr.MethodStartSession, asr.MethodTaskRequest}) {
			return fmt.Errorf("session %d method sequence starts with %#v", i, rec.Methods)
		}
		if rec.Methods[len(rec.Methods)-1] != asr.MethodFinishSession {
			return fmt.Errorf("session %d last method = %q, want FinishSession", i, rec.Methods[len(rec.Methods)-1])
		}
	}
	return nil
}

func sameMethodPrefix(got, want []string) bool {
	if len(got) < len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func parseAudioFormat(payload string) string {
	var body struct {
		AudioInfo struct {
			Format string `json:"format"`
		} `json:"audio_info"`
	}
	_ = json.Unmarshal([]byte(payload), &body)
	return body.AudioInfo.Format
}

func runCLIFormats(ctx context.Context, bin, configPath, audioPath, dir string) error {
	cases := []struct {
		format string
		check  func(string) error
	}{
		{"json", assertJSONText},
		{"text", assertContainsText},
		{"verbose_json", assertVerboseJSON},
		{"srt", assertSRT},
		{"vtt", assertVTT},
		{"ndjson", assertNDJSONDone},
	}
	for _, tc := range cases {
		args := []string{"--config", configPath, "--quiet", "transcribe", audioPath, "--format", tc.format, "--request-timeout", "30s"}
		if tc.format == "json" {
			args = append(args[:2], append([]string{"--trace-asr", filepath.Join(dir, "cli-json.trace")}, args[2:]...)...)
		}
		out, err := runCmd(ctx, nil, bin, args...)
		if err != nil {
			return fmt.Errorf("CLI format %s: %w\n%s", tc.format, err, out)
		}
		if err := tc.check(out); err != nil {
			return fmt.Errorf("CLI format %s: %w\n%s", tc.format, err, out)
		}
	}
	if err := assertTrace(filepath.Join(dir, "cli-json.trace")); err != nil {
		return err
	}
	return nil
}

func runStdinStream(ctx context.Context, bin, configPath, audioPath string) error {
	pcm, err := readPCM(ctx, audioPath)
	if err != nil {
		return err
	}
	out, err := runCmd(ctx, pcm, bin, "--config", configPath, "--quiet", "transcribe", "-", "--input-format", "pcm16", "--stream", "--format", "ndjson", "--request-timeout", "30s")
	if err != nil {
		return fmt.Errorf("stdin stream: %w\n%s", err, out)
	}
	if !strings.Contains(out, `"type":"transcript.text.delta"`) || !strings.Contains(out, `"type":"transcript.segment.stable"`) || !strings.Contains(out, `"type":"transcript.text.done"`) {
		return fmt.Errorf("stdin stream missing expected events:\n%s", out)
	}
	return nil
}

func runServerCases(ctx context.Context, bin, configPath, audioPath string) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "--config", configPath, "--quiet", "serve", "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--request-timeout", "30s")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitHealth(ctx, baseURL+"/health"); err != nil {
		return fmt.Errorf("server did not start: %w\n%s", err, stderr.String())
	}
	if err := runHTTPTranscription(ctx, baseURL, audioPath); err != nil {
		return err
	}
	if err := runSSETranscription(ctx, baseURL, audioPath); err != nil {
		return err
	}
	if err := runRealtime(ctx, strings.Replace(baseURL, "http://", "ws://", 1)+"/v1/realtime", audioPath); err != nil {
		return err
	}
	return nil
}

func runHTTPTranscription(ctx context.Context, baseURL, audioPath string) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := writeMultipartFile(mw, "file", audioPath); err != nil {
		return err
	}
	_ = mw.WriteField("model", "anything")
	_ = mw.WriteField("response_format", "json")
	_ = mw.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/audio/transcriptions", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP transcription status %d: %s", resp.StatusCode, string(data))
	}
	return assertJSONText(string(data))
}

func runSSETranscription(ctx context.Context, baseURL, audioPath string) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := writeMultipartFile(mw, "file", audioPath); err != nil {
		return err
	}
	_ = mw.WriteField("stream", "true")
	_ = mw.WriteField("response_format", "json")
	_ = mw.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/audio/transcriptions", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE status %d: %s", resp.StatusCode, string(data))
	}
	out := string(data)
	if !strings.Contains(out, "event: transcript.text.delta") || !strings.Contains(out, "event: transcript.text.done") {
		return fmt.Errorf("SSE missing expected events:\n%s", out)
	}
	return nil
}

func runRealtime(ctx context.Context, url, audioPath string) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	pcm, err := readPCM(ctx, audioPath)
	if err != nil {
		return err
	}
	if err := conn.WriteJSON(map[string]any{"type": "session.update", "session": map[string]any{"type": "transcription"}}); err != nil {
		return err
	}
	chunkBytes := audio.SampleRate * 2 / 10
	for off := 0; off < len(pcm); off += chunkBytes {
		end := off + chunkBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := conn.WriteJSON(map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm[off:end])}); err != nil {
			return err
		}
	}
	if err := conn.WriteJSON(map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
		return err
	}
	readCh := make(chan map[string]any, 16)
	errCh := make(chan error, 1)
	go func() {
		for {
			var ev map[string]any
			if err := conn.ReadJSON(&ev); err != nil {
				errCh <- err
				return
			}
			readCh <- ev
		}
	}()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-readCh:
			if ev["type"] == "conversation.item.input_audio_transcription.completed" && strings.Contains(fmt.Sprint(ev["transcript"]), expectedText) {
				return nil
			}
		case err := <-errCh:
			return err
		case <-timer.C:
			return errors.New("realtime did not receive completed transcription")
		}
	}
}

func writeMultipartFile(mw *multipart.Writer, field, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	part, err := mw.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = io.Copy(part, f)
	return err
}

func readPCM(ctx context.Context, path string) ([]byte, error) {
	src, err := audio.ConvertFile(ctx, path)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	var out []byte
	for {
		frame, ok, err := src.NextFrame()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		out = append(out, frame...)
	}
}

func runCmd(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func assertJSONText(out string) error {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		return err
	}
	if body.Text != expectedText {
		return fmt.Errorf("text = %q, want %q", body.Text, expectedText)
	}
	return nil
}

func assertContainsText(out string) error {
	if !strings.Contains(out, expectedText) {
		return fmt.Errorf("missing text %q", expectedText)
	}
	return nil
}

func assertVerboseJSON(out string) error {
	var body struct {
		Text    string `json:"text"`
		Results []any  `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		return err
	}
	if body.Text != expectedText || len(body.Results) == 0 {
		return fmt.Errorf("bad verbose_json text/results: %s", out)
	}
	return nil
}

func assertSRT(out string) error {
	if !strings.Contains(out, "00:00:") || !strings.Contains(out, expectedText) {
		return fmt.Errorf("bad srt output: %s", out)
	}
	return nil
}

func assertVTT(out string) error {
	if !strings.HasPrefix(out, "WEBVTT") || !strings.Contains(out, expectedText) {
		return fmt.Errorf("bad vtt output: %s", out)
	}
	return nil
}

func assertNDJSONDone(out string) error {
	if !strings.Contains(out, `"type":"transcript.text.done"`) || !strings.Contains(out, expectedText) {
		return fmt.Errorf("bad ndjson output: %s", out)
	}
	return nil
}

func assertTrace(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var sawStartSession, sawTaskRequest bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev struct {
			Direction string `json:"direction"`
			Request   struct {
				MethodName  string `json:"method_name"`
				Payload     string `json:"payload"`
				AudioBase64 string `json:"audio_base64"`
			} `json:"request"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return err
		}
		if ev.Direction != "send" {
			continue
		}
		if ev.Request.MethodName == asr.MethodStartSession {
			sawStartSession = true
			if got := parseAudioFormat(ev.Request.Payload); got != asr.AudioFormatRaw {
				return fmt.Errorf("trace StartSession format = %q", got)
			}
		}
		if ev.Request.MethodName == asr.MethodTaskRequest && ev.Request.AudioBase64 != "" {
			sawTaskRequest = true
			data, err := base64.StdEncoding.DecodeString(ev.Request.AudioBase64)
			if err != nil {
				return err
			}
			if len(data) != audio.BytesPerFrame {
				return fmt.Errorf("trace first audio bytes = %d, want %d", len(data), audio.BytesPerFrame)
			}
			break
		}
	}
	if !sawStartSession || !sawTaskRequest {
		return fmt.Errorf("trace missing StartSession or TaskRequest")
	}
	return nil
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func waitHealth(ctx context.Context, url string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return context.DeadlineExceeded
}
