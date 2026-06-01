package paths

import (
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	tests := map[string]string{
		"~":             home,
		"~/voxgate":     filepath.Join(home, "voxgate"),
		`~\voxgate`:     filepath.Join(home, "voxgate"),
		"plain/path":    "plain/path",
		"/plain/path":   "/plain/path",
		`C:\plain\path`: `C:\plain\path`,
	}
	for input, want := range tests {
		if got := Expand(input); got != want {
			t.Fatalf("Expand(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExpandEnvironmentVariables(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	t.Setenv("VOXGATE_TEST_CONFIG", dir)
	t.Setenv("AppData", dir)

	tests := map[string]string{
		"$VOXGATE_TEST_CONFIG/voxgate":   filepath.Join(dir, "voxgate"),
		"${VOXGATE_TEST_CONFIG}/voxgate": filepath.Join(dir, "voxgate"),
		"%AppData%/voxgate":              filepath.Join(dir, "voxgate"),
	}
	for input, want := range tests {
		if got := filepath.Clean(Expand(input)); got != filepath.Clean(want) {
			t.Fatalf("Expand(%q) = %q, want %q", input, got, want)
		}
	}
}
