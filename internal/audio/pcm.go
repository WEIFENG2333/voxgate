package audio

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"gopkg.in/hraban/opus.v2"
)

const (
	SampleRate      = 16000
	Channels        = 1
	FrameDurationMS = 20
	BytesPerFrame   = SampleRate * FrameDurationMS / 1000 * 2
)

type Source struct {
	data   []byte
	offset int
}

func NewSourceFromPCM(pcm []byte) *Source {
	return &Source{data: pcm}
}

func Open(ctx context.Context, path string, inputFormat string, sampleRate int) (*Source, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		switch inputFormat {
		case "pcm16", "raw":
			if sampleRate != 0 && sampleRate != SampleRate {
				return ConvertBytes(ctx, data, sampleRate)
			}
			return NewSourceFromPCM(data), nil
		case "", "wav":
			return ConvertBytes(ctx, data, SampleRate)
		default:
			return nil, fmt.Errorf("unsupported stdin input format %q", inputFormat)
		}
	}
	return ConvertFile(ctx, path)
}

func ConvertFile(ctx context.Context, path string) (*Source, error) {
	cmd := exec.CommandContext(ctx, ffmpegPath(), "-hide_banner", "-loglevel", "error", "-i", path, "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("ffmpeg failed: %s", string(ee.Stderr))
		}
		return nil, err
	}
	return NewSourceFromPCM(out), nil
}

func ConvertBytes(ctx context.Context, data []byte, sourceRate int) (*Source, error) {
	args := []string{"-hide_banner", "-loglevel", "error"}
	if sourceRate > 0 && sourceRate != SampleRate {
		args = append(args, "-f", "s16le", "-ac", "1", "-ar", fmt.Sprint(sourceRate))
	}
	args = append(args, "-i", "pipe:0", "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1")
	cmd := exec.CommandContext(ctx, ffmpegPath(), args...)
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("ffmpeg failed: %s", string(ee.Stderr))
		}
		return nil, err
	}
	return NewSourceFromPCM(out), nil
}

func ffmpegPath() string {
	if path := os.Getenv("VOXGATE_FFMPEG"); path != "" {
		return path
	}
	return "ffmpeg"
}

func (s *Source) NextFrame() ([]byte, bool, error) {
	if s.offset >= len(s.data) {
		return nil, false, nil
	}
	end := s.offset + BytesPerFrame
	frame := make([]byte, BytesPerFrame)
	if end <= len(s.data) {
		copy(frame, s.data[s.offset:end])
		s.offset = end
	} else {
		copy(frame, s.data[s.offset:])
		s.offset = len(s.data)
	}
	return frame, true, nil
}

func (s *Source) Duration() time.Duration {
	samples := len(s.data) / 2
	return time.Duration(samples) * time.Second / SampleRate
}

func (s *Source) Close() error { return nil }

func (s *Source) Chunks(max time.Duration) []*Source {
	if max <= 0 || s.Duration() <= max {
		return []*Source{NewSourceFromPCM(s.data)}
	}
	bytesPerChunk := int(max.Seconds()) * SampleRate * 2
	if bytesPerChunk <= 0 {
		return []*Source{NewSourceFromPCM(s.data)}
	}
	bytesPerChunk -= bytesPerChunk % BytesPerFrame
	var chunks []*Source
	for off := 0; off < len(s.data); off += bytesPerChunk {
		end := off + bytesPerChunk
		if end > len(s.data) {
			end = len(s.data)
		}
		buf := make([]byte, end-off)
		copy(buf, s.data[off:end])
		chunks = append(chunks, NewSourceFromPCM(buf))
	}
	return chunks
}

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
