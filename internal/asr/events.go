package asr

import "time"

type EventType string

const (
	EventTaskStarted       EventType = "task.started"
	EventSessionStarted    EventType = "session.started"
	EventVADStart          EventType = "speech.started"
	EventTranscriptPartial EventType = "transcript.partial"
	EventTranscriptDone    EventType = "transcript.done"
	EventError             EventType = "error"
)

// Event is the internal streaming envelope. The transcript model is cumulative
// full text: each recognition frame becomes one transcript.partial whose Text is
// the whole transcript so far (it grows and may be revised in place as the
// backend refines wording and punctuation). transcript.done signals end of
// stream and carries the final full text. There are no segments and no boundary
// guessing: a consumer renders the latest partial and settles on done. End/Duration
// are audio-position metadata; Start is always 0 because the text starts at the
// beginning of the stream.
type Event struct {
	Type         EventType      `json:"type"`
	RequestID    string         `json:"request_id,omitempty"`
	Text         string         `json:"text,omitempty"`
	Start        float64        `json:"start,omitempty"`
	End          float64        `json:"end,omitempty"`
	AudioStartMS int64          `json:"audio_start_ms,omitempty"`
	AudioEndMS   int64          `json:"audio_end_ms,omitempty"`
	Duration     float64        `json:"duration,omitempty"`
	TimestampMS  int64          `json:"timestamp_ms,omitempty"`
	Error        *ErrorPayload  `json:"error,omitempty"`
	Results      []ASRResult    `json:"results,omitempty"`
	Extra        *ASRExtra      `json:"extra,omitempty"`
	Raw          map[string]any `json:"-"`
}

type ErrorPayload struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type Segment struct {
	Index        int           `json:"id"`
	Text         string        `json:"text"`
	Start        float64       `json:"start"`
	End          float64       `json:"end"`
	Confidence   float64       `json:"confidence,omitempty"`
	Words        []ASRWord     `json:"words,omitempty"`
	Alternatives []Alternative `json:"alternatives,omitempty"`
}

type Result struct {
	Text     string      `json:"text"`
	Language string      `json:"language,omitempty"`
	Duration float64     `json:"duration,omitempty"`
	Segments []Segment   `json:"segments,omitempty"`
	Results  []ASRResult `json:"results,omitempty"`
	Extra    *ASRExtra   `json:"extra,omitempty"`
}

type ASRWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start_time,omitempty"`
	End   float64 `json:"end_time,omitempty"`
}

type OIDecodingInfo struct {
	FormerWordNum int      `json:"oi_former_word_num,omitempty"`
	LatterWordNum int      `json:"oi_latter_word_num,omitempty"`
	Words         []string `json:"oi_words,omitempty"`
}

type Alternative struct {
	Text                  string          `json:"text"`
	Start                 float64         `json:"start_time,omitempty"`
	End                   float64         `json:"end_time,omitempty"`
	Words                 []ASRWord       `json:"words,omitempty"`
	SemanticRelatedToPrev *bool           `json:"semantic_related_to_prev,omitempty"`
	OIDecodingInfo        *OIDecodingInfo `json:"oi_decoding_info,omitempty"`
}

type ASRResult struct {
	Text            string        `json:"text"`
	Start           float64       `json:"start_time,omitempty"`
	End             float64       `json:"end_time,omitempty"`
	Confidence      float64       `json:"confidence,omitempty"`
	Alternatives    []Alternative `json:"alternatives,omitempty"`
	IsInterim       bool          `json:"is_interim"`
	IsVADFinished   bool          `json:"is_vad_finished,omitempty"`
	NonstreamResult bool          `json:"nonstream_result,omitempty"`
	Index           int           `json:"index"`
}

type ASRExtra struct {
	AudioDuration           *int           `json:"audio_duration,omitempty"`
	ModelAvgRTF             *float64       `json:"model_avg_rtf,omitempty"`
	ModelSendFirstResponse  *int           `json:"model_send_first_response,omitempty"`
	SpeechAdaptationVersion string         `json:"speech_adaptation_version,omitempty"`
	ModelTotalProcessTime   *int           `json:"model_total_process_time,omitempty"`
	PacketNumber            int            `json:"packet_number,omitempty"`
	VADStart                bool           `json:"vad_start,omitempty"`
	ReqPayload              map[string]any `json:"req_payload,omitempty"`
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
