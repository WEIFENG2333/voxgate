package transcription

import (
	"context"
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
	EnablePunctuation bool
	EnableThreePass   bool
	EnableTwoPass     bool
	RequestTimeout    time.Duration
	ChunkDuration     time.Duration
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
		EnablePunctuation: cfg.ASR.EnablePunctuation,
		EnableThreePass:   cfg.ASR.EnableThreePass,
		EnableTwoPass:     cfg.ASR.EnableTwoPass,
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

func (s Service) Runner() transcriber.Runner {
	return transcriber.Runner{Config: transcriber.Config{
		CredentialPath: s.Config.CredentialPath,
		UserAgent:      s.Config.UserAgent,
		WebSocketURL:   s.Config.WebSocketURL,
		ChunkDuration:  s.Config.ChunkDuration,
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
