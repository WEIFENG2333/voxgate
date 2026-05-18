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
	t.Setenv("IME_ASR_CREDENTIAL_PATH", "env.json")
	t.Setenv("IME_ASR_SERVER_PORT", "7777")
	t.Setenv("IME_ASR_SERVER_MAX_CONCURRENCY", "9")
	t.Setenv("IME_ASR_SERVER_REQUEST_TIMEOUT", "90s")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.CredentialPath != "env.json" || c.Server.Port != 7777 || c.Server.MaxConcurrency != 9 || c.Server.RequestTimeout != "90s" {
		t.Fatalf("bad config: %+v", c)
	}
}

func TestServerRequestTimeout(t *testing.T) {
	c := Default()
	c.Server.RequestTimeout = "2m"
	if got := ServerRequestTimeout(c); got != 2*time.Minute {
		t.Fatalf("timeout = %v, want 2m", got)
	}
	c.Server.RequestTimeout = "not-a-duration"
	if got := ServerRequestTimeout(c); got != 60*time.Second {
		t.Fatalf("bad timeout fallback = %v, want 60s", got)
	}
}
