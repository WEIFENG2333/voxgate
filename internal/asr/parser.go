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
	Results     []ASRResult
	Extra       ASRExtra
}

type resultJSON struct {
	Results []struct {
		Text         string  `json:"text"`
		StartTime    float64 `json:"start_time"`
		EndTime      float64 `json:"end_time"`
		Confidence   float64 `json:"confidence"`
		Alternatives []struct {
			Text      string  `json:"text"`
			StartTime float64 `json:"start_time"`
			EndTime   float64 `json:"end_time"`
			Words     []struct {
				Word      string  `json:"word"`
				StartTime float64 `json:"start_time"`
				EndTime   float64 `json:"end_time"`
			} `json:"words"`
			SemanticRelatedToPrev *bool `json:"semantic_related_to_prev"`
			OIDecodingInfo        *struct {
				FormerWordNum int      `json:"oi_former_word_num"`
				LatterWordNum int      `json:"oi_latter_word_num"`
				Words         []string `json:"oi_words"`
			} `json:"oi_decoding_info"`
		} `json:"alternatives"`
		IsInterim     *bool `json:"is_interim"`
		IsVADFinished bool  `json:"is_vad_finished"`
		Index         int   `json:"index"`
		Extra         struct {
			NonstreamResult bool `json:"nonstream_result"`
		} `json:"extra"`
	} `json:"results"`
	Extra struct {
		AudioDuration           *int           `json:"audio_duration"`
		ModelAvgRTF             *float64       `json:"model_avg_rtf"`
		ModelSendFirstResponse  *int           `json:"model_send_first_response"`
		SpeechAdaptationVersion string         `json:"speech_adaptation_version"`
		ModelTotalProcessTime   *int           `json:"model_total_process_time"`
		PacketNumber            int            `json:"packet_number"`
		VADStart                bool           `json:"vad_start"`
		ReqPayload              map[string]any `json:"req_payload"`
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
	extra := parseExtra(r)
	results := parseResults(r)
	if r.Extra.VADStart {
		return ParsedResult{Kind: ParsedVADStart, Packet: r.Extra.PacketNumber, Raw: raw, Results: results, Extra: extra}, nil
	}
	if r.Results == nil {
		return ParsedResult{Kind: ParsedNoop, Packet: r.Extra.PacketNumber, Raw: raw, Results: results, Extra: extra}, nil
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
		return ParsedResult{Kind: ParsedNoop, Packet: r.Extra.PacketNumber, Raw: raw, Results: results, Extra: extra}, nil
	}
	if nonstream || (!isInterim && vadFinished) {
		return ParsedResult{Kind: ParsedFinal, Text: text, VADFinished: vadFinished, Packet: r.Extra.PacketNumber, Raw: raw, Results: results, Extra: extra}, nil
	}
	if !isInterim {
		return ParsedResult{Kind: ParsedDefinite, Text: text, VADFinished: vadFinished, Packet: r.Extra.PacketNumber, Raw: raw, Results: results, Extra: extra}, nil
	}
	return ParsedResult{Kind: ParsedInterim, Text: text, Packet: r.Extra.PacketNumber, Raw: raw, Results: results, Extra: extra}, nil
}

func parseExtra(r resultJSON) ASRExtra {
	return ASRExtra{
		AudioDuration:           r.Extra.AudioDuration,
		ModelAvgRTF:             r.Extra.ModelAvgRTF,
		ModelSendFirstResponse:  r.Extra.ModelSendFirstResponse,
		SpeechAdaptationVersion: r.Extra.SpeechAdaptationVersion,
		ModelTotalProcessTime:   r.Extra.ModelTotalProcessTime,
		PacketNumber:            r.Extra.PacketNumber,
		VADStart:                r.Extra.VADStart,
		ReqPayload:              r.Extra.ReqPayload,
	}
}

func parseResults(r resultJSON) []ASRResult {
	out := make([]ASRResult, 0, len(r.Results))
	for _, item := range r.Results {
		isInterim := true
		if item.IsInterim != nil {
			isInterim = *item.IsInterim
		}
		res := ASRResult{
			Text:          item.Text,
			Start:         item.StartTime,
			End:           item.EndTime,
			Confidence:    item.Confidence,
			IsInterim:     isInterim,
			IsVADFinished: item.IsVADFinished,
			Index:         item.Index,
		}
		for _, alt := range item.Alternatives {
			a := Alternative{
				Text:                  alt.Text,
				Start:                 alt.StartTime,
				End:                   alt.EndTime,
				SemanticRelatedToPrev: alt.SemanticRelatedToPrev,
			}
			for _, w := range alt.Words {
				a.Words = append(a.Words, ASRWord{Word: w.Word, Start: w.StartTime, End: w.EndTime})
			}
			if alt.OIDecodingInfo != nil {
				a.OIDecodingInfo = &OIDecodingInfo{
					FormerWordNum: alt.OIDecodingInfo.FormerWordNum,
					LatterWordNum: alt.OIDecodingInfo.LatterWordNum,
					Words:         alt.OIDecodingInfo.Words,
				}
			}
			res.Alternatives = append(res.Alternatives, a)
		}
		out = append(out, res)
	}
	return out
}
