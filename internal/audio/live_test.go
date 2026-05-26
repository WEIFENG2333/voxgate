package audio

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLiveSourcePadsFinalFrame(t *testing.T) {
	src := NewLiveSource()
	if err := src.WritePCM([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	src.CloseWrite()
	frame, ok, err := src.NextFrame()
	if err != nil || !ok {
		t.Fatalf("NextFrame err=%v ok=%v", err, ok)
	}
	if len(frame) != BytesPerFrame || frame[0] != 1 || frame[2] != 3 {
		t.Fatalf("bad frame")
	}
	_, ok, err = src.NextFrame()
	if err != nil || ok {
		t.Fatalf("expected eof, err=%v ok=%v", err, ok)
	}
}

func TestLiveSourceMaxBufferBackpressure(t *testing.T) {
	src := NewLiveSourceWithMaxBuffer(BytesPerFrame)
	if err := src.WritePCM(make([]byte, BytesPerFrame)); err != nil {
		t.Fatal(err)
	}
	wrote := make(chan error, 1)
	go func() {
		wrote <- src.WritePCM(make([]byte, BytesPerFrame))
	}()
	select {
	case err := <-wrote:
		t.Fatalf("WritePCM returned before buffer was consumed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, ok, err := src.NextFrame(); err != nil || !ok {
		t.Fatalf("NextFrame err=%v ok=%v", err, ok)
	}
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("WritePCM did not unblock after frame consumption")
	}
	src.CloseWrite()
}

func TestLiveSourceMaxBufferCloseUnblocksWriter(t *testing.T) {
	src := NewLiveSourceWithMaxBuffer(BytesPerFrame)
	if err := src.WritePCM(make([]byte, BytesPerFrame)); err != nil {
		t.Fatal(err)
	}
	wrote := make(chan error, 1)
	go func() {
		wrote <- src.WritePCM(make([]byte, BytesPerFrame))
	}()
	time.Sleep(50 * time.Millisecond)
	src.CloseWrite()
	select {
	case err := <-wrote:
		if err != ErrLiveSourceClosed {
			t.Fatalf("WritePCM err = %v, want ErrLiveSourceClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WritePCM did not unblock after close")
	}
}

func TestLiveSourceWriteContextTimeout(t *testing.T) {
	src := NewLiveSourceWithMaxBuffer(BytesPerFrame)
	if err := src.WritePCM(make([]byte, BytesPerFrame)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := src.WritePCMContext(ctx, make([]byte, BytesPerFrame))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WritePCMContext err = %v, want DeadlineExceeded", err)
	}
	src.CloseWrite()
}
