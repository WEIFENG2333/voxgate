package main

import (
	"context"
	"errors"
	"testing"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/transcription"
)

func TestParseGlobalAcceptsFlagsAfterSubcommand(t *testing.T) {
	g, rest, err := parseGlobal([]string{"doctor", "--credential-path", "custom.json", "--quiet", "--json-logs"})
	if err != nil {
		t.Fatal(err)
	}
	if g.credentialPath != "custom.json" || !g.quiet || !g.jsonLogs {
		t.Fatalf("bad globals: %+v", g)
	}
	if len(rest) != 1 || rest[0] != "doctor" {
		t.Fatalf("bad rest: %#v", rest)
	}
}

func TestParseGlobalKeepsSubcommandFlags(t *testing.T) {
	g, rest, err := parseGlobal([]string{"transcribe", "--format", "json", "--credential-path", "creds.json", "audio.wav"})
	if err != nil {
		t.Fatal(err)
	}
	if g.credentialPath != "creds.json" {
		t.Fatalf("credential path = %q, want creds.json", g.credentialPath)
	}
	want := []string{"transcribe", "--format", "json", "audio.wav"}
	if len(rest) != len(want) {
		t.Fatalf("rest = %#v, want %#v", rest, want)
	}
	for i := range want {
		if rest[i] != want[i] {
			t.Fatalf("rest = %#v, want %#v", rest, want)
		}
	}
}

func TestSubcommandHelpReturnsSuccess(t *testing.T) {
	for _, args := range [][]string{
		{"transcribe", "--help"},
		{"serve", "--help"},
	} {
		if got := run(args); got != 0 {
			t.Fatalf("run(%v) = %d, want 0", args, got)
		}
	}
}

func TestTranscribeRejectsInvalidFormatBeforeOpeningAudio(t *testing.T) {
	if got := run([]string{"transcribe", "--format", "xml", "missing.wav"}); got != 2 {
		t.Fatalf("invalid format exit = %d, want 2", got)
	}
}

func TestTranscribeRejectsSubtitleStreamingFormat(t *testing.T) {
	if got := run([]string{"transcribe", "--stream", "--format", "srt", "missing.wav"}); got != 2 {
		t.Fatalf("invalid stream format exit = %d, want 2", got)
	}
}

func TestIsLiveStdinStream(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		inputFormat string
		stream      bool
		want        bool
	}{
		{name: "pcm16 stdin stream", path: "-", inputFormat: "pcm16", stream: true, want: true},
		{name: "raw stdin stream", path: "-", inputFormat: "raw", stream: true, want: true},
		{name: "wav stdin is buffered", path: "-", inputFormat: "wav", stream: true, want: false},
		{name: "file is buffered", path: "speech.wav", inputFormat: "pcm16", stream: true, want: false},
		{name: "non stream is buffered", path: "-", inputFormat: "pcm16", stream: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLiveStdinStream(tt.path, tt.inputFormat, tt.stream)
			if got != tt.want {
				t.Fatalf("isLiveStdinStream() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStreamEventsRejectsLiveStdinSampleRate(t *testing.T) {
	_, err := streamEvents(context.Background(), transcription.Service{}, "-", "pcm16", 8000, asr.Options{}, true, true)
	if !errors.Is(err, errLiveStdinSampleRate) {
		t.Fatalf("streamEvents error = %v, want errLiveStdinSampleRate", err)
	}
}
