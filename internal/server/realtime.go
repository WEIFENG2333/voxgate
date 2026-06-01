package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
	"github.com/WEIFENG2333/voxgate/internal/transcription"
)

var realtimeUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

const realtimeMaxBufferedAudio = audio.SampleRate * audio.Channels * 2 * 30

var realtimeMaxItemDuration = 5 * time.Minute

// Zero means append never waits in the WebSocket control loop; a full source
// rolls to a new upstream item instead of delaying later control events.
var realtimeAppendTimeout time.Duration

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
	created time.Time
}

type realtimeReadResult struct {
	messageType int
	data        []byte
	err         error
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
	readCh := make(chan realtimeReadResult, 1)
	itemDoneCh := make(chan string, 8)
	go readRealtimeMessages(rw.conn, readCh)
	_ = writeRealtimeJSON(rw, map[string]any{
		"type":     "session.created",
		"event_id": newRealtimeEventID(),
		"session":  realtimeSessionObject(sessionID),
	})
	for {
		select {
		case <-ctx.Done():
			return
		case itemID := <-itemDoneCh:
			if current != nil && current.id == itemID {
				current = nil
			}
			continue
		case read := <-readCh:
			if read.err != nil {
				if current != nil {
					current.source.CloseWrite()
				}
				return
			}
			current = s.handleRealtimeClientMessage(ctx, rw, read, sessionID, current, &itemIndex, itemDoneCh)
		}
	}
}

func readRealtimeMessages(conn *websocket.Conn, out chan<- realtimeReadResult) {
	for {
		mt, data, err := conn.ReadMessage()
		out <- realtimeReadResult{messageType: mt, data: data, err: err}
		if err != nil {
			return
		}
	}
}

func (s *Server) handleRealtimeClientMessage(ctx context.Context, rw *realtimeWriter, read realtimeReadResult, sessionID string, current *realtimeItem, itemIndex *int, itemDoneCh chan<- string) *realtimeItem {
	if read.messageType != websocket.TextMessage {
		_ = writeRealtimeError(rw, "", "invalid_request_error", "only JSON text events are supported", "unsupported_message_type")
		return current
	}
	var ev realtimeClientEvent
	if err := json.Unmarshal(read.data, &ev); err != nil {
		_ = writeRealtimeError(rw, "", "invalid_request_error", "invalid JSON event", "invalid_json")
		return current
	}
	switch ev.Type {
	case "session.update":
		_ = writeRealtimeJSON(rw, map[string]any{
			"type":     "session.updated",
			"event_id": newRealtimeEventID(),
			"session":  realtimeSessionObject(sessionID),
		})
	case "input_audio_buffer.append":
		pcm, ok := decodeRealtimeAudio(rw, ev)
		if !ok {
			return current
		}
		if current != nil && current.started && time.Since(current.created) >= realtimeMaxItemDuration {
			current.source.CloseWrite()
			current = nil
		}
		current = s.ensureRealtimeItem(ctx, rw, current, itemIndex, itemDoneCh)
		current.started = true
		if err := writeRealtimePCM(ctx, current.source, pcm); err != nil {
			if !isRealtimeAppendRecoverable(err) {
				_ = writeRealtimeError(rw, ev.EventID, "invalid_request_error", err.Error(), "audio_buffer_closed")
				return current
			}
			current.source.CloseWrite()
			current = s.ensureRealtimeItem(ctx, rw, nil, itemIndex, itemDoneCh)
			current.started = true
			if retryErr := writeRealtimePCM(ctx, current.source, pcm); retryErr != nil {
				_ = writeRealtimeError(rw, ev.EventID, "invalid_request_error", retryErr.Error(), "audio_buffer_closed")
			}
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
			return current
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
	return current
}

func decodeRealtimeAudio(rw *realtimeWriter, ev realtimeClientEvent) ([]byte, bool) {
	if ev.Audio == "" {
		_ = writeRealtimeError(rw, ev.EventID, "invalid_request_error", "audio is required", "missing_audio")
		return nil, false
	}
	pcm, err := base64.StdEncoding.DecodeString(ev.Audio)
	if err != nil {
		_ = writeRealtimeError(rw, ev.EventID, "invalid_request_error", "audio must be base64 encoded PCM16", "invalid_audio")
		return nil, false
	}
	return pcm, true
}

func writeRealtimePCM(ctx context.Context, src *audio.LiveSource, pcm []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, realtimeAppendTimeout)
	defer cancel()
	return src.WritePCMContext(writeCtx, pcm)
}

func isRealtimeAppendRecoverable(err error) bool {
	return errors.Is(err, audio.ErrLiveSourceClosed) || errors.Is(err, context.DeadlineExceeded)
}

func (s *Server) ensureRealtimeItem(ctx context.Context, rw *realtimeWriter, current *realtimeItem, itemIndex *int, itemDoneCh chan<- string) *realtimeItem {
	if current != nil {
		return current
	}
	itemID := fmt.Sprintf("item_%06d", *itemIndex)
	(*itemIndex)++
	item := &realtimeItem{id: itemID, source: audio.NewLiveSourceWithMaxBuffer(realtimeMaxBufferedAudio), created: time.Now()}
	go s.transcribeRealtimeLive(ctx, rw, item.id, item.source, itemDoneCh)
	return item
}

func (s *Server) transcribeRealtimeLive(ctx context.Context, rw *realtimeWriter, itemID string, src *audio.LiveSource, itemDoneCh chan<- string) {
	defer func() {
		select {
		case itemDoneCh <- itemID:
		default:
		}
	}()
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
