package asr

import "time"

type EventType string

const (
	EventTaskStarted       EventType = "task.started"
	EventSessionStarted    EventType = "session.started"
	EventVADStart          EventType = "vad.start"
	EventTranscriptDelta   EventType = "transcript.delta"
	EventTranscriptSegment EventType = "transcript.segment"
	EventTranscriptDone    EventType = "transcript.done"
	EventError             EventType = "error"
)

type Event struct {
	Type         EventType      `json:"type"`
	RequestID    string         `json:"request_id,omitempty"`
	Text         string         `json:"text,omitempty"`
	IsInterim    bool           `json:"is_interim,omitempty"`
	SegmentIndex int            `json:"segment_index,omitempty"`
	Start        float64        `json:"start,omitempty"`
	End          float64        `json:"end,omitempty"`
	Duration     float64        `json:"duration,omitempty"`
	TimestampMS  int64          `json:"timestamp_ms,omitempty"`
	Error        *ErrorPayload  `json:"error,omitempty"`
	Raw          map[string]any `json:"-"`
}

type ErrorPayload struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type Segment struct {
	Index int     `json:"id"`
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type Result struct {
	Text     string    `json:"text"`
	Language string    `json:"language,omitempty"`
	Duration float64   `json:"duration,omitempty"`
	Segments []Segment `json:"segments,omitempty"`
}

type Options struct {
	EnablePunctuation bool
	EnableThreePass   bool
	EnableTwoPass     bool
	Language          string
	Prompt            string
	RequestTimeout    time.Duration
	Realtime          bool
}

func DefaultOptions() Options {
	return Options{
		EnablePunctuation: true,
		EnableThreePass:   true,
		EnableTwoPass:     true,
		Language:          "zh",
		RequestTimeout:    60 * time.Second,
	}
}
