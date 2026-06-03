//go:build opus

package audio

import (
	"fmt"

	"gopkg.in/hraban/opus.v2"
)

type OpusEncoder struct {
	enc *opus.Encoder
}

func NewOpusEncoder() (*OpusEncoder, error) {
	enc, err := opus.NewEncoder(SampleRate, Channels, opus.AppAudio)
	if err != nil {
		return nil, err
	}
	return &OpusEncoder{enc: enc}, nil
}

func (e *OpusEncoder) EncodePCMFrame(pcm []byte) ([]byte, error) {
	if len(pcm) != BytesPerFrame {
		return nil, fmt.Errorf("pcm frame must be %d bytes, got %d", BytesPerFrame, len(pcm))
	}
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(pcm[2*i]) | int16(pcm[2*i+1])<<8
	}
	out := make([]byte, 4000)
	n, err := e.enc.Encode(samples, out)
	if err != nil {
		return nil, err
	}
	return out[:n], nil
}

func (e *OpusEncoder) Close() error { return nil }
