package server

import (
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/WEIFENG2333/ime-asr/internal/asr"
	"github.com/WEIFENG2333/ime-asr/internal/audio"
	"github.com/WEIFENG2333/ime-asr/internal/output"
	"github.com/WEIFENG2333/ime-asr/internal/transcriber"
)

type Config struct {
	Host              string
	Port              int
	AuthToken         string
	MaxConcurrency    int
	RequestTimeout    time.Duration
	CredentialPath    string
	EnableRealtime    bool
	EnablePunctuation bool
	EnableThreePass   bool
	EnableTwoPass     bool
	UserAgent         string
	WebSocketURL      string
}

type Server struct {
	Config Config
	sem    chan struct{}
}

func New(cfg Config) *Server {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 4
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 60 * time.Second
	}
	return &Server{Config: cfg, sem: make(chan struct{}, cfg.MaxConcurrency)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/metrics", s.metrics)
	mux.HandleFunc("/v1/models", s.models)
	mux.HandleFunc("/v1/audio/translations", s.translations)
	mux.HandleFunc("/v1/audio/transcriptions", s.transcriptions)
	mux.HandleFunc("/v1/realtime", s.realtime)
	return s.withCORS(mux)
}

func (s *Server) Addr() string {
	return fmt.Sprintf("%s:%d", s.Config.Host, s.Config.Port)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintln(w, "ime_asr_up 1")
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	if !s.authorize(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []map[string]string{{"id": "ime-asr", "object": "model"}}})
}

func (s *Server) translations(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodPost) {
		return
	}
	if !s.authorize(w, r) {
		return
	}
	writeOpenAIError(w, http.StatusBadRequest, "unsupported_request_error", "Doubao IME ASR does not support translation to English", "unsupported")
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
	writeOpenAIError(w, http.StatusNotImplemented, "unsupported_request_error", "OpenAI realtime compatibility is not implemented yet", "unsupported")
}

func (s *Server) transcriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed", "method_not_allowed")
		return
	}
	if !s.authorize(w, r) {
		return
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_error", "too many concurrent transcription requests", "concurrency_exceeded")
		return
	}
	if err := r.ParseMultipartForm(128 << 20); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), "bad_multipart")
		return
	}
	responseFormat := formValue(r.MultipartForm, "response_format", output.JSON)
	stream := formBool(r.MultipartForm, "stream")
	if stream {
		if !output.ValidStreamFormat(responseFormat) {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("stream response_format %q is unsupported", responseFormat), "unsupported_response_format")
			return
		}
	} else if !output.ValidResultFormat(responseFormat) {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("response_format %q is unsupported", responseFormat), "unsupported_response_format")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "missing multipart file field", "missing_file")
		return
	}
	defer file.Close()
	tmp, err := writeTemp(file, header)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error(), "temp_file")
		return
	}
	defer os.Remove(tmp)
	ctx, cancel := context.WithTimeout(r.Context(), s.Config.RequestTimeout)
	defer cancel()
	src, err := audio.Open(ctx, tmp, "", audio.SampleRate)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), "audio_decode_failed")
		return
	}
	opts := asr.DefaultOptions()
	opts.EnablePunctuation = s.Config.EnablePunctuation
	opts.EnableThreePass = s.Config.EnableThreePass
	opts.EnableTwoPass = s.Config.EnableTwoPass
	opts.RequestTimeout = s.Config.RequestTimeout
	runner := transcriber.Runner{Config: transcriber.Config{CredentialPath: s.Config.CredentialPath, UserAgent: s.Config.UserAgent, WebSocketURL: s.Config.WebSocketURL}}
	if stream {
		events, err := runner.Stream(ctx, src, opts)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error(), "transcribe_failed")
			return
		}
		s.streamSSE(w, events)
		return
	}
	result, err := runner.Transcribe(ctx, src, opts, true)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error(), "asr_error")
		return
	}
	w.Header().Set("Content-Type", contentType(responseFormat))
	if err := output.WriteResult(w, responseFormat, result); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error(), "encode_failed")
	}
}

func (s *Server) streamSSE(w http.ResponseWriter, events <-chan asr.Event) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for ev := range events {
		if ev.Type == asr.EventTranscriptDelta {
			writeSSE(w, "transcript.text.delta", map[string]string{"delta": ev.Text})
		}
		if ev.Type == asr.EventTranscriptDone {
			writeSSE(w, "transcript.text.done", map[string]string{"text": ev.Text})
		}
		if ev.Type == asr.EventError && ev.Error != nil {
			writeSSE(w, "error", ev.Error)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.Config.AuthToken == "" {
		return true
	}
	if r.Header.Get("Authorization") == "Bearer "+s.Config.AuthToken {
		return true
	}
	writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", "invalid bearer token", "invalid_api_key")
	return false
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || strings.Contains(origin, "localhost") || strings.Contains(origin, "127.0.0.1") {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			if origin == "" {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeTemp(file multipart.File, header *multipart.FileHeader) (string, error) {
	tmp, err := os.CreateTemp("", "ime-asr-*-"+header.Filename)
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := tmp.ReadFrom(file); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOpenAIError(w http.ResponseWriter, status int, typ, message, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": typ, "code": code}})
}

func writeSSE(w http.ResponseWriter, event string, data any) {
	b, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

func allowMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed", "method_not_allowed")
	return false
}

func formValue(f *multipart.Form, key, fallback string) string {
	if f != nil && len(f.Value[key]) > 0 && f.Value[key][0] != "" {
		return f.Value[key][0]
	}
	return fallback
}

func formBool(f *multipart.Form, key string) bool {
	v := strings.ToLower(formValue(f, key, "false"))
	b, _ := strconv.ParseBool(v)
	return b
}

func contentType(format string) string {
	if format == output.Text || format == output.SRT || format == output.VTT {
		return "text/plain; charset=utf-8"
	}
	return "application/json"
}
