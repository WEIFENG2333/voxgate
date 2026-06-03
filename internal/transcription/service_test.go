package transcription

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/WEIFENG2333/voxgate/internal/asr"
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
	cfg.ASR.Hotwords = []string{"Claude", "Anthropic"}
	cfg.ASR.AudioFormat = "pcm"
	cfg.ASR.WebSocketURL = "ws://127.0.0.1:9999/ws"
	cfg.Server.RequestTimeout = "3m"
	svc := FromAppConfig(cfg)
	if svc.Config.CredentialPath != "creds.json" || svc.Config.UserAgent != "ua" || svc.Config.AudioFormat != "pcm" || svc.Config.WebSocketURL != "ws://127.0.0.1:9999/ws" {
		t.Fatalf("bad config mapping: %+v", svc.Config)
	}
	if !reflect.DeepEqual(svc.Config.Hotwords, []string{"Claude", "Anthropic"}) {
		t.Fatalf("hotwords = %#v", svc.Config.Hotwords)
	}
	if svc.Config.RequestTimeout != 3*time.Minute {
		t.Fatalf("timeout = %v, want 3m", svc.Config.RequestTimeout)
	}
}

func TestReportHotwordsUsesConfiguredReporter(t *testing.T) {
	var got []string
	svc := New(Config{
		CredentialPath: testCredentialPath(t, "device-1"),
		Hotwords:       []string{" Claude ", "", "Claude", "Anthropic"},
		HotwordReporter: func(_ context.Context, words []string) error {
			got = append([]string(nil), words...)
			return nil
		},
	})
	if err := svc.ReportHotwords(context.Background()); err != nil {
		t.Fatalf("ReportHotwords returned error: %v", err)
	}
	want := []string{"Claude", "Anthropic"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reported words = %#v, want %#v", got, want)
	}
}

func TestReportHotwordsSkipsCachedWordsAndPersistsMissing(t *testing.T) {
	credPath := testCredentialPath(t, "device-1")
	cachePath := hotwordCachePath(credPath)
	if err := saveHotwordCache(cachePath, hotwordCache{DeviceID: "device-1", Words: []string{"Claude"}}); err != nil {
		t.Fatalf("saveHotwordCache: %v", err)
	}
	var got []string
	svc := New(Config{
		CredentialPath:  credPath,
		Hotwords:        []string{"Claude", "Anthropic", "VoxGate"},
		HotwordReporter: captureHotwords(&got),
	})
	if err := svc.ReportHotwords(context.Background()); err != nil {
		t.Fatalf("ReportHotwords returned error: %v", err)
	}
	wantReported := []string{"Anthropic", "VoxGate"}
	if !reflect.DeepEqual(got, wantReported) {
		t.Fatalf("reported words = %#v, want %#v", got, wantReported)
	}
	cache := loadHotwordCache(cachePath, "device-1")
	wantCache := []string{"Claude", "Anthropic", "VoxGate"}
	if !reflect.DeepEqual(cache.Words, wantCache) {
		t.Fatalf("cached words = %#v, want %#v", cache.Words, wantCache)
	}
}

func TestReportHotwordsDoesNotRepeatUploadedWords(t *testing.T) {
	var calls int
	var got []string
	svc := New(Config{
		CredentialPath: testCredentialPath(t, "device-1"),
		Hotwords:       []string{"Claude", "Anthropic"},
		HotwordReporter: func(_ context.Context, words []string) error {
			calls++
			got = append([]string(nil), words...)
			return nil
		},
	})
	if err := svc.ReportHotwords(context.Background()); err != nil {
		t.Fatalf("first ReportHotwords returned error: %v", err)
	}
	if err := svc.ReportHotwords(context.Background()); err != nil {
		t.Fatalf("second ReportHotwords returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("reporter calls = %d, want 1", calls)
	}
	want := []string{"Claude", "Anthropic"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reported words = %#v, want %#v", got, want)
	}
}

func TestReportHotwordsUsesCacheBeforeRefreshingExpiredCredentials(t *testing.T) {
	credPath := filepath.Join(t.TempDir(), "credentials.json")
	err := asr.SaveCredentials(credPath, asr.Credentials{
		DeviceID:         "device-1",
		CDID:             "cdid",
		Token:            "expired",
		TokenUpdatedAtMS: 0,
	})
	if err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if err := saveHotwordCache(hotwordCachePath(credPath), hotwordCache{DeviceID: "device-1", Words: []string{"Claude"}}); err != nil {
		t.Fatalf("saveHotwordCache: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := New(Config{CredentialPath: credPath, Hotwords: []string{"Claude"}})
	if err := svc.ReportHotwords(ctx); err != nil {
		t.Fatalf("ReportHotwords returned error: %v", err)
	}
}

func TestReportHotwordsDoesNotCacheFailedReport(t *testing.T) {
	credPath := testCredentialPath(t, "device-1")
	reportErr := errors.New("report failed")
	svc := New(Config{
		CredentialPath: credPath,
		Hotwords:       []string{"Claude"},
		HotwordReporter: func(context.Context, []string) error {
			return reportErr
		},
	})
	if err := svc.ReportHotwords(context.Background()); !errors.Is(err, reportErr) {
		t.Fatalf("ReportHotwords error = %v, want %v", err, reportErr)
	}
	if _, err := os.Stat(hotwordCachePath(credPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache stat error = %v, want not exist", err)
	}
}

func TestReportHotwordsIgnoresCacheFromDifferentDevice(t *testing.T) {
	credPath := testCredentialPath(t, "device-new")
	cachePath := hotwordCachePath(credPath)
	if err := saveHotwordCache(cachePath, hotwordCache{DeviceID: "device-old", Words: []string{"Claude"}}); err != nil {
		t.Fatalf("saveHotwordCache: %v", err)
	}
	var got []string
	svc := New(Config{
		CredentialPath:  credPath,
		Hotwords:        []string{"Claude"},
		HotwordReporter: captureHotwords(&got),
	})
	if err := svc.ReportHotwords(context.Background()); err != nil {
		t.Fatalf("ReportHotwords returned error: %v", err)
	}
	want := []string{"Claude"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reported words = %#v, want %#v", got, want)
	}
	cache := loadHotwordCache(cachePath, "device-new")
	if cache.DeviceID != "device-new" {
		t.Fatalf("cache device = %q, want device-new", cache.DeviceID)
	}
}

func testCredentialPath(t *testing.T, deviceID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	err := asr.SaveCredentials(path, asr.Credentials{
		DeviceID:         deviceID,
		CDID:             "cdid",
		Token:            "token",
		TokenUpdatedAtMS: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	return path
}

func captureHotwords(dst *[]string) func(context.Context, []string) error {
	return func(_ context.Context, words []string) error {
		*dst = append([]string(nil), words...)
		return nil
	}
}
