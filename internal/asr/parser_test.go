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

func TestParseInterimDelta(t *testing.T) {
	// 中间结果（is_interim=true）→ Delta，带 IsInterim 标志
	got, err := ParseResultJSON(`{"results":[{"text":"甚至","is_interim":true,"index":0}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ParsedDelta || got.Text != "甚至" || !got.IsInterim {
		t.Fatalf("expected interim delta, got %+v", got)
	}
}

func TestParseDefiniteDelta(t *testing.T) {
	// 已确定但 VAD 段未结束（is_interim=false, 无 vad_finished）→ 仍是 Delta，IsInterim=false
	got, err := ParseResultJSON(`{"results":[{"text":"甚至出现","is_interim":false,"index":0}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ParsedDelta || got.IsInterim {
		t.Fatalf("expected definite delta (not interim), got %+v", got)
	}
}

func TestParsePrefersNonstreamResult(t *testing.T) {
	// 最终帧含 vad_finished 与 nonstream_result 两个元素（同 index、文字可能不同），
	// 应取 nonstream_result（finish 后整句重识别，最准）。
	got, err := ParseResultJSON(`{"results":[
		{"text":"识别错的","is_interim":false,"is_vad_finished":true,"index":0},
		{"text":"识别对的","is_interim":false,"index":0,"extra":{"nonstream_result":true}}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ParsedFinal || got.Text != "识别对的" {
		t.Fatalf("expected nonstream result, got kind=%v text=%q", got.Kind, got.Text)
	}
}

func TestParseDeduplicatesMultiPassByIndex(t *testing.T) {
	// 同一语音段（index 相同）返回 twopass/threepass 两遍结果，应去重而非拼接
	got, err := ParseResultJSON(`{"results":[
		{"text":"甚至出现交易几乎停滞的情况。","is_interim":false,"is_vad_finished":true,"index":0},
		{"text":"甚至出现交易几乎停滞的情况。","is_interim":false,"index":0}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ParsedFinal || got.Text != "甚至出现交易几乎停滞的情况。" {
		t.Fatalf("expected deduplicated final, got kind=%v text=%q", got.Kind, got.Text)
	}
	if len(got.Results) != 1 {
		t.Fatalf("expected 1 result after dedup, got %d", len(got.Results))
	}
}

func TestParseKeepsDistinctIndexes(t *testing.T) {
	// 不同 index 是不同语音段，应顺序拼接而非去重
	got, err := ParseResultJSON(`{"results":[
		{"text":"第一句。","is_interim":false,"is_vad_finished":true,"index":0},
		{"text":"第二句。","is_interim":false,"is_vad_finished":true,"index":1}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "第一句。第二句。" {
		t.Fatalf("expected concatenated text, got %q", got.Text)
	}
	if len(got.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got.Results))
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
