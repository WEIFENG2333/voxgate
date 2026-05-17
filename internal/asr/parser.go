package asr

import (
	"encoding/json"
	"strings"
)

type ParsedKind int

const (
	ParsedNoop ParsedKind = iota
	ParsedVADStart
	ParsedInterim
	ParsedDefinite
	ParsedFinal
)

type ParsedResult struct {
	Kind        ParsedKind
	Text        string
	Packet      int
	Raw         map[string]any
	VADFinished bool
}

type resultJSON struct {
	Results []struct {
		Text          string `json:"text"`
		IsInterim     *bool  `json:"is_interim"`
		IsVADFinished bool   `json:"is_vad_finished"`
		Extra         struct {
			NonstreamResult bool `json:"nonstream_result"`
		} `json:"extra"`
	} `json:"results"`
	Extra struct {
		VADStart     bool `json:"vad_start"`
		PacketNumber int  `json:"packet_number"`
	} `json:"extra"`
}

func ParseResultJSON(s string) (ParsedResult, error) {
	if strings.TrimSpace(s) == "" {
		return ParsedResult{Kind: ParsedNoop}, nil
	}
	var r resultJSON
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return ParsedResult{}, err
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(s), &raw)
	if r.Extra.VADStart {
		return ParsedResult{Kind: ParsedVADStart, Packet: r.Extra.PacketNumber, Raw: raw}, nil
	}
	if r.Results == nil {
		return ParsedResult{Kind: ParsedNoop, Packet: r.Extra.PacketNumber, Raw: raw}, nil
	}
	var text string
	isInterim := true
	vadFinished := false
	nonstream := false
	for _, item := range r.Results {
		text += item.Text
		if item.IsInterim != nil {
			isInterim = *item.IsInterim
		} else {
			isInterim = true
		}
		vadFinished = item.IsVADFinished
		nonstream = item.Extra.NonstreamResult
	}
	if text == "" {
		return ParsedResult{Kind: ParsedNoop, Packet: r.Extra.PacketNumber, Raw: raw}, nil
	}
	if nonstream || (!isInterim && vadFinished) {
		return ParsedResult{Kind: ParsedFinal, Text: text, VADFinished: vadFinished, Packet: r.Extra.PacketNumber, Raw: raw}, nil
	}
	if !isInterim {
		return ParsedResult{Kind: ParsedDefinite, Text: text, VADFinished: vadFinished, Packet: r.Extra.PacketNumber, Raw: raw}, nil
	}
	return ParsedResult{Kind: ParsedInterim, Text: text, Packet: r.Extra.PacketNumber, Raw: raw}, nil
}
