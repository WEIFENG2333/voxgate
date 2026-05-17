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
