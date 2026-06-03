package transcription

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
	"github.com/WEIFENG2333/voxgate/internal/config"
	"github.com/WEIFENG2333/voxgate/internal/transcriber"
)

type Config struct {
	CredentialPath    string
	UserAgent         string
	WebSocketURL      string
	AudioFormat       string
	EnablePunctuation bool
	EnableThreePass   bool
	EnableTwoPass     bool
	Hotwords          []string
	HotwordReporter   func(context.Context, []string) error
	RequestTimeout    time.Duration
	ChunkDuration     time.Duration
	TraceWriter       io.Writer
}

type OptionInput struct {
	Language           string
	Prompt             string
	DisablePunctuation bool
	DisableThreePass   bool
	RequestTimeout     time.Duration
	Realtime           bool
}

type Service struct {
	Config Config
}

func New(cfg Config) Service {
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = config.DefaultServerRequestTimeout
	}
	if !cfg.EnablePunctuation && !cfg.EnableThreePass && !cfg.EnableTwoPass {
		cfg.EnablePunctuation = true
		cfg.EnableThreePass = true
		cfg.EnableTwoPass = true
	}
	return Service{Config: cfg}
}

func FromAppConfig(cfg config.Config) Service {
	return New(Config{
		CredentialPath:    cfg.CredentialPath,
		UserAgent:         cfg.ASR.UserAgent,
		WebSocketURL:      cfg.ASR.WebSocketURL,
		AudioFormat:       cfg.ASR.AudioFormat,
		EnablePunctuation: cfg.ASR.EnablePunctuation,
		EnableThreePass:   cfg.ASR.EnableThreePass,
		EnableTwoPass:     cfg.ASR.EnableTwoPass,
		Hotwords:          cfg.ASR.Hotwords,
		RequestTimeout:    config.ServerRequestTimeout(cfg),
	})
}

func (s Service) Options(in OptionInput) asr.Options {
	opts := asr.DefaultOptions()
	opts.Language = in.Language
	if opts.Language == "" {
		opts.Language = "zh"
	}
	opts.Prompt = in.Prompt
	opts.EnablePunctuation = s.Config.EnablePunctuation && !in.DisablePunctuation
	opts.EnableThreePass = s.Config.EnableThreePass && !in.DisableThreePass
	opts.EnableTwoPass = s.Config.EnableTwoPass
	opts.RequestTimeout = s.Config.RequestTimeout
	if in.RequestTimeout > 0 {
		opts.RequestTimeout = in.RequestTimeout
	}
	opts.Realtime = in.Realtime
	return opts
}

func (s Service) ReportHotwords(ctx context.Context) error {
	words := normalizeHotwords(s.Config.Hotwords)
	if len(words) == 0 {
		return nil
	}
	cachePath := hotwordCachePath(s.Config.CredentialPath)
	if creds, err := asr.LoadCredentials(s.Config.CredentialPath); err == nil && creds.DeviceID != "" {
		cache := loadHotwordCache(cachePath, creds.DeviceID)
		if len(cache.missing(words)) == 0 {
			return nil
		}
	}
	manager := asr.CredentialManager{
		Path:      s.Config.CredentialPath,
		UserAgent: s.Config.UserAgent,
	}
	creds, err := manager.Ensure(ctx, false)
	if err != nil {
		return err
	}
	cache := loadHotwordCache(cachePath, creds.DeviceID)
	missing := cache.missing(words)
	if len(missing) == 0 {
		return nil
	}
	if s.Config.HotwordReporter != nil {
		err = s.Config.HotwordReporter(ctx, missing)
	} else {
		err = asr.NewContextClient(creds, s.Config.UserAgent, nil).ReportUserWords(ctx, missing)
	}
	if err != nil {
		return err
	}
	cache.add(missing)
	return saveHotwordCache(cachePath, cache)
}

func (s Service) ReportHotwordsAsync(ctx context.Context) func() {
	if len(s.Config.Hotwords) == 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := s.ReportHotwords(reportCtx); err != nil {
			log.Printf("voxgate: hotwords report failed: %v", err)
		}
	}()
	return func() {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
}

func normalizeHotwords(words []string) []string {
	out := make([]string, 0, len(words))
	seen := map[string]struct{}{}
	for _, word := range words {
		word = strings.TrimSpace(word)
		if word == "" {
			continue
		}
		if _, ok := seen[word]; ok {
			continue
		}
		seen[word] = struct{}{}
		out = append(out, word)
	}
	return out
}

type hotwordCache struct {
	DeviceID    string   `json:"device_id"`
	Words       []string `json:"words"`
	UpdatedAtMS int64    `json:"updated_at_ms"`
}

func hotwordCachePath(credentialPath string) string {
	if credentialPath == "" {
		credentialPath = asr.DefaultCredentialPath()
	}
	path := config.ExpandPath(credentialPath)
	ext := filepath.Ext(path)
	if ext == "" {
		return path + ".hotwords.json"
	}
	return strings.TrimSuffix(path, ext) + ".hotwords.json"
}

func loadHotwordCache(path, deviceID string) hotwordCache {
	data, err := os.ReadFile(path)
	if err != nil {
		return hotwordCache{DeviceID: deviceID}
	}
	var cache hotwordCache
	if err := json.Unmarshal(data, &cache); err != nil || cache.DeviceID != deviceID {
		return hotwordCache{DeviceID: deviceID}
	}
	cache.Words = normalizeHotwords(cache.Words)
	return cache
}

func saveHotwordCache(path string, cache hotwordCache) error {
	cache.Words = normalizeHotwords(cache.Words)
	cache.UpdatedAtMS = time.Now().UnixMilli()
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hotwords-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (c hotwordCache) missing(words []string) []string {
	seen := map[string]struct{}{}
	for _, word := range c.Words {
		seen[word] = struct{}{}
	}
	var missing []string
	for _, word := range words {
		if _, ok := seen[word]; ok {
			continue
		}
		seen[word] = struct{}{}
		missing = append(missing, word)
	}
	return missing
}

func (c *hotwordCache) add(words []string) {
	c.Words = append(c.Words, words...)
	c.Words = normalizeHotwords(c.Words)
}

func (s Service) Runner() transcriber.Runner {
	return transcriber.Runner{Config: transcriber.Config{
		CredentialPath: s.Config.CredentialPath,
		UserAgent:      s.Config.UserAgent,
		WebSocketURL:   s.Config.WebSocketURL,
		AudioFormat:    s.Config.AudioFormat,
		ChunkDuration:  s.Config.ChunkDuration,
		TraceWriter:    s.Config.TraceWriter,
	}}
}

func (s Service) Open(ctx context.Context, path, inputFormat string, sampleRate int) (*audio.Source, error) {
	return audio.Open(ctx, path, inputFormat, sampleRate)
}

func (s Service) Transcribe(ctx context.Context, src *audio.Source, opts asr.Options, allowChunking bool) (asr.Result, error) {
	return s.Runner().Transcribe(ctx, src, opts, allowChunking)
}

func (s Service) Stream(ctx context.Context, src *audio.Source, opts asr.Options, allowChunking bool) (<-chan asr.Event, error) {
	return s.Runner().StreamWithChunking(ctx, src, opts, allowChunking)
}

func (s Service) StreamFrames(ctx context.Context, src asr.PCMFrameSource, opts asr.Options) (<-chan asr.Event, error) {
	return s.Runner().StreamFrames(ctx, src, opts)
}
