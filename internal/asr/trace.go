package asr

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	asrproto "github.com/WEIFENG2333/voxgate/internal/proto"
)

type synchronizedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func NewSynchronizedWriter(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	return &synchronizedWriter{w: w}
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

type frameTrace struct {
	mu sync.Mutex
	w  io.Writer
}

func newFrameTrace(w io.Writer) *frameTrace {
	if w == nil {
		return nil
	}
	return &frameTrace{w: w}
}

func (t *frameTrace) sent(req asrproto.Request, raw []byte) {
	if t == nil {
		return
	}
	t.write(map[string]any{
		"time":                   time.Now().Format(time.RFC3339Nano),
		"direction":              "send",
		"websocket_message_type": websocket.BinaryMessage,
		"raw_base64":             base64.StdEncoding.EncodeToString(raw),
		"request": map[string]any{
			"token":        req.Token,
			"service_name": req.ServiceName,
			"method_name":  req.MethodName,
			"payload":      req.Payload,
			"audio_base64": base64.StdEncoding.EncodeToString(req.AudioData),
			"request_id":   req.RequestID,
			"frame_state":  req.FrameState,
		},
	})
}

func (t *frameTrace) received(messageType int, raw []byte, resp asrproto.Response, err error) {
	if t == nil {
		return
	}
	event := map[string]any{
		"time":                   time.Now().Format(time.RFC3339Nano),
		"direction":              "recv",
		"websocket_message_type": messageType,
		"raw_base64":             base64.StdEncoding.EncodeToString(raw),
	}
	if err != nil {
		event["decode_error"] = err.Error()
	} else {
		event["response"] = map[string]any{
			"request_id":     resp.RequestID,
			"task_id":        resp.TaskID,
			"service_name":   resp.ServiceName,
			"message_type":   resp.MessageType,
			"status_code":    resp.StatusCode,
			"status_message": resp.StatusMessage,
			"result_json":    resp.ResultJSON,
			"unknown_9":      resp.Unknown9,
			"unknown_11_b64": base64.StdEncoding.EncodeToString(resp.Unknown11),
		}
	}
	t.write(event)
}

func (t *frameTrace) write(event map[string]any) {
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	b = append(b, '\n')
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.w.Write(b)
}
