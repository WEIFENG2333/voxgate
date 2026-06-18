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
		var result any
		if resultJSON, _ := resp["result_json"].(string); strings.TrimSpace(resultJSON) != "" {
			// result_json is passed through verbatim (parsed for readability); only
			// content-free keepalive frames are dropped, since they add nothing but
			// noise (and leak client_ip) to a readable protocol view.
			result = parseJSONObject(resultJSON)
			if isProtocolHeartbeat(result) {
				return nil, false
			}
		}
		copyString(out, resp, "message_type")
		copyString(out, resp, "request_id")
		copyString(out, resp, "task_id")
		copyNumber(out, resp, "status_code")
		copyString(out, resp, "status_message")
		if result != nil {
			out["result_json"] = result
		}
		return out, true
	default:
		return nil, false
	}
}

// isProtocolHeartbeat reports whether a recv result frame carries no recognition
// content — the upstream's periodic keepalive frames (results null/empty, tagged
// with extra.heartbeat_num). VAD-start frames (extra.vad_start) are kept.
func isProtocolHeartbeat(parsed any) bool {
	m, ok := parsed.(map[string]any)
	if !ok {
		return false
	}
	if results, ok := m["results"].([]any); ok && len(results) > 0 {
		return false
	}
	if extra, ok := m["extra"].(map[string]any); ok {
		if vadStart, _ := extra["vad_start"].(bool); vadStart {
			return false
		}
	}
	return true
}

func parseJSONObject(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
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
