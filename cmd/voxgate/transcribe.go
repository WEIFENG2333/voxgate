package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
	"github.com/WEIFENG2333/voxgate/internal/config"
	"github.com/WEIFENG2333/voxgate/internal/output"
	"github.com/WEIFENG2333/voxgate/internal/transcriber"
	"github.com/WEIFENG2333/voxgate/internal/transcription"
)

func transcribe(args []string, cfg config.Config, g globalFlags) int {
	fs := flag.NewFlagSet("transcribe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	format := fs.String("format", "", "text|json|verbose_json|srt|vtt|ndjson")
	fs.StringVar(format, "f", "", "text|json|verbose_json|srt|vtt|ndjson")
	stream := fs.Bool("stream", false, "stream events")
	language := fs.String("language", "zh", "language hint")
	fs.StringVar(language, "l", "zh", "language hint")
	prompt := fs.String("prompt", "", "prompt/hotwords hint")
	hotwords := fs.String("hotwords", "", "comma-separated hotwords to boost recognition")
	noPunc := fs.Bool("no-punctuation", false, "disable punctuation")
	disableThreePass := fs.Bool("disable-three-pass", false, "disable third pass")
	outPath := fs.String("output", "", "write output to file")
	fs.StringVar(outPath, "o", "", "write output to file")
	inputFormat := fs.String("input-format", "wav", "stdin format: pcm16|wav|raw; pcm16/raw + --stream is live")
	sampleRate := fs.Int("sample-rate", audio.SampleRate, "raw PCM sample rate")
	requestTimeout := fs.Duration("request-timeout", config.DefaultServerRequestTimeout, "request timeout")
	noChunk := fs.Bool("no-chunk", false, "disable long-file chunking")
	chunkDuration := fs.Duration("chunk-duration", transcriber.DefaultChunkDuration, "long-file chunk duration")
	if err := fs.Parse(reorderTranscribeArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		printErr("invalid_args", fmt.Errorf("usage: voxgate transcribe <file|->"))
		return 2
	}
	stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))
	display := textStreamDisplay{Interactive: stdoutTTY && *outPath == ""}
	if display.Interactive {
		if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			display.Width = width
		}
	}
	chosen := *format
	if chosen == "" {
		chosen = output.DefaultFormat(*stream, stdoutTTY)
	}
	if *stream {
		if !output.ValidStreamFormat(chosen) {
			printErr("invalid_format", fmt.Errorf("stream format %q is unsupported; use text, json, verbose_json, or ndjson", chosen))
			return 2
		}
	} else if !output.ValidResultFormat(chosen) {
		printErr("invalid_format", fmt.Errorf("format %q is unsupported", chosen))
		return 2
	}
	w := os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			printErr("output_error", err)
			return 1
		}
		defer f.Close()
		w = f
	}
	ctx := context.Background()
	svc := transcription.FromAppConfig(cfg)
	svc.Config.ChunkDuration = *chunkDuration
	if *hotwords != "" {
		svc.Config.Hotwords = config.SplitList(*hotwords)
	}
	traceWriter, err := openTraceWriter(g.traceASRPath)
	if err != nil {
		printErr("trace_error", err)
		return 1
	}
	if traceWriter != nil {
		defer traceWriter.Close()
		svc.Config.TraceWriter = asr.NewSynchronizedWriter(traceWriter)
	}
	liveInput := isLiveStdinStream(fs.Arg(0), *inputFormat, *stream)
	requestTimeoutSet := flagWasSet(fs, "request-timeout")
	opts := svc.Options(transcription.OptionInput{
		Language:           *language,
		Prompt:             *prompt,
		DisablePunctuation: *noPunc,
		DisableThreePass:   *disableThreePass,
		RequestTimeout:     *requestTimeout,
	})
	opts.RequestTimeout = liveRequestTimeout(opts.RequestTimeout, liveInput, requestTimeoutSet)
	waitHotwords := svc.ReportHotwordsAsync(ctx)
	defer waitHotwords()
	if *stream {
		events, err := streamEvents(ctx, svc, fs.Arg(0), *inputFormat, *sampleRate, opts, !*noChunk, liveInput)
		if err != nil {
			printErr(streamErrorCode(err), err)
			return streamErrorExitCode(err)
		}
		return writeStreamEvents(w, chosen, events, display)
	}
	src, err := svc.Open(ctx, fs.Arg(0), *inputFormat, *sampleRate)
	if err != nil {
		printErr("audio_error", err)
		return 5
	}
	result, err := svc.Transcribe(ctx, src, opts, !*noChunk)
	if err != nil {
		printErr("asr_error", err)
		return 1
	}
	if err := output.WriteResult(w, chosen, result); err != nil {
		printErr("format_error", err)
		return 1
	}
	return 0
}

var errLiveStdinSampleRate = fmt.Errorf("live stdin pcm16 requires %d Hz mono PCM; pipe ffmpeg/arecord output at 16000 Hz or omit --sample-rate", audio.SampleRate)

func isLiveStdinStream(path, inputFormat string, stream bool) bool {
	return stream && path == "-" && (inputFormat == "pcm16" || inputFormat == "raw")
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	seen := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			seen = true
		}
	})
	return seen
}

func liveRequestTimeout(timeout time.Duration, liveInput, timeoutSet bool) time.Duration {
	if liveInput && !timeoutSet {
		return 0
	}
	return timeout
}

func streamEvents(ctx context.Context, svc transcription.Service, path, inputFormat string, sampleRate int, opts asr.Options, allowChunking, liveInput bool) (<-chan asr.Event, error) {
	if liveInput {
		if sampleRate != 0 && sampleRate != audio.SampleRate {
			return nil, errLiveStdinSampleRate
		}
		src := audio.NewLiveSource()
		go copyStdinPCM(src)
		return svc.StreamFrames(ctx, src, opts)
	}
	src, err := svc.Open(ctx, path, inputFormat, sampleRate)
	if err != nil {
		return nil, err
	}
	return svc.Stream(ctx, src, opts, allowChunking)
}

type pcmWriter interface {
	WritePCM([]byte) error
	CloseWrite()
}

func copyStdinPCM(src pcmWriter) {
	defer src.CloseWrite()
	buf := make([]byte, audio.BytesPerFrame)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if writeErr := src.WritePCM(buf[:n]); writeErr != nil {
				return
			}
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			return
		}
	}
}

func writeStreamEvents(w io.Writer, format string, events <-chan asr.Event, display textStreamDisplay) int {
	if format == output.Text {
		return writeTextStreamEvents(w, events, display)
	}
	for ev := range events {
		if ev.Type == asr.EventError && ev.Error != nil {
			printErr(ev.Error.Code, fmt.Errorf("%s", ev.Error.Message))
			return 1
		}
		if err := output.WriteEvent(w, format, ev); err != nil {
			printErr("format_error", err)
			return 1
		}
	}
	return 0
}

type textStreamDisplay struct {
	Interactive bool
	Width       int
}

func writeTextStreamEvents(w io.Writer, events <-chan asr.Event, display textStreamDisplay) int {
	lineOpen := false
	for ev := range events {
		if ev.Type == asr.EventError && ev.Error != nil {
			if display.Interactive && lineOpen {
				if _, err := fmt.Fprint(w, "\r\033[2K"); err != nil {
					printErr("format_error", err)
					return 1
				}
			}
			printErr(ev.Error.Code, fmt.Errorf("%s", ev.Error.Message))
			return 1
		}
		switch ev.Type {
		case asr.EventTranscriptDelta:
			if !display.Interactive {
				continue
			}
			if _, err := fmt.Fprintf(w, "\r\033[2K%s", display.preview(ev.Text)); err != nil {
				printErr("format_error", err)
				return 1
			}
			lineOpen = true
		case asr.EventTranscriptFinal:
			if display.Interactive && lineOpen {
				if _, err := fmt.Fprint(w, "\r\033[2K"); err != nil {
					printErr("format_error", err)
					return 1
				}
				lineOpen = false
			}
			if _, err := fmt.Fprintln(w, ev.Text); err != nil {
				printErr("format_error", err)
				return 1
			}
		}
	}
	if display.Interactive && lineOpen {
		if _, err := fmt.Fprint(w, "\r\033[2K"); err != nil {
			printErr("format_error", err)
			return 1
		}
	}
	return 0
}

func (d textStreamDisplay) preview(text string) string {
	width := d.Width
	if width <= 0 {
		width = 80
	}
	if width > 2 {
		width -= 2
	}
	if displayWidth(text) <= width {
		return text
	}
	if width <= 1 {
		return ""
	}
	ellipsis := "…"
	budget := width - displayWidth(ellipsis)
	if budget <= 0 {
		return ellipsis
	}
	runes := []rune(text)
	start := len(runes)
	used := 0
	for start > 0 {
		next := runeDisplayWidth(runes[start-1])
		if used+next > budget {
			break
		}
		start--
		used += next
	}
	return ellipsis + string(runes[start:])
}

func displayWidth(text string) int {
	width := 0
	for _, r := range text {
		width += runeDisplayWidth(r)
	}
	return width
}

func runeDisplayWidth(r rune) int {
	if r < 128 {
		return 1
	}
	return 2
}

func streamErrorCode(err error) string {
	if errors.Is(err, errLiveStdinSampleRate) {
		return "audio_error"
	}
	if strings.HasPrefix(err.Error(), "unsupported stdin input format") {
		return "audio_error"
	}
	return "asr_error"
}

func streamErrorExitCode(err error) int {
	if streamErrorCode(err) == "audio_error" {
		return 5
	}
	return 1
}
