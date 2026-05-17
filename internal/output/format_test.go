package output

import "testing"

func TestDefaultFormat(t *testing.T) {
	if DefaultFormat(false, true) != Text {
		t.Fatal("tty should default text")
	}
	if DefaultFormat(false, false) != JSON {
		t.Fatal("pipe should default json")
	}
	if DefaultFormat(true, true) != NDJSON {
		t.Fatal("stream should default ndjson")
	}
}

func TestFormatTimestamp(t *testing.T) {
	if got := FormatTimestamp(3661.234, ","); got != "01:01:01,234" {
		t.Fatalf("got %s", got)
	}
	if got := FormatTimestamp(1.2, "."); got != "00:00:01.200" {
		t.Fatalf("got %s", got)
	}
}
