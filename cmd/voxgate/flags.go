package main

import (
	"fmt"
	"strings"
)

func parseGlobal(args []string) (globalFlags, []string, error) {
	var g globalFlags
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--config", "--credential-path", "--log-level", "--trace-asr":
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
			case "--trace-asr":
				g.traceASRPath = value
			}
		case "-v":
			g.logLevel = "debug"
		case "-q", "--quiet":
			g.quiet = true
		case "--json-logs":
			g.jsonLogs = true
		default:
			rest = append(rest, a)
		}
	}
	return g, rest, nil
}

func reorderTranscribeArgs(args []string) []string {
	valueFlags := map[string]bool{
		"-f": true, "--format": true, "-l": true, "--language": true, "--prompt": true,
		"--hotwords": true,
		"-o":         true, "--output": true, "--input-format": true, "--sample-rate": true,
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
