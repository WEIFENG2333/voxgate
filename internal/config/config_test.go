package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPriorityEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("credential_path: file.json\nserver:\n  port: 9000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IME_ASR_CREDENTIAL_PATH", "env.json")
	t.Setenv("IME_ASR_SERVER_PORT", "7777")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.CredentialPath != "env.json" || c.Server.Port != 7777 {
		t.Fatalf("bad config: %+v", c)
	}
}
