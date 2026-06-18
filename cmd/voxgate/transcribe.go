package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
	format := fs.String("format", "", "text|json|verbose_json|srt|vtt|ndjson|protocol")
	fs.StringVar(format, "f", "", "text|json|verbose_json|srt|vtt|ndjson|protocol")
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
	realtime := fs.Bool("realtime", false, "pace file input at realtime speed")
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
		if chosen != output.Protocol && !output.ValidStreamFormat(chosen) {
			printErr("invalid_format", fmt.Errorf("stream format %q is unsupported; use text, json, verbose_json, ndjson, or protocol", chosen))
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
	signalCtx, cancelSignal := context.WithCancel(context.Background())
	defer cancelSignal()
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		cancelSignal()
		<-sigCh
		os.Exit(130)
	}()
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
	}
	switch {
	case chosen == output.Protocol && traceWriter != nil:
		svc.Config.TraceWriter = asr.NewSynchronizedWriter(io.MultiWriter(traceWriter, asr.NewProtocolTraceWriter(w)))
	case chosen == output.Protocol:
		svc.Config.TraceWriter = asr.NewSynchronizedWriter(asr.NewProtocolTraceWriter(w))
	case traceWriter != nil:
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
		Realtime:           *realtime && !liveInput,
	})
	opts.RequestTimeout = liveRequestTimeout(opts.RequestTimeout, liveInput, requestTimeoutSet)
	if *stream {
		streamCtx := signalCtx
		if liveInput {
			streamCtx = ctx
			if err := reportHotwordsBestEffort(signalCtx, svc); err != nil {
				fmt.Fprintf(os.Stderr, "Warning [hotwords_warning]: %v\n", err)
			}
			events, err := streamEvents(streamCtx, signalCtx, svc, fs.Arg(0), *inputFormat, *sampleRate, opts, !*noChunk, true)
			if err != nil {
				printErr(streamErrorCode(err), err)
				return streamErrorExitCode(err)
			}
			return writeStreamEvents(w, chosen, events, display)
		}
		src, err := svc.Open(streamCtx, fs.Arg(0), *inputFormat, *sampleRate)
		if err != nil {
			printErr("audio_error", err)
			return 5
		}
		if err := reportHotwordsBestEffort(signalCtx, svc); err != nil {
			fmt.Fprintf(os.Stderr, "Warning [hotwords_warning]: %v\n", err)
		}
		events, err := svc.Stream(streamCtx, src, opts, !*noChunk)
		if err != nil {
			printErr("asr_error", err)
			return 1
		}
		return writeStreamEvents(w, chosen, events, display)
	}
	src, err := svc.Open(ctx, fs.Arg(0), *inputFormat, *sampleRate)
	if err != nil {
		printErr("audio_error", err)
		return 5
	}
	if err := reportHotwordsBestEffort(signalCtx, svc); err != nil {
		fmt.Fprintf(os.Stderr, "Warning [hotwords_warning]: %v\n", err)
	}
	result, err := svc.Transcribe(signalCtx, src, opts, !*noChunk)
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

func reportHotwordsBestEffort(ctx context.Context, svc transcription.Service) error {
	if len(svc.Config.Hotwords) == 0 {
		return nil
	}
	reportCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return svc.ReportHotwords(reportCtx)
}

func streamEvents(ctx, stopCtx context.Context, svc transcription.Service, path, inputFormat string, sampleRate int, opts asr.Options, allowChunking, liveInput bool) (<-chan asr.Event, error) {
	if liveInput {
		if sampleRate != 0 && sampleRate != audio.SampleRate {
			return nil, errLiveStdinSampleRate
		}
		src := audio.NewLiveSource()
		go func() {
			<-stopCtx.Done()
			src.CloseWrite()
		}()
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
	if format == output.Protocol {
		return drainProtocolStreamEvents(events)
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

func drainProtocolStreamEvents(events <-chan asr.Event) int {
	for ev := range events {
		if ev.Type == asr.EventError && ev.Error != nil {
			printErr(ev.Error.Code, fmt.Errorf("%s", ev.Error.Message))
			return 1
		}
	}
	return 0
}

type textStreamDisplay struct {
	Interactive bool
	Width       int
}

// writeTextStreamEvents renders the cumulative stream as readable text: on an
// interactive terminal the growing full text is shown live and rewritten in
// place, then committed as a final line on done; when piped, only the final
// full text is written.
func writeTextStreamEvents(w io.Writer, events <-chan asr.Event, display textStreamDisplay) int {
	lineOpen := false
	// commitLine clears any live line and prints text as a finished line.
	commitLine := func(text string) int {
		if display.Interactive && lineOpen {
			if err := clearLine(w); err != nil {
				printErr("format_error", err)
				return 1
			}
			lineOpen = false
		}
		if text != "" {
			if _, err := fmt.Fprintln(w, text); err != nil {
				printErr("format_error", err)
				return 1
			}
		}
		return 0
	}
	for ev := range events {
		if ev.Type == asr.EventError && ev.Error != nil {
			if display.Interactive && lineOpen {
				_ = clearLine(w)
			}
			printErr(ev.Error.Code, fmt.Errorf("%s", ev.Error.Message))
			return 1
		}
		switch ev.Type {
		case asr.EventTranscriptPartial:
			if !display.Interactive {
				continue
			}
			if err := display.writePreview(w, ev.Text); err != nil {
				printErr("format_error", err)
				return 1
			}
			lineOpen = true
		case asr.EventTranscriptDone:
			// The cumulative full text becomes the final committed line.
			if rc := commitLine(ev.Text); rc != 0 {
				return rc
			}
		}
	}
	if display.Interactive && lineOpen {
		if err := clearLine(w); err != nil {
			printErr("format_error", err)
			return 1
		}
	}
	return 0
}

func (d textStreamDisplay) writePreview(w io.Writer, text string) error {
	if err := clearLine(w); err != nil {
		return err
	}
	_, err := fmt.Fprint(w, d.preview(text))
	return err
}

func clearLine(w io.Writer) error {
	_, err := fmt.Fprint(w, "\r\033[2K")
	return err
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
