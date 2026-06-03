//go:build !opus

package audio

import "errors"

type OpusEncoder struct{}

func NewOpusEncoder() (*OpusEncoder, error) {
	return nil, errors.New("opus support is not compiled in; rebuild with -tags opus")
}

func (e *OpusEncoder) EncodePCMFrame([]byte) ([]byte, error) {
	return nil, errors.New("opus support is not compiled in; rebuild with -tags opus")
}

func (e *OpusEncoder) Close() error { return nil }
