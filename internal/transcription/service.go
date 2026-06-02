package transcription

import (
	"context"
	"io"
	"log"
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
	AudioFormat       string   // speech_opus | raw
	DeviceName        string   // 设备画像名（xiaomi14 | samsung | pixel）
	Hotwords          []string // 个人热词，转录时异步上报
	EnablePunctuation bool
	EnableThreePass   bool
	EnableTwoPass     bool
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
		WebSocketURL:      asr.EndpointURL(cfg.ASR.Endpoint),
		AudioFormat:       cfg.ASR.AudioFormat,
		DeviceName:        cfg.ASR.Device,
		Hotwords:          cfg.ASR.Hotwords,
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
	return opts
}

// ReportHotwordsAsync 在后台上报配置的热词，增强后续识别对专有词的命中。
// 不阻塞转录；凭证或网络失败仅记录日志。返回的等待函数供调用方在退出前可选地
// 等待上报完成（最多 10s），避免短命进程（如 CLI）退出时丢掉未完成的上报。
func (s Service) ReportHotwordsAsync(ctx context.Context) (wait func()) {
	if len(s.Config.Hotwords) == 0 {
		return func() {}
	}
	device := asr.DeviceByName(s.Config.DeviceName)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 仅复用已注册凭证，绝不在此触发设备注册，避免与转录主流程争抢设备身份；
		// 凭证尚未就绪时跳过本次，待其就绪后的下次转录再报。
		creds, err := asr.LoadCredentials(s.Config.CredentialPath)
		if err != nil || creds.DeviceID == "" {
			return
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := asr.NewContextClient(creds, device, nil).ReportUserWords(rctx, s.Config.Hotwords); err != nil {
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

func (s Service) Runner() transcriber.Runner {
	return transcriber.Runner{Config: transcriber.Config{
		CredentialPath: s.Config.CredentialPath,
		UserAgent:      s.Config.UserAgent,
		WebSocketURL:   s.Config.WebSocketURL,
		AudioFormat:    s.Config.AudioFormat,
		Device:         asr.DeviceByName(s.Config.DeviceName),
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
