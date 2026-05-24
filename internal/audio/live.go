package audio

import (
	"errors"
	"sync"
	"time"
)

var ErrLiveSourceClosed = errors.New("live source is closed")

type LiveSource struct {
	mu       sync.Mutex
	cond     *sync.Cond
	buf      []byte
	closed   bool
	duration time.Duration
}

func NewLiveSource() *LiveSource {
	s := &LiveSource{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *LiveSource) WritePCM(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrLiveSourceClosed
	}
	s.buf = append(s.buf, p...)
	s.duration += time.Duration(len(p)/2) * time.Second / SampleRate
	s.cond.Signal()
	return nil
}

func (s *LiveSource) NextFrame() ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.buf) < BytesPerFrame && !s.closed {
		s.cond.Wait()
	}
	if len(s.buf) == 0 && s.closed {
		return nil, false, nil
	}
	frame := make([]byte, BytesPerFrame)
	if len(s.buf) >= BytesPerFrame {
		copy(frame, s.buf[:BytesPerFrame])
		s.buf = s.buf[BytesPerFrame:]
		return frame, true, nil
	}
	copy(frame, s.buf)
	s.buf = nil
	return frame, true, nil
}

func (s *LiveSource) CloseWrite() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *LiveSource) Duration() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.duration
}

func (s *LiveSource) Close() error {
	s.CloseWrite()
	return nil
}
