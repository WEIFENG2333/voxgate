package asr

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// writeProtocolLines feeds raw trace JSON lines through the readable protocol
// writer and returns the rendered output lines.
func writeProtocolLines(t *testing.T, raw ...string) []string {
	t.Helper()
	var buf bytes.Buffer
	w := NewProtocolTraceWriter(&buf)
	for _, line := range raw {
		if _, err := w.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	out := strings.TrimSpace(buf.String())
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func TestProtocolTraceDropsHeartbeatFrames(t *testing.T) {
	lines := writeProtocolLines(t,
		`{"time":"t1","direction":"recv","response":{"result_json":"{\"extra\":{\"client_ip\":\"1.2.3.4\",\"heartbeat_num\":0},\"results\":null}","status_code":20000000}}`,
		`{"time":"t2","direction":"recv","response":{"result_json":"{\"extra\":{\"heartbeat_num\":3},\"results\":null}","status_code":20000000}}`,
	)
	if len(lines) != 0 {
		t.Fatalf("heartbeat frames not filtered: %#v", lines)
	}
}

func TestProtocolTraceKeepsVADStartAndResults(t *testing.T) {
	lines := writeProtocolLines(t,
		`{"time":"t1","direction":"recv","response":{"result_json":"{\"extra\":{\"vad_start\":true},\"results\":[{\"is_interim\":true,\"text\":\"\"}]}"}}`,
		`{"time":"t2","direction":"recv","response":{"result_json":"{\"results\":[{\"text\":\"你好\",\"words\":[{\"word\":\"你\"}]}]}"}}`,
	)
	if len(lines) != 2 {
		t.Fatalf("expected 2 kept lines, got %#v", lines)
	}
	// words are stripped from the readable view.
	if strings.Contains(lines[1], "words") || strings.Contains(lines[1], "你") == false {
		t.Fatalf("result line not rendered as expected: %s", lines[1])
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if second["result_json"] == nil {
		t.Fatalf("result_json missing: %s", lines[1])
	}
}

func TestProtocolTraceDropsAudioRequestFrames(t *testing.T) {
	lines := writeProtocolLines(t,
		`{"time":"t1","direction":"send","request":{"method_name":"TaskRequest","audio_base64":"AAAA"}}`,
		`{"time":"t2","direction":"send","request":{"method_name":"StartTask","service_name":"ASR"}}`,
	)
	if len(lines) != 1 || !strings.Contains(lines[0], "StartTask") {
		t.Fatalf("expected only StartTask kept, got %#v", lines)
	}
}
