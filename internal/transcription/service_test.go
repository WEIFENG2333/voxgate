package transcription

import (
	"testing"
	"time"

	"github.com/WEIFENG2333/voxgate/internal/config"
)

func TestNewAppliesRuntimeDefaults(t *testing.T) {
	svc := New(Config{})
	if !svc.Config.EnablePunctuation || !svc.Config.EnableThreePass || !svc.Config.EnableTwoPass {
		t.Fatalf("ASR feature defaults not enabled: %+v", svc.Config)
	}
	if svc.Config.RequestTimeout != config.DefaultServerRequestTimeout {
		t.Fatalf("timeout = %v, want %v", svc.Config.RequestTimeout, config.DefaultServerRequestTimeout)
	}
}

func TestOptionsApplyRequestOverrides(t *testing.T) {
	svc := New(Config{EnablePunctuation: true, EnableThreePass: true, EnableTwoPass: true, RequestTimeout: 2 * time.Minute})
	opts := svc.Options(OptionInput{
		Language:           "en",
		Prompt:             "names",
		DisablePunctuation: true,
		DisableThreePass:   true,
		RequestTimeout:     30 * time.Second,
		Realtime:           true,
	})
	if opts.Language != "en" || opts.Prompt != "names" {
		t.Fatalf("bad language/prompt: %+v", opts)
	}
	if opts.EnablePunctuation || opts.EnableThreePass || !opts.EnableTwoPass {
		t.Fatalf("bad feature flags: %+v", opts)
	}
	if opts.RequestTimeout != 30*time.Second || !opts.Realtime {
		t.Fatalf("bad runtime options: %+v", opts)
	}
}

func TestFromAppConfig(t *testing.T) {
	cfg := config.Default()
	cfg.CredentialPath = "creds.json"
	cfg.ASR.UserAgent = "ua"
	cfg.Server.RequestTimeout = "3m"
	svc := FromAppConfig(cfg)
	if svc.Config.CredentialPath != "creds.json" || svc.Config.UserAgent != "ua" {
		t.Fatalf("bad config mapping: %+v", svc.Config)
	}
	if svc.Config.RequestTimeout != 3*time.Minute {
		t.Fatalf("timeout = %v, want 3m", svc.Config.RequestTimeout)
	}
}
