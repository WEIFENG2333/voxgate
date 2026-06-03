package asr

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func decodeContextInfo(t *testing.T, encoded string) map[string]any {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	return info
}

func TestBuildContextInfoEmptyPromptHasNoChat(t *testing.T) {
	info := decodeContextInfo(t, buildContextInfo("com.android.chrome", "  "))
	chat, ok := info["chat"].([]any)
	if !ok || len(chat) != 0 {
		t.Fatalf("chat = %v, want empty slice", info["chat"])
	}
	ime, ok := info["ime_info"].(map[string]any)
	if !ok || ime["app_apk_name"] != "com.android.chrome" {
		t.Fatalf("ime_info = %v", info["ime_info"])
	}
}

func TestBuildContextInfoPromptBecomesUserInputEntry(t *testing.T) {
	info := decodeContextInfo(t, buildContextInfo("com.android.chrome", "聊斋"))
	chat, ok := info["chat"].([]any)
	if !ok || len(chat) != 1 {
		t.Fatalf("chat = %v, want one entry", info["chat"])
	}
	entry := chat[0].(map[string]any)
	if entry["type"] != "user_input" || entry["app_apk_name"] != "com.android.chrome" {
		t.Fatalf("entry = %v", entry)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(entry["data"].(string)), &data); err != nil {
		t.Fatalf("data is not a JSON string: %v", err)
	}
	if data["text"] != "聊斋" {
		t.Fatalf("data.text = %v, want 聊斋", data["text"])
	}
	if got := data["cursor_position"].(float64); got != 2 {
		t.Fatalf("cursor_position = %v, want 2 (rune count)", got)
	}
}
