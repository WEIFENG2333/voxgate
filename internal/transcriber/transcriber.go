package transcriber

import (
	"context"
	"errors"
	"fmt"
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

type streamClient interface {
	Transcribe(context.Context, asr.PCMFrameSource, asr.PCMFrameEncoder, asr.Options) (<-chan asr.Event, error)
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
	res, err := transcribeOne(ctx, client, chunk, opts)
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
