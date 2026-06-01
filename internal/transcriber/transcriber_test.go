package transcriber

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
)

func TestDefaultChunkPolicy(t *testing.T) {
	if DefaultChunkDuration != 300*time.Second {
		t.Fatalf("default chunk duration = %v, want 300s", DefaultChunkDuration)
	}
	if DefaultLongAudioThreshold != DefaultChunkDuration {
		t.Fatalf("default long-audio threshold = %v, want chunk duration %v", DefaultLongAudioThreshold, DefaultChunkDuration)
	}
	r := Runner{}
	if got := r.threshold(); got != DefaultChunkDuration {
		t.Fatalf("default threshold = %v, want %v", got, DefaultChunkDuration)
	}
	if got := r.chunkDuration(); got != DefaultChunkDuration {
		t.Fatalf("default chunk duration = %v, want %v", got, DefaultChunkDuration)
	}
}

func TestChunkPolicyOverrides(t *testing.T) {
	r := Runner{Config: Config{
		ChunkThreshold: 120 * time.Second,
		ChunkDuration:  180 * time.Second,
	}}
	if got := r.threshold(); got != 120*time.Second {
		t.Fatalf("threshold override = %v, want 120s", got)
	}
	if got := r.chunkDuration(); got != 180*time.Second {
		t.Fatalf("chunk duration override = %v, want 180s", got)
	}
}

func TestStreamChunkDurationCapsAtFallbackDuration(t *testing.T) {
	if got := (Runner{}).streamChunkDuration(); got != fallbackChunkDuration {
		t.Fatalf("default stream chunk duration = %v, want %v", got, fallbackChunkDuration)
	}
	r := Runner{Config: Config{ChunkDuration: 30 * time.Second}}
	if got := r.streamChunkDuration(); got != 30*time.Second {
		t.Fatalf("short stream chunk duration = %v, want 30s", got)
	}
}

func TestTranscribeChunksFallsBackOnEmptyChunk(t *testing.T) {
	src := silentSource(120 * time.Second)
	client := fakeStreamClient{emptyAbove: fallbackChunkDuration}

	res, err := Runner{}.transcribeChunks(context.Background(), client, src.Chunks(120*time.Second), asr.Options{Language: "zh"})
	if err != nil {
		t.Fatalf("transcribe chunks returned error: %v", err)
	}
	if got, want := res.Text, "partpart"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if len(res.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(res.Segments))
	}
	if res.Segments[0].Start != 0 || res.Segments[0].End != 60 {
		t.Fatalf("first segment time = %.0f..%.0f, want 0..60", res.Segments[0].Start, res.Segments[0].End)
	}
	if res.Segments[1].Start != 60 || res.Segments[1].End != 120 {
		t.Fatalf("second segment time = %.0f..%.0f, want 60..120", res.Segments[1].Start, res.Segments[1].End)
	}
}

func TestTranscribeChunksSkipsMinimumEmptyChunk(t *testing.T) {
	src := silentSource(60 * time.Second)
	client := fakeStreamClient{emptyAbove: 0}

	res, err := Runner{}.transcribeChunks(context.Background(), client, src.Chunks(60*time.Second), asr.Options{Language: "zh"})
	if err != nil {
		t.Fatalf("transcribe chunks returned error: %v", err)
	}
	if strings.TrimSpace(res.Text) != "" {
		t.Fatalf("text = %q, want empty", res.Text)
	}
	if len(res.Segments) != 0 {
		t.Fatalf("segments = %d, want 0", len(res.Segments))
	}
}

func TestStreamChunksAggregatesFinalsAndSuppressesChunkDone(t *testing.T) {
	src := silentSource(120 * time.Second)
	client := &scriptedStreamClient{events: [][]asr.Event{
		{
			{Type: asr.EventTranscriptDelta, Text: "a"},
			{Type: asr.EventTranscriptFinal, Text: "甲"},
			{Type: asr.EventStreamDone},
		},
		{
			{Type: asr.EventTranscriptFinal, Text: "乙"},
			{Type: asr.EventTranscriptFinal, Text: "丙"},
			{Type: asr.EventStreamDone},
		},
	}}
	out := make(chan asr.Event, 8)

	Runner{Config: Config{ChunkDuration: 60 * time.Second}}.streamChunks(context.Background(), out, client, src.Chunks(60*time.Second), asr.Options{})

	var got []asr.Event
	for ev := range out {
		got = append(got, ev)
	}
	if len(got) != 3 {
		t.Fatalf("events = %#v, want 3 events", got)
	}
	if got[0].Type != asr.EventTranscriptDelta || got[0].Text != "a" {
		t.Fatalf("first event = %#v, want delta a", got[0])
	}
	if got[1].Type != asr.EventTranscriptFinal || got[1].Text != "甲乙丙" {
		t.Fatalf("final event = %#v, want aggregated final", got[1])
	}
	if got[2].Type != asr.EventStreamDone {
		t.Fatalf("last event = %#v, want stream.done", got[2])
	}
}

type fakeStreamClient struct {
	emptyAbove time.Duration
}

func (f fakeStreamClient) Transcribe(_ context.Context, src asr.PCMFrameSource, _ asr.PCMFrameEncoder, _ asr.Options) (<-chan asr.Event, error) {
	events := make(chan asr.Event, 4)
	if f.emptyAbove == 0 || src.Duration() > f.emptyAbove {
		close(events)
		return events, nil
	}
	events <- asr.Event{Type: asr.EventTranscriptFinal, Text: "part", Duration: src.Duration().Seconds()}
	close(events)
	return events, nil
}

type scriptedStreamClient struct {
	events [][]asr.Event
	calls  int
}

func (s *scriptedStreamClient) Transcribe(_ context.Context, _ asr.PCMFrameSource, _ asr.PCMFrameEncoder, _ asr.Options) (<-chan asr.Event, error) {
	events := make(chan asr.Event, 4)
	if s.calls < len(s.events) {
		for _, ev := range s.events[s.calls] {
			events <- ev
		}
	}
	s.calls++
	close(events)
	return events, nil
}

func silentSource(d time.Duration) *audio.Source {
	return audio.NewSourceFromPCM(make([]byte, int(d.Seconds())*audio.SampleRate*2))
}
