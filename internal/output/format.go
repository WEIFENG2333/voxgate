package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/WEIFENG2333/ime-asr/internal/asr"
)

const (
	Text        = "text"
	JSON        = "json"
	VerboseJSON = "verbose_json"
	SRT         = "srt"
	VTT         = "vtt"
	NDJSON      = "ndjson"
)

func DefaultFormat(stream, stdoutTTY bool) string {
	if stream {
		return NDJSON
	}
	if stdoutTTY {
		return Text
	}
	return JSON
}

func WriteResult(w io.Writer, format string, result asr.Result) error {
	switch format {
	case Text:
		_, err := fmt.Fprintln(w, result.Text)
		return err
	case JSON:
		return json.NewEncoder(w).Encode(map[string]string{"text": result.Text})
	case VerboseJSON:
		return json.NewEncoder(w).Encode(result)
	case SRT:
		return writeSRT(w, result.Segments, result.Text)
	case VTT:
		_, _ = fmt.Fprint(w, "WEBVTT\n\n")
		return writeCues(w, result.Segments, result.Text, true)
	case NDJSON:
		return json.NewEncoder(w).Encode(asr.Event{Type: asr.EventTranscriptDone, Text: result.Text, Duration: result.Duration})
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func WriteEvent(w io.Writer, format string, event asr.Event) error {
	switch format {
	case NDJSON, JSON, VerboseJSON:
		return json.NewEncoder(w).Encode(event)
	case Text:
		if event.Type == asr.EventTranscriptDelta || event.Type == asr.EventTranscriptDone {
			_, err := fmt.Fprint(w, event.Text)
			return err
		}
		return nil
	default:
		return json.NewEncoder(w).Encode(event)
	}
}

func writeSRT(w io.Writer, segments []asr.Segment, text string) error {
	return writeCues(w, segments, text, false)
}

func writeCues(w io.Writer, segments []asr.Segment, text string, vtt bool) error {
	if len(segments) == 0 {
		segments = []asr.Segment{{Index: 0, Text: text, Start: 0, End: 0}}
	}
	for i, seg := range segments {
		if !vtt {
			if _, err := fmt.Fprintf(w, "%d\n", i+1); err != nil {
				return err
			}
		}
		sep := ","
		if vtt {
			sep = "."
		}
		if _, err := fmt.Fprintf(w, "%s --> %s\n%s\n\n", FormatTimestamp(seg.Start, sep), FormatTimestamp(seg.End, sep), strings.TrimSpace(seg.Text)); err != nil {
			return err
		}
	}
	return nil
}

func FormatTimestamp(seconds float64, sep string) string {
	if seconds < 0 {
		seconds = 0
	}
	d := time.Duration(seconds*1000) * time.Millisecond
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int(d / time.Second)
	d -= time.Duration(s) * time.Second
	ms := int(d / time.Millisecond)
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", h, m, s, sep, ms)
}
