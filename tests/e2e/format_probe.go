//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
)

func main() {
	audioPath := flag.String("audio", "tests/audio/zh_clean_6s.wav", "audio file")
	credentialPath := flag.String("credential-path", "", "credential cache path")
	formats := flag.String("formats", "raw,pcm,wav,aac,acc,opus,speech_opus", "comma-separated StartSession audio_info.format values")
	wire := flag.String("wire", "pcm", "wire audio encoding: pcm|opus")
	timeout := flag.Duration("timeout", 90*time.Second, "request timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *credentialPath == "" {
		*credentialPath = fmt.Sprintf("%s/voxgate-format-probe-creds.json", os.TempDir())
	}
	for _, format := range split(*formats) {
		res := probe(ctx, *audioPath, *credentialPath, format, *wire, *timeout)
		_ = json.NewEncoder(os.Stdout).Encode(res)
	}
}

type probeResult struct {
	Format string `json:"format"`
	Wire   string `json:"wire"`
	OK     bool   `json:"ok"`
	Text   string `json:"text,omitempty"`
	Error  string `json:"error,omitempty"`
}

func probe(ctx context.Context, audioPath, credentialPath, format, wire string, timeout time.Duration) probeResult {
	src, err := audio.ConvertFile(ctx, audioPath)
	if err != nil {
		return probeResult{Format: format, Wire: wire, Error: err.Error()}
	}
	defer src.Close()

	enc, err := encoder(wire)
	if err != nil {
		return probeResult{Format: format, Wire: wire, Error: err.Error()}
	}
	client := asr.Client{Config: asr.ClientConfig{CredentialPath: credentialPath, AudioFormat: format}}
	events, err := client.Transcribe(ctx, src, enc, asr.Options{
		EnablePunctuation: true,
		EnableThreePass:   true,
		EnableTwoPass:     true,
		Language:          "zh",
		RequestTimeout:    timeout,
	})
	if err != nil {
		return probeResult{Format: format, Wire: wire, Error: err.Error()}
	}
	var text string
	for ev := range events {
		if ev.Type == asr.EventError && ev.Error != nil {
			return probeResult{Format: format, Wire: wire, Error: ev.Error.Message}
		}
		if ev.Type == asr.EventTranscriptDone {
			text = ev.Text
		}
	}
	return probeResult{Format: format, Wire: wire, OK: strings.TrimSpace(text) != "", Text: text}
}

func encoder(wire string) (asr.PCMFrameEncoder, error) {
	switch strings.ToLower(strings.TrimSpace(wire)) {
	case "pcm", "raw":
		return audio.NewPCMEncoder(), nil
	case "opus", "speech_opus":
		return audio.NewOpusEncoder()
	default:
		return nil, fmt.Errorf("unsupported wire encoding %q", wire)
	}
}

func split(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
