package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/WEIFENG2333/ime-asr/internal/asr"
	"github.com/WEIFENG2333/ime-asr/internal/audio"
	"github.com/WEIFENG2333/ime-asr/internal/config"
	"github.com/WEIFENG2333/ime-asr/internal/output"
	"github.com/WEIFENG2333/ime-asr/internal/server"
	"github.com/WEIFENG2333/ime-asr/internal/transcriber"
)

const version = "0.1.0"

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
		cfg.CredentialPath = g.credentialPath
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
		return serve(rest[1:], cfg)
	case "doctor":
		return doctor(cfg)
	case "auth":
		return auth(cfg)
	case "version", "--version", "-V":
		fmt.Println("ime-asr", version)
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
		case "--config":
			i++
			if i >= len(args) {
				return g, nil, fmt.Errorf("--config needs a value")
			}
			g.configPath = args[i]
		case "--credential-path":
			i++
			if i >= len(args) {
				return g, nil, fmt.Errorf("--credential-path needs a value")
			}
			g.credentialPath = args[i]
		case "--log-level":
			i++
			if i >= len(args) {
				return g, nil, fmt.Errorf("--log-level needs a value")
			}
			g.logLevel = args[i]
		case "-v":
			g.logLevel = "debug"
		case "-q", "--quiet":
			g.quiet = true
		case "--json-logs":
			g.jsonLogs = true
		case "--no-color":
			g.noColor = true
		default:
			rest = append(rest, args[i:]...)
			return g, rest, nil
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
	inputFormat := fs.String("input-format", "wav", "stdin format: pcm16|wav|raw")
	sampleRate := fs.Int("sample-rate", audio.SampleRate, "raw PCM sample rate")
	requestTimeout := fs.Duration("request-timeout", 60*time.Second, "request timeout")
	realtime := fs.Bool("realtime", false, "send audio at realtime speed")
	noChunk := fs.Bool("no-chunk", false, "disable long-file chunking")
	chunkDuration := fs.Duration("chunk-duration", transcriber.DefaultChunkDuration, "long-file chunk duration")
	if err := fs.Parse(reorderTranscribeArgs(args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		printErr("invalid_args", fmt.Errorf("usage: ime-asr transcribe <file|->"))
		return 2
	}
	stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))
	chosen := *format
	if chosen == "" {
		chosen = output.DefaultFormat(*stream, stdoutTTY)
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
	src, err := audio.Open(ctx, fs.Arg(0), *inputFormat, *sampleRate)
	if err != nil {
		printErr("audio_error", err)
		return 5
	}
	opts := asr.DefaultOptions()
	opts.Language = *language
	opts.Prompt = *prompt
	opts.EnablePunctuation = cfg.ASR.EnablePunctuation && !*noPunc
	opts.EnableThreePass = cfg.ASR.EnableThreePass && !*disableThreePass
	opts.EnableTwoPass = cfg.ASR.EnableTwoPass
	opts.RequestTimeout = *requestTimeout
	opts.Realtime = *realtime
	runner := transcriber.Runner{Config: transcriber.Config{CredentialPath: cfg.CredentialPath, UserAgent: cfg.ASR.UserAgent, ChunkDuration: *chunkDuration}}
	if *stream {
		events, err := runner.Stream(ctx, src, opts)
		if err != nil {
			printErr("asr_error", err)
			return 1
		}
		for ev := range events {
			if ev.Type == asr.EventError && ev.Error != nil {
				printErr(ev.Error.Code, fmt.Errorf("%s", ev.Error.Message))
				return 1
			}
			_ = output.WriteEvent(w, chosen, ev)
			if chosen == output.Text && ev.Type == asr.EventTranscriptDelta {
				_, _ = fmt.Fprint(w, "\r")
			}
		}
		if chosen == output.Text {
			_, _ = fmt.Fprintln(w)
		}
		return 0
	}
	result, err := runner.Transcribe(ctx, src, opts, !*noChunk)
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

func serve(args []string, cfg config.Config) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	host := fs.String("host", cfg.Server.Host, "host")
	port := fs.Int("port", cfg.Server.Port, "port")
	authToken := fs.String("auth-token", cfg.Server.AuthToken, "optional bearer token")
	maxConc := fs.Int("max-concurrency", cfg.Server.MaxConcurrency, "max concurrent requests")
	timeout := fs.Duration("request-timeout", 60*time.Second, "request timeout")
	realtime := fs.Bool("enable-realtime", false, "enable /v1/realtime")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	srv := server.New(server.Config{
		Host: *host, Port: *port, AuthToken: *authToken, MaxConcurrency: *maxConc, RequestTimeout: *timeout,
		CredentialPath: cfg.CredentialPath, EnableRealtime: *realtime, EnablePunctuation: cfg.ASR.EnablePunctuation,
		EnableThreePass: cfg.ASR.EnableThreePass, EnableTwoPass: cfg.ASR.EnableTwoPass, UserAgent: cfg.ASR.UserAgent,
	})
	fmt.Fprintf(os.Stderr, "ime-asr serving http://%s\n", srv.Addr())
	fmt.Fprintf(os.Stderr, "endpoints: http://%s/v1/audio/transcriptions http://%s/v1/models http://%s/health\n", srv.Addr(), srv.Addr(), srv.Addr())
	if err := http.ListenAndServe(srv.Addr(), srv.Handler()); err != nil {
		printErr("server_error", err)
		return 1
	}
	return 0
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
	_, err := exec.LookPath("ffmpeg")
	check("ffmpeg", err)
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
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`ime-asr - OpenAI-compatible CLI for IME ASR research

Usage:
  ime-asr [global flags] transcribe <file|->
  ime-asr [global flags] serve
  ime-asr [global flags] doctor
  ime-asr [global flags] auth
  ime-asr version

Global flags:
  --config <file>             YAML config file
  --credential-path <file>    credential cache path
  --log-level <level>         error|warn|info|debug
  -v                          debug logging
  -q, --quiet                 suppress non-error logs
  --no-color                  disable color
  --json-logs                 JSON logs

Examples:
  ime-asr transcribe sample.wav
  ime-asr transcribe sample.mp3 --format json
  ime-asr transcribe sample.m4a --format srt -o out.srt
  cat sample.wav | ime-asr transcribe - --stream
  ime-asr serve --host 127.0.0.1 --port 8080
`))
}
