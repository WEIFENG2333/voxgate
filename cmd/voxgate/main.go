package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/WEIFENG2333/voxgate/internal/config"
)

const (
	version = "0.2.7"
	repo    = "WEIFENG2333/voxgate"
)

type globalFlags struct {
	configPath     string
	credentialPath string
	logLevel       string
	quiet          bool
	jsonLogs       bool
	traceASRPath   string
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
		return transcribe(rest[1:], cfg, g)
	case "serve":
		return serve(rest[1:], cfg, g)
	case "doctor":
		return doctor(cfg)
	case "auth":
		return auth(cfg)
	case "version", "--version", "-V":
		return versionCmd(rest[1:])
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		printErr("invalid_command", fmt.Errorf("unknown command %q", rest[0]))
		return 2
	}
}

func openTraceWriter(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	return os.Create(config.ExpandPath(path))
}

func printErr(code string, err error) {
	fmt.Fprintf(os.Stderr, "Error [%s]: %s\n", code, err)
	if code == "asr_error" && strings.Contains(err.Error(), "context deadline exceeded") {
		fmt.Fprintln(os.Stderr, "Hint: retry with --trace-asr <file> to capture raw upstream WebSocket frames for debugging.")
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`voxgate - OpenAI-compatible CLI for speech transcription research

Usage:
  voxgate [global flags] transcribe <file|->
  voxgate [global flags] serve
  voxgate [global flags] doctor
  voxgate [global flags] auth
  voxgate version [--check]

Global flags:
  --config <file>             YAML config file
  --credential-path <file>    credential cache path
  --log-level <level>         error|warn|info|debug
  -v                          debug logging
  -q, --quiet                 suppress non-error logs
  --json-logs                 JSON logs
  --trace-asr <file>          write raw upstream ASR WebSocket frames as NDJSON

Examples:
  voxgate transcribe sample.wav
  voxgate transcribe sample.mp3 --format json
  voxgate transcribe sample.m4a --format srt -o out.srt
  cat sample.wav | voxgate transcribe - --stream
  voxgate serve --host 127.0.0.1 --port 8080
`))
}
