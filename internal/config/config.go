package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/paths"
)

const DefaultServerRequestTimeout = 10 * time.Minute

type Config struct {
	CredentialPath string `yaml:"credential_path"`
	LogLevel       string `yaml:"log_level"`
	ASR            struct {
		EnablePunctuation bool     `yaml:"enable_punctuation"`
		EnableThreePass   bool     `yaml:"enable_three_pass"`
		EnableTwoPass     bool     `yaml:"enable_two_pass"`
		UserAgent         string   `yaml:"user_agent"`
		AudioFormat       string   `yaml:"audio_format"`
		Hotwords          []string `yaml:"hotwords"`
	} `yaml:"asr"`
	Server struct {
		Host           string `yaml:"host"`
		Port           int    `yaml:"port"`
		AuthToken      string `yaml:"auth_token"`
		MaxConcurrency int    `yaml:"max_concurrency"`
		RequestTimeout string `yaml:"request_timeout"`
	} `yaml:"server"`
}

func Default() Config {
	var c Config
	c.CredentialPath = asr.DefaultCredentialPath()
	c.LogLevel = "info"
	c.ASR.EnablePunctuation = true
	c.ASR.EnableThreePass = true
	c.ASR.EnableTwoPass = true
	c.ASR.UserAgent = asr.DefaultUserAgent
	c.ASR.AudioFormat = "auto"
	c.Server.Host = "127.0.0.1"
	c.Server.Port = 8080
	c.Server.MaxConcurrency = 4
	c.Server.RequestTimeout = DefaultServerRequestTimeout.String()
	return c
}

func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		data, err := os.ReadFile(ExpandPath(path))
		if err != nil {
			return c, err
		}
		if err := yaml.Unmarshal(data, &c); err != nil {
			return c, err
		}
	}
	applyEnv(&c)
	c.CredentialPath = ExpandPath(c.CredentialPath)
	return c, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("VOXGATE_CREDENTIAL_PATH"); v != "" {
		c.CredentialPath = v
	}
	if v := os.Getenv("VOXGATE_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("VOXGATE_ASR_AUDIO_FORMAT"); v != "" {
		c.ASR.AudioFormat = v
	}
	if v := os.Getenv("VOXGATE_ASR_HOTWORDS"); v != "" {
		c.ASR.Hotwords = SplitList(v)
	}
	if v := os.Getenv("VOXGATE_SERVER_HOST"); v != "" {
		c.Server.Host = v
	}
	if v := os.Getenv("VOXGATE_SERVER_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Server.Port = n
		}
	}
	if v := os.Getenv("VOXGATE_SERVER_AUTH_TOKEN"); v != "" {
		c.Server.AuthToken = v
	}
	if v := os.Getenv("VOXGATE_SERVER_MAX_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Server.MaxConcurrency = n
		}
	}
	if v := os.Getenv("VOXGATE_SERVER_REQUEST_TIMEOUT"); v != "" {
		c.Server.RequestTimeout = v
	}
}

func ServerRequestTimeout(c Config) time.Duration {
	d, err := time.ParseDuration(c.Server.RequestTimeout)
	if err != nil || d <= 0 {
		return DefaultServerRequestTimeout
	}
	return d
}

func ExpandPath(path string) string {
	return paths.Expand(path)
}

func SplitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
