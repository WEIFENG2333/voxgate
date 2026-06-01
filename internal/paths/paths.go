package paths

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var windowsEnvPattern = regexp.MustCompile(`%([^%]+)%`)

// Expand expands common user-facing path forms across platforms.
// It supports ~/ and ~\, POSIX-style environment variables, and Windows
// %NAME% variables so config files can be shared more easily.
func Expand(path string) string {
	if path == "" {
		return path
	}
	path = expandHome(path)
	path = os.ExpandEnv(path)
	path = windowsEnvPattern.ReplaceAllStringFunc(path, func(match string) string {
		name := strings.Trim(match, "%")
		if value := os.Getenv(name); value != "" {
			return value
		}
		return match
	})
	return path
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	rest := strings.ReplaceAll(path[2:], `\`, string(filepath.Separator))
	return filepath.Join(home, rest)
}
