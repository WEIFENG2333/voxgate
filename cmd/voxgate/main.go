package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
	"github.com/WEIFENG2333/voxgate/internal/config"
	"github.com/WEIFENG2333/voxgate/internal/output"
	"github.com/WEIFENG2333/voxgate/internal/server"
	"github.com/WEIFENG2333/voxgate/internal/transcriber"
	"github.com/WEIFENG2333/voxgate/internal/transcription"
)

const version = "0.2.7"

type globalFlags struct {
	configPath     string
	credentialPath string
	logLevel       string
	quiet          bool
	jsonLogs       bool
	noColor        bool
}

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	g, rest, err := parseGlobal(args)
	if err != nil {
		printErr("invalid_args", err)
		return 2
	}
	cfg, err := config.Load(g.configPath)
	if err != nil {
		printErr("config_error", err)
		return 1
	}
	if g.credentialPath != "" {
		cfg.CredentialPath = config.ExpandPath(g.credentialPath)
	}
	if g.logLevel != "" {
		cfg.LogLevel = g.logLevel
	}
	if len(rest) == 0 {
		usage()
		return 2
	}
	switch rest[0] {
	case "transcribe":
		return transcribe(rest[1:], cfg)
	case "serve":
		return serve(rest[1:], cfg, g)
	case "doctor":
		return doctor(cfg)
	case "auth":
		return auth(cfg)
	case "version", "--version", "-V":
		fmt.Println("voxgate", version)
		return 0
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		printErr("invalid_command", fmt.Errorf("unknown command %q", rest[0]))
		return 2
	}
}

func parseGlobal(args []string) (globalFlags, []string, error) {
	var g globalFlags
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--config", "--credential-path", "--log-level":
			if i+1 >= len(args) {
				return g, nil, fmt.Errorf("%s needs a value", a)
			}
			value := args[i+1]
			i++
			switch a {
			case "--config":
				g.configPath = value
			case "--credential-path":
				g.credentialPath = value
			case "--log-level":
				g.logLevel = value
			}
		case "-v":
			g.logLevel = "debug"
		case "-q", "--quiet":
			g.quiet = true
		case "--json-logs":
			g.jsonLogs = true
		case "--no-color":
			g.noColor = true
		default:
			rest = append(rest, a)
		}
	}
	return g, rest, nil
}

func transcribe(args []string, cfg config.Config) int {
	fs := flag.NewFlagSet("transcribe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	format := fs.String("format", "", "text|json|verbose_json|srt|vtt|ndjson")
	fs.StringVar(format, "f", "", "text|json|verbose_json|srt|vtt|ndjson")
	stream := fs.Bool("stream", false, "stream events")
	language := fs.String("language", "zh", "language hint")
	fs.StringVar(language, "l", "zh", "language hint")
	prompt := fs.String("prompt", "", "prompt/hotwords hint")
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
	liveInput := isLiveStdinStream(fs.Arg(0), *inputFormat, *stream)
	opts := svc.Options(transcription.OptionInput{
		Language:           *language,
		Prompt:             *prompt,
		DisablePunctuation: *noPunc,
		DisableThreePass:   *disableThreePass,
		RequestTimeout:     *requestTimeout,
		Realtime:           *realtime && !liveInput,
	})
	if *stream {
		events, err := streamEvents(ctx, svc, fs.Arg(0), *inputFormat, *sampleRate, opts, !*noChunk, liveInput)
		if err != nil {
			printErr(streamErrorCode(err), err)
			return streamErrorExitCode(err)
		}
		return writeStreamEvents(w, chosen, events)
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

func copyStdinPCM(src *audio.LiveSource) {
	defer src.CloseWrite()
	buf := make([]byte, audio.BytesPerFrame*10)
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

func writeStreamEvents(w io.Writer, format string, events <-chan asr.Event) int {
	for ev := range events {
		if ev.Type == asr.EventError && ev.Error != nil {
			printErr(ev.Error.Code, fmt.Errorf("%s", ev.Error.Message))
			return 1
		}
		_ = output.WriteEvent(w, format, ev)
		if format == output.Text && ev.Type == asr.EventTranscriptDelta {
			_, _ = fmt.Fprint(w, "\r")
		}
	}
	if format == output.Text {
		_, _ = fmt.Fprintln(w)
	}
	return 0
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

func reorderTranscribeArgs(args []string) []string {
	valueFlags := map[string]bool{
		"-f": true, "--format": true, "-l": true, "--language": true, "--prompt": true,
		"-o": true, "--output": true, "--input-format": true, "--sample-rate": true,
		"--request-timeout": true, "--chunk-duration": true,
	}
	var flagsPart []string
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flagsPart = append(flagsPart, a)
			if valueFlags[a] && i+1 < len(args) {
				i++
				flagsPart = append(flagsPart, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flagsPart, pos...)
}

func serve(args []string, cfg config.Config, g globalFlags) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	host := fs.String("host", cfg.Server.Host, "host")
	port := fs.Int("port", cfg.Server.Port, "port")
	authToken := fs.String("auth-token", cfg.Server.AuthToken, "optional bearer token")
	maxConc := fs.Int("max-concurrency", cfg.Server.MaxConcurrency, "max concurrent requests")
	timeout := fs.Duration("request-timeout", config.ServerRequestTimeout(cfg), "request timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	srv := server.New(server.Config{
		Host: *host, Port: *port, AuthToken: *authToken, MaxConcurrency: *maxConc, RequestTimeout: *timeout,
		CredentialPath: cfg.CredentialPath, EnablePunctuation: cfg.ASR.EnablePunctuation,
		EnableThreePass: cfg.ASR.EnableThreePass, EnableTwoPass: cfg.ASR.EnableTwoPass, UserAgent: cfg.ASR.UserAgent,
	})
	if !g.quiet {
		logStartup(g, srv.Addr())
	}
	if err := http.ListenAndServe(srv.Addr(), srv.Handler()); err != nil {
		printErr("server_error", err)
		return 1
	}
	return 0
}

func logStartup(g globalFlags, addr string) {
	baseURL := "http://" + addr
	if g.jsonLogs {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{
			"level": "info",
			"msg":   "server started",
			"url":   baseURL,
			"endpoints": []string{
				baseURL + "/v1/audio/transcriptions",
				baseURL + "/v1/realtime",
				baseURL + "/v1/models",
				baseURL + "/health",
			},
		})
		return
	}
	fmt.Fprintf(os.Stderr, "voxgate serving %s\n", baseURL)
	fmt.Fprintf(os.Stderr, "endpoints: %s/v1/audio/transcriptions %s/v1/realtime %s/v1/models %s/health\n", baseURL, baseURL, baseURL, baseURL)
}

func doctor(cfg config.Config) int {
	ok := true
	check := func(name string, err error) {
		if err != nil {
			ok = false
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", name, err)
		} else {
			fmt.Fprintf(os.Stderr, "OK   %s\n", name)
		}
	}
	if path := os.Getenv("VOXGATE_FFMPEG"); path != "" {
		_, err := os.Stat(path)
		check("ffmpeg", err)
	} else {
		_, err := exec.LookPath("ffmpeg")
		check("ffmpeg", err)
	}
	var err error
	_, err = audio.NewOpusEncoder()
	check("libopus", err)
	_, err = asr.LoadCredentials(cfg.CredentialPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN credentials: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "OK   credentials\n")
	}
	if ok {
		return 0
	}
	return 1
}

func auth(cfg config.Config) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	creds, err := (asr.CredentialManager{Path: cfg.CredentialPath, UserAgent: cfg.ASR.UserAgent}).Ensure(ctx, true)
	if err != nil {
		printErr("auth_error", err)
		return 3
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"device_id": creds.DeviceID, "credential_path": cfg.CredentialPath})
	return 0
}

func printErr(code string, err error) {
	_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"error": map[string]any{"code": code, "message": err.Error(), "details": map[string]any{}}})
}

func usage() {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`voxgate - OpenAI-compatible CLI for speech transcription research

Usage:
  voxgate [global flags] transcribe <file|->
  voxgate [global flags] serve
  voxgate [global flags] doctor
  voxgate [global flags] auth
  voxgate version

Global flags:
  --config <file>             YAML config file
  --credential-path <file>    credential cache path
  --log-level <level>         error|warn|info|debug
  -v                          debug logging
  -q, --quiet                 suppress non-error logs
  --no-color                  disable color
  --json-logs                 JSON logs

Examples:
  voxgate transcribe sample.wav
  voxgate transcribe sample.mp3 --format json
  voxgate transcribe sample.m4a --format srt -o out.srt
  cat sample.wav | voxgate transcribe - --stream
  voxgate serve --host 127.0.0.1 --port 8080
`))
}
