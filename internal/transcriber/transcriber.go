package transcriber

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
)

const (
	DefaultChunkDuration      = 300 * time.Second
	DefaultLongAudioThreshold = DefaultChunkDuration
	fallbackChunkDuration     = 60 * time.Second
)

var ErrEmptyTranscript = errors.New("no transcript text returned")

type Config struct {
	CredentialPath string
	UserAgent      string
	WebSocketURL   string
	AudioFormat    string            // speech_opus | raw
	Device         asr.DeviceProfile // 设备画像
	ChunkDuration  time.Duration
	ChunkThreshold time.Duration
	TraceWriter    io.Writer
}

type Runner struct {
	Config Config
}

// clientConfig 由 Runner 配置组装 asr.ClientConfig（端点/格式/设备/凭证）。
func (r Runner) clientConfig() asr.ClientConfig {
	return asr.ClientConfig{
		CredentialPath: r.Config.CredentialPath,
		UserAgent:      r.Config.UserAgent,
		WebSocketURL:   r.Config.WebSocketURL,
		AudioFormat:    r.Config.AudioFormat,
		Device:         r.Config.Device,
		TraceWriter:    r.Config.TraceWriter,
	}
}

func (r Runner) Transcribe(ctx context.Context, src *audio.Source, opts asr.Options, allowChunking bool) (asr.Result, error) {
	client := asr.Client{Config: r.clientConfig()}
	if allowChunking && src.Duration() > r.threshold() {
		return r.transcribeChunks(ctx, client, src.Chunks(r.chunkDuration()), opts)
	}
	return r.transcribeOne(ctx, client, src, opts)
}

func (r Runner) Stream(ctx context.Context, src *audio.Source, opts asr.Options) (<-chan asr.Event, error) {
	return r.StreamFrames(ctx, src, opts)
}

func (r Runner) StreamFrames(ctx context.Context, src asr.PCMFrameSource, opts asr.Options) (<-chan asr.Event, error) {
	enc, err := audio.NewOpusEncoder()
	if err != nil {
		return nil, err
	}
	client := asr.Client{Config: r.clientConfig()}
	return client.Transcribe(ctx, src, enc, opts)
}

func (r Runner) StreamWithChunking(ctx context.Context, src *audio.Source, opts asr.Options, allowChunking bool) (<-chan asr.Event, error) {
	if !allowChunking || src.Duration() <= r.threshold() {
		return r.Stream(ctx, src, opts)
	}
	client := asr.Client{Config: r.clientConfig()}
	out := make(chan asr.Event, 32)
	go r.streamChunks(ctx, out, client, src.Chunks(r.streamChunkDuration()), opts)
	return out, nil
}

type streamClient interface {
	Transcribe(context.Context, asr.PCMFrameSource, asr.PCMFrameEncoder, asr.Options) (<-chan asr.Event, error)
}

func (r Runner) streamChunks(ctx context.Context, out chan<- asr.Event, client streamClient, chunks []*audio.Source, opts asr.Options) {
	defer close(out)
	var b strings.Builder
	var offset float64
	for _, chunk := range chunks {
		// Long-file streaming uses serial chunks to stay within upstream session
		// limits while keeping timestamps monotonic for downstream consumers.
		enc, err := audio.NewOpusEncoder()
		if err != nil {
			out <- asr.Event{Type: asr.EventError, Error: &asr.ErrorPayload{Code: "encoder_error", Message: err.Error()}}
			return
		}
		events, err := client.Transcribe(ctx, chunk, enc, opts)
		if err != nil {
			out <- asr.Event{Type: asr.EventError, Error: &asr.ErrorPayload{Code: "asr_error", Message: err.Error()}}
			return
		}
		var chunkText strings.Builder
		for ev := range events {
			if ev.Type == asr.EventError {
				out <- ev
				return
			}
			if ev.Type == asr.EventTranscriptFinal {
				chunkText.WriteString(ev.Text)
				continue
			}
			if ev.Type == asr.EventStreamDone {
				continue
			}
			out <- ev
		}
		b.WriteString(chunkText.String())
		offset += chunk.Duration().Seconds()
	}
	out <- asr.Event{Type: asr.EventTranscriptFinal, Text: b.String(), Duration: offset}
	out <- asr.Event{Type: asr.EventStreamDone, Duration: offset}
}

func (r Runner) transcribeChunks(ctx context.Context, client streamClient, chunks []*audio.Source, opts asr.Options) (asr.Result, error) {
	var b strings.Builder
	var segments []asr.Segment
	var offset float64
	nextSegmentIndex := 0
	for i, chunk := range chunks {
		res, err := r.transcribeChunkWithFallback(ctx, client, chunk, opts, offset, &nextSegmentIndex)
		if err != nil {
			return asr.Result{}, fmt.Errorf("chunk %d: %w", i, err)
		}
		b.WriteString(res.Text)
		segments = append(segments, res.Segments...)
		offset += chunk.Duration().Seconds()
	}
	return asr.Result{Text: b.String(), Language: opts.Language, Duration: offset, Segments: segments}, nil
}

func (r Runner) transcribeChunkWithFallback(ctx context.Context, client streamClient, chunk *audio.Source, opts asr.Options, offset float64, nextSegmentIndex *int) (asr.Result, error) {
	res, err := r.transcribeOne(ctx, client, chunk, opts)
	if err == nil {
		if strings.TrimSpace(res.Text) == "" {
			err = ErrEmptyTranscript
		} else {
			return normalizeChunkResult(res, opts.Language, offset, chunk.Duration().Seconds(), nextSegmentIndex), nil
		}
	}
	if !errors.Is(err, ErrEmptyTranscript) || chunk.Duration() <= fallbackChunkDuration {
		if errors.Is(err, ErrEmptyTranscript) {
			return asr.Result{Language: opts.Language, Duration: chunk.Duration().Seconds()}, nil
		}
		return asr.Result{}, err
	}

	// Some long chunks complete with an empty transcript even though smaller
	// slices work. Retry recursively at a smaller duration before giving up.
	var combined asr.Result
	var b strings.Builder
	subOffset := offset
	for _, sub := range chunk.Chunks(fallbackChunkDuration) {
		subRes, subErr := r.transcribeChunkWithFallback(ctx, client, sub, opts, subOffset, nextSegmentIndex)
		if subErr != nil {
			return asr.Result{}, subErr
		}
		b.WriteString(subRes.Text)
		combined.Segments = append(combined.Segments, subRes.Segments...)
		subOffset += sub.Duration().Seconds()
	}
	combined.Text = b.String()
	combined.Language = opts.Language
	combined.Duration = chunk.Duration().Seconds()
	return combined, nil
}

func transcribeOne(ctx context.Context, client streamClient, src *audio.Source, opts asr.Options) (asr.Result, error) {
	enc, err := audio.NewOpusEncoder()
	if err != nil {
		return asr.Result{}, err
	}
	events, err := client.Transcribe(ctx, src, enc, opts)
	if err != nil {
		return asr.Result{}, err
	}
	return collect(events)
}

func (r Runner) transcribeOne(ctx context.Context, client streamClient, src *audio.Source, opts asr.Options) (asr.Result, error) {
	res, err := transcribeOne(ctx, client, src, opts)
	if !isCredentialRecoverable(err) {
		return res, err
	}
	_, reissueErr := (asr.CredentialManager{Path: r.Config.CredentialPath, UserAgent: r.Config.UserAgent}).Reissue(ctx)
	if reissueErr != nil {
		return res, err
	}
	return transcribeOne(ctx, client, src.Clone(), opts)
}

func isCredentialRecoverable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "service discovery failure")
}

func normalizeChunkResult(res asr.Result, language string, offset, duration float64, nextSegmentIndex *int) asr.Result {
	res.Language = language
	res.Duration = duration
	if len(res.Segments) == 0 {
		res.Segments = []asr.Segment{{Text: res.Text, Start: offset, End: offset + duration}}
	} else {
		for i := range res.Segments {
			res.Segments[i].Start += offset
			res.Segments[i].End += offset
		}
	}
	for i := range res.Segments {
		res.Segments[i].Index = *nextSegmentIndex
		(*nextSegmentIndex)++
	}
	return res
}

func collect(events <-chan asr.Event) (asr.Result, error) {
	var result asr.Result
	var text strings.Builder
	for ev := range events {
		if ev.Type == asr.EventError && ev.Error != nil {
			return result, fmt.Errorf("%s", ev.Error.Message)
		}
		if ev.Type == asr.EventTranscriptFinal {
			text.WriteString(ev.Text)
			result.Text = text.String()
			result.Duration = ev.Duration
			result.Language = "zh"
			result.Results = ev.Results
			result.Extra = ev.Extra
		}
	}
	if strings.TrimSpace(result.Text) == "" {
		return result, ErrEmptyTranscript
	}
	return result, nil
}

func (r Runner) threshold() time.Duration {
	if r.Config.ChunkThreshold > 0 {
		return r.Config.ChunkThreshold
	}
	return DefaultLongAudioThreshold
}

func (r Runner) chunkDuration() time.Duration {
	if r.Config.ChunkDuration > 0 {
		return r.Config.ChunkDuration
	}
	return DefaultChunkDuration
}

func (r Runner) streamChunkDuration() time.Duration {
	d := r.chunkDuration()
	if d <= 0 || d > fallbackChunkDuration {
		return fallbackChunkDuration
	}
	return d
}
