package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

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
		{"version", "--help"},
		{"update", "--help"},
	} {
		if got := run(args); got != 0 {
			t.Fatalf("run(%v) = %d, want 0", args, got)
		}
	}
}

func TestUpdateRejectsUnexpectedArgs(t *testing.T) {
	if got := updateCmd([]string{"now"}); got != 2 {
		t.Fatalf("update exit = %d, want 2", got)
	}
}

func TestSameVersion(t *testing.T) {
	if !sameVersion(version, "v"+version) {
		t.Fatal("expected v-prefixed latest tag to match current version")
	}
	if sameVersion(version, "v0.0.0") {
		t.Fatal("expected different versions not to match")
	}
}

func TestCompareVersion(t *testing.T) {
	if got := compareVersion("0.2.9", "v0.2.8"); got != 1 {
		t.Fatalf("compare newer = %d, want 1", got)
	}
	if got := compareVersion("0.2.8", "v0.2.9"); got != -1 {
		t.Fatalf("compare older = %d, want -1", got)
	}
	if got := compareVersion("0.2.9", "v0.2.9"); got != 0 {
		t.Fatalf("compare equal = %d, want 0", got)
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

func TestLiveRequestTimeout(t *testing.T) {
	if got := liveRequestTimeout(10*time.Minute, true, false); got != 0 {
		t.Fatalf("live default timeout = %v, want disabled", got)
	}
	if got := liveRequestTimeout(30*time.Second, true, true); got != 30*time.Second {
		t.Fatalf("live explicit timeout = %v, want 30s", got)
	}
	if got := liveRequestTimeout(10*time.Minute, false, false); got != 10*time.Minute {
		t.Fatalf("file timeout = %v, want 10m", got)
	}
}

func TestStreamEventsRejectsLiveStdinSampleRate(t *testing.T) {
	_, err := streamEvents(context.Background(), transcription.Service{}, "-", "pcm16", 8000, asr.Options{}, true, true)
	if !errors.Is(err, errLiveStdinSampleRate) {
		t.Fatalf("streamEvents error = %v, want errLiveStdinSampleRate", err)
	}
}

func TestWriteTextStreamEventsPipeWritesFinalLinesOnly(t *testing.T) {
	events := make(chan asr.Event, 3)
	events <- asr.Event{Type: asr.EventTranscriptDelta, Text: "你"}
	events <- asr.Event{Type: asr.EventTranscriptDelta, Text: "你好"}
	events <- asr.Event{Type: asr.EventTranscriptCompleted, Text: "你好。"}
	close(events)

	var buf bytes.Buffer
	if got := writeTextStreamEvents(&buf, events, textStreamDisplay{}); got != 0 {
		t.Fatalf("exit = %d, want 0", got)
	}
	if got, want := buf.String(), "你好。\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestWriteTextStreamEventsIgnoresShorterInterimText(t *testing.T) {
	events := make(chan asr.Event, 3)
	events <- asr.Event{Type: asr.EventTranscriptDelta, Text: "你好啊"}
	events <- asr.Event{Type: asr.EventTranscriptDelta, Text: "你好"}
	events <- asr.Event{Type: asr.EventTranscriptCompleted, Text: "你好。"}
	close(events)

	var buf bytes.Buffer
	display := textStreamDisplay{Interactive: true, Width: 80}
	if got := writeTextStreamEvents(&buf, events, display); got != 0 {
		t.Fatalf("exit = %d, want 0", got)
	}
	if got, want := buf.String(), "\r\033[2K你好啊\r\033[2K你好\r\033[2K你好。\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestTextStreamDisplayPreviewTruncatesLongInterimText(t *testing.T) {
	display := textStreamDisplay{Interactive: true, Width: 8}
	if got, want := display.preview("一二三四五六七八九"), "…八九"; got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
}
