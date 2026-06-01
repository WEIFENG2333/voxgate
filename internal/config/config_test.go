package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadPriorityEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("credential_path: file.json\nserver:\n  port: 9000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOXGATE_CREDENTIAL_PATH", "env.json")
	t.Setenv("VOXGATE_SERVER_PORT", "7777")
	t.Setenv("VOXGATE_SERVER_MAX_CONCURRENCY", "9")
	t.Setenv("VOXGATE_SERVER_REQUEST_TIMEOUT", "90s")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.CredentialPath != "env.json" || c.Server.Port != 7777 || c.Server.MaxConcurrency != 9 || c.Server.RequestTimeout != "90s" {
		t.Fatalf("bad config: %+v", c)
	}
}

func TestLoadExpandsCredentialPath(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("credential_path: ~/.config/voxgate/credentials.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "voxgate", "credentials.json")
	if c.CredentialPath != want {
		t.Fatalf("credential path = %q, want %q", c.CredentialPath, want)
	}
}

func TestExpandPathSupportsWindowsStyleEnvAndHome(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	configDir := filepath.Join(dir, "config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AppData", configDir)

	tests := map[string]string{
		`~\voxgate\credentials.json`:         filepath.Join(home, "voxgate", "credentials.json"),
		`%AppData%/voxgate/credentials.json`: filepath.Join(configDir, "voxgate", "credentials.json"),
	}
	for input, want := range tests {
		if got := filepath.Clean(ExpandPath(input)); got != filepath.Clean(want) {
			t.Fatalf("ExpandPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestServerRequestTimeout(t *testing.T) {
	c := Default()
	c.Server.RequestTimeout = "2m"
	if got := ServerRequestTimeout(c); got != 2*time.Minute {
		t.Fatalf("timeout = %v, want 2m", got)
	}
	c.Server.RequestTimeout = "not-a-duration"
	if got := ServerRequestTimeout(c); got != DefaultServerRequestTimeout {
		t.Fatalf("bad timeout fallback = %v, want %v", got, DefaultServerRequestTimeout)
	}
}
