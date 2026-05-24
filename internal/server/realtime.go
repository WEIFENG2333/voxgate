package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
	"github.com/WEIFENG2333/voxgate/internal/transcription"
)

var realtimeUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type realtimeClientEvent struct {
	Type    string          `json:"type"`
	EventID string          `json:"event_id,omitempty"`
	Audio   string          `json:"audio,omitempty"`
	Session json.RawMessage `json:"session,omitempty"`
}

type realtimeItem struct {
	id      string
	source  *audio.LiveSource
	started bool
}

type realtimeWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *realtimeWriter) WriteJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteJSON(v)
}

func (s *Server) realtime(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	if !s.authorize(w, r) {
		return
	}
	if !s.Config.EnableRealtime {
		writeOpenAIError(w, http.StatusNotFound, "not_found_error", "realtime endpoint is disabled", "not_found")
		return
	}
	conn, err := realtimeUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	s.handleRealtimeConn(r.Context(), &realtimeWriter{conn: conn})
}

func (s *Server) handleRealtimeConn(ctx context.Context, rw *realtimeWriter) {
	sessionID := "sess_" + uuid.NewString()
	itemIndex := 0
	var current *realtimeItem
	_ = writeRealtimeJSON(rw, map[string]any{
		"type":     "session.created",
		"event_id": newRealtimeEventID(),
		"session":  realtimeSessionObject(sessionID),
	})
	for {
		mt, data, err := rw.conn.ReadMessage()
		if err != nil {
			return
		}
		if mt != websocket.TextMessage {
			_ = writeRealtimeError(rw, "", "invalid_request_error", "only JSON text events are supported", "unsupported_message_type")
			continue
		}
		var ev realtimeClientEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			_ = writeRealtimeError(rw, "", "invalid_request_error", "invalid JSON event", "invalid_json")
			continue
		}
		switch ev.Type {
		case "session.update":
			_ = writeRealtimeJSON(rw, map[string]any{
				"type":     "session.updated",
				"event_id": newRealtimeEventID(),
				"session":  realtimeSessionObject(sessionID),
			})
		case "input_audio_buffer.append":
			if ev.Audio == "" {
				_ = writeRealtimeError(rw, ev.EventID, "invalid_request_error", "audio is required", "missing_audio")
				continue
			}
			pcm, err := base64.StdEncoding.DecodeString(ev.Audio)
			if err != nil {
				_ = writeRealtimeError(rw, ev.EventID, "invalid_request_error", "audio must be base64 encoded PCM16", "invalid_audio")
				continue
			}
			if current == nil {
				itemID := fmt.Sprintf("item_%06d", itemIndex)
				itemIndex++
				current = &realtimeItem{id: itemID, source: audio.NewLiveSource()}
				go s.transcribeRealtimeLive(ctx, rw, current.id, current.source)
			}
			current.started = true
			if err := current.source.WritePCM(pcm); err != nil {
				_ = writeRealtimeError(rw, ev.EventID, "invalid_request_error", err.Error(), "audio_buffer_closed")
			}
		case "input_audio_buffer.clear":
			if current != nil {
				current.source.CloseWrite()
				current = nil
			}
			_ = writeRealtimeJSON(rw, map[string]any{"type": "input_audio_buffer.cleared", "event_id": newRealtimeEventID()})
		case "input_audio_buffer.commit":
			if current == nil || !current.started {
				_ = writeRealtimeError(rw, ev.EventID, "invalid_request_error", "input audio buffer is empty", "empty_audio_buffer")
				continue
			}
			itemID := current.id
			current.source.CloseWrite()
			current = nil
			_ = writeRealtimeJSON(rw, map[string]any{
				"type":             "input_audio_buffer.committed",
				"event_id":         newRealtimeEventID(),
				"item_id":          itemID,
				"previous_item_id": nil,
			})
		default:
			_ = writeRealtimeError(rw, ev.EventID, "invalid_request_error", fmt.Sprintf("unsupported realtime event %q", ev.Type), "unsupported_event")
		}
	}
}

func (s *Server) transcribeRealtimeLive(ctx context.Context, rw *realtimeWriter, itemID string, src *audio.LiveSource) {
	reqCtx, cancel := context.WithTimeout(ctx, s.Config.RequestTimeout)
	defer cancel()
	svc := s.transcriptionService()
	opts := svc.Options(transcription.OptionInput{RequestTimeout: s.Config.RequestTimeout, Realtime: true})
	events, err := svc.StreamFrames(reqCtx, src, opts)
	if err != nil {
		_ = writeRealtimeTranscriptionFailed(rw, itemID, err)
		return
	}
	for ev := range events {
		if ev.Type == asr.EventTranscriptDelta {
			_ = writeRealtimeJSON(rw, map[string]any{
				"type":          "conversation.item.input_audio_transcription.delta",
				"event_id":      newRealtimeEventID(),
				"item_id":       itemID,
				"content_index": 0,
				"delta":         ev.Text,
			})
		}
		if ev.Type == asr.EventTranscriptDone {
			_ = writeRealtimeJSON(rw, map[string]any{
				"type":          "conversation.item.input_audio_transcription.completed",
				"event_id":      newRealtimeEventID(),
				"item_id":       itemID,
				"content_index": 0,
				"transcript":    ev.Text,
			})
		}
		if ev.Type == asr.EventError && ev.Error != nil {
			_ = writeRealtimeTranscriptionFailed(rw, itemID, fmt.Errorf("%s", ev.Error.Message))
		}
	}
}

func realtimeSessionObject(id string) map[string]any {
	return map[string]any{
		"object": "realtime.session",
		"type":   "transcription",
		"id":     id,
		"audio": map[string]any{
			"input": map[string]any{
				"format": map[string]any{"type": "audio/pcm", "rate": audio.SampleRate},
				"transcription": map[string]any{
					"model": "voxgate",
				},
				"turn_detection": nil,
			},
		},
	}
}

func writeRealtimeTranscriptionFailed(rw *realtimeWriter, itemID string, err error) error {
	return writeRealtimeJSON(rw, map[string]any{
		"type":     "conversation.item.input_audio_transcription.failed",
		"event_id": newRealtimeEventID(),
		"item_id":  itemID,
		"error": map[string]any{
			"type":    "server_error",
			"code":    "asr_error",
			"message": err.Error(),
		},
	})
}

func writeRealtimeError(rw *realtimeWriter, eventID, typ, message, code string) error {
	return writeRealtimeJSON(rw, map[string]any{
		"type":     "error",
		"event_id": newRealtimeEventID(),
		"error": map[string]any{
			"type":     typ,
			"code":     code,
			"message":  message,
			"event_id": eventID,
		},
	})
}

func writeRealtimeJSON(rw *realtimeWriter, v any) error {
	return rw.WriteJSON(v)
}

func newRealtimeEventID() string {
	return "event_" + uuid.NewString()
}
