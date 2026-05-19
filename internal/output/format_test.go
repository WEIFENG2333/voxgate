package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/WEIFENG2333/voxgate/internal/asr"
)

func TestDefaultFormat(t *testing.T) {
	if DefaultFormat(false, true) != Text {
		t.Fatal("tty should default text")
	}
	if DefaultFormat(false, false) != JSON {
		t.Fatal("pipe should default json")
	}
	if DefaultFormat(true, true) != NDJSON {
		t.Fatal("stream should default ndjson")
	}
}

func TestFormatValidation(t *testing.T) {
	for _, format := range []string{Text, JSON, VerboseJSON, SRT, VTT, NDJSON} {
		if !ValidResultFormat(format) {
			t.Fatalf("%s should be a valid result format", format)
		}
	}
	for _, format := range []string{Text, JSON, VerboseJSON, NDJSON} {
		if !ValidStreamFormat(format) {
			t.Fatalf("%s should be a valid stream format", format)
		}
	}
	if ValidResultFormat("xml") {
		t.Fatal("xml should not be valid")
	}
	if ValidStreamFormat(SRT) || ValidStreamFormat(VTT) {
		t.Fatal("subtitle formats should not be valid streaming formats")
	}
}

func TestFormatTimestamp(t *testing.T) {
	if got := FormatTimestamp(3661.234, ","); got != "01:01:01,234" {
		t.Fatalf("got %s", got)
	}
	if got := FormatTimestamp(1.2, "."); got != "00:00:01.200" {
		t.Fatalf("got %s", got)
	}
}

func TestSubtitleFallbackUsesDuration(t *testing.T) {
	var b bytes.Buffer
	err := WriteResult(&b, SRT, asr.Result{Text: "hello", Duration: 3.5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "00:00:00,000 --> 00:00:03,500") {
		t.Fatalf("bad srt fallback: %q", b.String())
	}
}

func TestSubtitleSingleZeroLengthSegmentUsesDuration(t *testing.T) {
	var b bytes.Buffer
	err := WriteResult(&b, SRT, asr.Result{
		Text:     "hello",
		Duration: 3.5,
		Segments: []asr.Segment{{Index: 0, Text: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "00:00:00,000 --> 00:00:03,500") {
		t.Fatalf("bad srt segment fallback: %q", b.String())
	}
}
