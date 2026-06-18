package asr

import "strings"

// Assembler derives OpenAI-style append-only deltas from the cumulative
// transcript stream. The core events already carry the whole transcript in each
// transcript.partial / transcript.done, so this only tracks how much has been
// exposed as append deltas; the SSE and Realtime endpoints use it to turn an
// in-place-revised full text into append-only output.
type Assembler struct {
	sent string // full text already exposed as append deltas
}

// Apply folds one transcript event and returns the cumulative full transcript
// and the append-only delta since the previous Apply. The delta is empty when
// the change was a non-append revision; append-only consumers ignore it and
// rely on Full (delivered at done) to settle.
func (a *Assembler) Apply(ev Event) (full, delta string) {
	switch ev.Type {
	case EventTranscriptPartial, EventTranscriptDone:
		full = ev.Text
	default:
		full = a.sent
	}
	if strings.HasPrefix(full, a.sent) {
		delta = full[len(a.sent):]
		a.sent = full
	}
	return full, delta
}

// Full returns the cumulative transcript exposed so far.
func (a *Assembler) Full() string { return a.sent }
