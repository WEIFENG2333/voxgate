package asr

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// buildContextInfo assembles the base64-encoded session context for
// StartSession: a chat history of prior inputs plus ime_info. A one-shot
// transcription has no history, so a user-supplied prompt is injected as a
// single user_input entry to bias recognition toward its vocabulary.
func buildContextInfo(appName, prompt string) string {
	chat := []map[string]any{}
	if prompt = strings.TrimSpace(prompt); prompt != "" {
		data, _ := json.Marshal(map[string]any{
			"cursor_position": len([]rune(prompt)),
			"text":            prompt,
		})
		chat = append(chat, map[string]any{
			"type":         "user_input",
			"data":         string(data),
			"time":         strconv.FormatInt(time.Now().UnixMilli(), 10),
			"app_apk_name": appName,
		})
	}
	info, _ := json.Marshal(map[string]any{
		"chat":     chat,
		"ime_info": map[string]any{"app_apk_name": appName, "input_type": ""},
	})
	return base64.StdEncoding.EncodeToString(info)
}
