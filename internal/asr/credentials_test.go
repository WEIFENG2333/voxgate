package asr

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultCredentialPathUsesUserConfigDir(t *testing.T) {
	dir := t.TempDir()
	var configDir string
	switch runtime.GOOS {
	case "windows":
		configDir = filepath.Join(dir, "AppData", "Roaming")
		t.Setenv("AppData", configDir)
	case "darwin":
		home := filepath.Join(dir, "home")
		t.Setenv("HOME", home)
		configDir = filepath.Join(home, "Library", "Application Support")
	default:
		configDir = filepath.Join(dir, "config")
		t.Setenv("XDG_CONFIG_HOME", configDir)
	}

	want := filepath.Join(configDir, "voxgate", "credentials.json")
	if got := DefaultCredentialPath(); got != want {
		t.Fatalf("credential path = %q, want %q", got, want)
	}
}
