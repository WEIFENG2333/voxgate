package transcriber

import (
	"testing"
	"time"
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
