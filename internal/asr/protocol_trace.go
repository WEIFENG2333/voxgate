package asr

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
)

type protocolTraceWriter struct {
	mu  sync.Mutex
	buf []byte
	w   io.Writer
}

func NewProtocolTraceWriter(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	return &protocolTraceWriter{w: w}
}

func (w *protocolTraceWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(w.buf[:i])
		w.buf = w.buf[i+1:]
		if len(line) == 0 {
			continue
		}
		if err := w.writeLine(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *protocolTraceWriter) writeLine(line []byte) error {
	var ev map[string]any
	if err := json.Unmarshal(line, &ev); err != nil {
		return err
	}
	out, ok := protocolTraceEvent(ev)
	if !ok {
		return nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.w.Write(b)
	return err
}

func protocolTraceEvent(ev map[string]any) (map[string]any, bool) {
	out := map[string]any{
		"time":      ev["time"],
		"direction": ev["direction"],
	}
	switch ev["direction"] {
	case "send":
		req, _ := ev["request"].(map[string]any)
		method, _ := req["method_name"].(string)
		if method == "" {
			return nil, false
		}
		out["method_name"] = method
		copyString(out, req, "service_name")
		copyString(out, req, "request_id")
		if method == MethodTaskRequest {
			return nil, false
		}
		if payload, _ := req["payload"].(string); strings.TrimSpace(payload) != "" {
			out["payload"] = parseJSONObject(payload)
		}
		return out, true
	case "recv":
		resp, _ := ev["response"].(map[string]any)
		if decodeErr, _ := ev["decode_error"].(string); decodeErr != "" {
			out["decode_error"] = decodeErr
			return out, true
		}
		copyString(out, resp, "message_type")
		copyString(out, resp, "request_id")
		copyString(out, resp, "task_id")
		copyNumber(out, resp, "status_code")
		copyString(out, resp, "status_message")
		if resultJSON, _ := resp["result_json"].(string); strings.TrimSpace(resultJSON) != "" {
			out["result_json"] = parseProtocolResult(resultJSON)
		}
		return out, true
	default:
		return nil, false
	}
}

func parseJSONObject(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}

func parseProtocolResult(s string) any {
	parsed := parseJSONObject(s)
	value, ok := parsed.(map[string]any)
	if !ok {
		return parsed
	}
	return stripProtocolWords(value)
}

func stripProtocolWords(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			if k == "words" {
				continue
			}
			out[k] = stripProtocolWords(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = stripProtocolWords(v)
		}
		return out
	default:
		return v
	}
}

func copyString(dst, src map[string]any, key string) {
	if v, _ := src[key].(string); v != "" {
		dst[key] = v
	}
}

func copyNumber(dst, src map[string]any, key string) {
	if v, ok := src[key]; ok {
		dst[key] = v
	}
}
