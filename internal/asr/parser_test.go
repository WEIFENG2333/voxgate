package asr

import "testing"

func TestParseThreePassFinal(t *testing.T) {
	got, err := ParseResultJSON(`{"results":[{"text":"你好","is_interim":false,"is_vad_finished":true,"extra":{"nonstream_result":true}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ParsedFinal || got.Text != "你好" {
		t.Fatalf("bad parse: %+v", got)
	}
}

func TestParseVADStart(t *testing.T) {
	got, err := ParseResultJSON(`{"extra":{"vad_start":true,"packet_number":7}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ParsedVADStart || got.Packet != 7 {
		t.Fatalf("bad vad parse: %+v", got)
	}
}

func TestParseRichResultMetadata(t *testing.T) {
	got, err := ParseResultJSON(`{
		"results":[{
			"text":"你好",
			"start_time":1.2,
			"end_time":2.3,
			"confidence":0.98,
			"is_interim":false,
			"is_vad_finished":true,
			"index":4,
			"alternatives":[{
				"text":"你好",
				"start_time":1.2,
				"end_time":2.3,
				"words":[{"word":"你","start_time":1.2,"end_time":1.5}],
				"semantic_related_to_prev":true,
				"oi_decoding_info":{"oi_former_word_num":1,"oi_latter_word_num":2,"oi_words":["你"]}
			}],
			"extra":{"nonstream_result":true}
		}],
		"extra":{"packet_number":9,"model_avg_rtf":0.12,"model_total_process_time":34}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ParsedFinal || got.Text != "你好" || len(got.Results) != 1 {
		t.Fatalf("bad parse: %+v", got)
	}
	res := got.Results[0]
	if res.Start != 1.2 || res.End != 2.3 || res.Confidence != 0.98 || res.Index != 4 {
		t.Fatalf("missing result metadata: %+v", res)
	}
	if len(res.Alternatives) != 1 || len(res.Alternatives[0].Words) != 1 {
		t.Fatalf("missing alternatives/words: %+v", res.Alternatives)
	}
	if got.Extra.ModelAvgRTF == nil || *got.Extra.ModelAvgRTF != 0.12 || got.Extra.ModelTotalProcessTime == nil {
		t.Fatalf("missing extra metadata: %+v", got.Extra)
	}
}
