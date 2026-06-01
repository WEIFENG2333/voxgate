package server

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

type logLevel int

const (
	logDebug logLevel = iota
	logInfo
	logWarn
	logError
	logOff
)

type logger struct {
	mu       sync.Mutex
	minLevel logLevel
	json     bool
}

func newLogger(level string, jsonLogs, quiet bool) *logger {
	minLevel := parseLogLevel(level)
	if quiet && minLevel < logError {
		minLevel = logError
	}
	return &logger{minLevel: minLevel, json: jsonLogs}
}

func parseLogLevel(level string) logLevel {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return logDebug
	case "", "info":
		return logInfo
	case "warn", "warning":
		return logWarn
	case "error":
		return logError
	case "off", "none":
		return logOff
	default:
		return logInfo
	}
}

func (l *logger) Debug(msg string, fields ...any) { l.write(logDebug, msg, fields...) }
func (l *logger) Info(msg string, fields ...any)  { l.write(logInfo, msg, fields...) }
func (l *logger) Warn(msg string, fields ...any)  { l.write(logWarn, msg, fields...) }
func (l *logger) Error(msg string, fields ...any) { l.write(logError, msg, fields...) }

func (l *logger) write(level logLevel, msg string, fields ...any) {
	if l == nil || level < l.minLevel || l.minLevel == logOff {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.json {
		record := map[string]any{
			"time":  time.Now().Format(time.RFC3339),
			"level": level.String(),
			"msg":   msg,
		}
		for i := 0; i+1 < len(fields); i += 2 {
			key, ok := fields[i].(string)
			if ok && key != "" {
				record[key] = fields[i+1]
			}
		}
		_ = json.NewEncoder(os.Stderr).Encode(record)
		return
	}
	fmt.Fprintf(os.Stderr, "%s level=%s msg=%q", time.Now().Format(time.RFC3339), level.String(), msg)
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if ok && key != "" {
			fmt.Fprintf(os.Stderr, " %s=%v", key, fields[i+1])
		}
	}
	fmt.Fprintln(os.Stderr)
}

func (l logLevel) String() string {
	switch l {
	case logDebug:
		return "debug"
	case logInfo:
		return "info"
	case logWarn:
		return "warn"
	case logError:
		return "error"
	default:
		return "off"
	}
}
