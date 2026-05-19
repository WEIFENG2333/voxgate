package transcriber

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
)

const (
	DefaultChunkDuration      = 300 * time.Second
	DefaultLongAudioThreshold = DefaultChunkDuration
)

type Config struct {
	CredentialPath string
	UserAgent      string
	WebSocketURL   string
	ChunkDuration  time.Duration
	ChunkThreshold time.Duration
}

type Runner struct {
	Config Config
}

func (r Runner) Transcribe(ctx context.Context, src *audio.Source, opts asr.Options, allowChunking bool) (asr.Result, error) {
	client := asr.Client{Config: asr.ClientConfig{
		CredentialPath: r.Config.CredentialPath,
		UserAgent:      r.Config.UserAgent,
		WebSocketURL:   r.Config.WebSocketURL,
	}}
	if allowChunking && src.Duration() > r.threshold() {
		return r.transcribeChunks(ctx, client, src.Chunks(r.chunkDuration()), opts)
	}
	events, err := r.Stream(ctx, src, opts)
	if err != nil {
		return asr.Result{}, err
	}
	return collect(events)
}

func (r Runner) Stream(ctx context.Context, src *audio.Source, opts asr.Options) (<-chan asr.Event, error) {
	enc, err := audio.NewOpusEncoder()
	if err != nil {
		return nil, err
	}
	client := asr.Client{Config: asr.ClientConfig{
		CredentialPath: r.Config.CredentialPath,
		UserAgent:      r.Config.UserAgent,
		WebSocketURL:   r.Config.WebSocketURL,
	}}
	return client.Transcribe(ctx, src, enc, opts)
}

func (r Runner) transcribeChunks(ctx context.Context, client asr.Client, chunks []*audio.Source, opts asr.Options) (asr.Result, error) {
	var b strings.Builder
	var segments []asr.Segment
	var offset float64
	for i, chunk := range chunks {
		enc, err := audio.NewOpusEncoder()
		if err != nil {
			return asr.Result{}, err
		}
		events, err := client.Transcribe(ctx, chunk, enc, opts)
		if err != nil {
			return asr.Result{}, err
		}
		res, err := collect(events)
		if err != nil {
			return asr.Result{}, fmt.Errorf("chunk %d: %w", i, err)
		}
		if strings.TrimSpace(res.Text) == "" {
			return asr.Result{}, fmt.Errorf("chunk %d produced empty transcript", i)
		}
		b.WriteString(res.Text)
		segments = append(segments, asr.Segment{Index: i, Text: res.Text, Start: offset, End: offset + chunk.Duration().Seconds()})
		offset += chunk.Duration().Seconds()
	}
	return asr.Result{Text: b.String(), Language: opts.Language, Duration: offset, Segments: segments}, nil
}

func collect(events <-chan asr.Event) (asr.Result, error) {
	var result asr.Result
	for ev := range events {
		if ev.Type == asr.EventError && ev.Error != nil {
			return result, fmt.Errorf("%s", ev.Error.Message)
		}
		if ev.Type == asr.EventTranscriptSegment {
			result.Segments = append(result.Segments, asr.Segment{Index: ev.SegmentIndex, Text: ev.Text, Start: ev.Start, End: ev.End})
		}
		if ev.Type == asr.EventTranscriptDone {
			result.Text = ev.Text
			result.Duration = ev.Duration
			result.Language = "zh"
		}
	}
	if strings.TrimSpace(result.Text) == "" {
		return result, fmt.Errorf("no transcript text returned")
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
