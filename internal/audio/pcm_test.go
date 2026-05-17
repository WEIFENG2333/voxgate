package audio

import "testing"

func TestSourceFramesPadLastFrame(t *testing.T) {
	src := NewSourceFromPCM([]byte{1, 2, 3})
	frame, ok, err := src.NextFrame()
	if err != nil || !ok {
		t.Fatalf("NextFrame err=%v ok=%v", err, ok)
	}
	if len(frame) != BytesPerFrame || frame[0] != 1 || frame[2] != 3 {
		t.Fatalf("bad frame")
	}
	_, ok, _ = src.NextFrame()
	if ok {
		t.Fatal("expected eof")
	}
}

func TestOpusEncoderCompressesSilence(t *testing.T) {
	enc, err := NewOpusEncoder()
	if err != nil {
		t.Skipf("libopus unavailable: %v", err)
	}
	out, err := enc.EncodePCMFrame(make([]byte, BytesPerFrame))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 || len(out) >= BytesPerFrame {
		t.Fatalf("unexpected opus size: %d", len(out))
	}
}
