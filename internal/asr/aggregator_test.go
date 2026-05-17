package asr

import "testing"

func TestSegmentResetAggregatorPreservesPreviousSegment(t *testing.T) {
	var a SegmentResetAggregator
	full, reset, _ := a.Update("今天天气真好我们一起出去玩")
	if reset || full == "" {
		t.Fatalf("first update reset=%v full=%q", reset, full)
	}
	full, reset, seg := a.Update("明天继续")
	if !reset {
		t.Fatal("expected reset")
	}
	if seg.Text != "今天天气真好我们一起出去玩" {
		t.Fatalf("wrong segment: %q", seg.Text)
	}
	if full != "今天天气真好我们一起出去玩明天继续" {
		t.Fatalf("wrong full: %q", full)
	}
}

func TestSegmentResetAggregatorIgnoresCorrectionPrefix(t *testing.T) {
	var a SegmentResetAggregator
	a.Update("中华人民共和国")
	_, reset, _ := a.Update("中华人民")
	if reset {
		t.Fatal("prefix correction must not reset")
	}
}
