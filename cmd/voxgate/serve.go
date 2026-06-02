package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/config"
	"github.com/WEIFENG2333/voxgate/internal/server"
	"github.com/WEIFENG2333/voxgate/internal/transcription"
)

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

	traceWriter, err := openTraceWriter(g.traceASRPath)
	if err != nil {
		printErr("trace_error", err)
		return 1
	}
	if traceWriter != nil {
		defer traceWriter.Close()
	}

	srv := server.New(server.Config{
		Host:              *host,
		Port:              *port,
		AuthToken:         *authToken,
		MaxConcurrency:    *maxConc,
		RequestTimeout:    *timeout,
		CredentialPath:    cfg.CredentialPath,
		EnablePunctuation: cfg.ASR.EnablePunctuation,
		EnableThreePass:   cfg.ASR.EnableThreePass,
		EnableTwoPass:     cfg.ASR.EnableTwoPass,
		UserAgent:         cfg.ASR.UserAgent,
		LogLevel:          cfg.LogLevel,
		JSONLogs:          g.jsonLogs,
		Quiet:             g.quiet,
		TraceWriter:       asr.NewSynchronizedWriter(traceWriter),
	})
	if !g.quiet {
		logStartup(g, srv.Addr())
	}
	transcription.FromAppConfig(cfg).ReportHotwordsAsync(context.Background())
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
