package config

import (
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/WEIFENG2333/ime-asr/internal/asr"
)

type Config struct {
	CredentialPath string `yaml:"credential_path"`
	LogLevel       string `yaml:"log_level"`
	ASR            struct {
		EnablePunctuation bool   `yaml:"enable_punctuation"`
		EnableThreePass   bool   `yaml:"enable_three_pass"`
		EnableTwoPass     bool   `yaml:"enable_two_pass"`
		UserAgent         string `yaml:"user_agent"`
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
	c.Server.Host = "127.0.0.1"
	c.Server.Port = 8080
	c.Server.MaxConcurrency = 4
	c.Server.RequestTimeout = "60s"
	return c
}

func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		data, err := os.ReadFile(expandHome(path))
		if err != nil {
			return c, err
		}
		if err := yaml.Unmarshal(data, &c); err != nil {
			return c, err
		}
	}
	applyEnv(&c)
	return c, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("DOUBAO_ASR_CREDENTIAL_PATH"); v != "" {
		c.CredentialPath = v
	}
	if v := os.Getenv("IME_ASR_CREDENTIAL_PATH"); v != "" {
		c.CredentialPath = v
	}
	if v := os.Getenv("DOUBAO_ASR_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("IME_ASR_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("DOUBAO_ASR_SERVER_HOST"); v != "" {
		c.Server.Host = v
	}
	if v := os.Getenv("IME_ASR_SERVER_HOST"); v != "" {
		c.Server.Host = v
	}
	if v := firstEnv("DOUBAO_ASR_SERVER_PORT", "IME_ASR_SERVER_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Server.Port = n
		}
	}
	if v := firstEnv("DOUBAO_ASR_SERVER_AUTH_TOKEN", "IME_ASR_SERVER_AUTH_TOKEN"); v != "" {
		c.Server.AuthToken = v
	}
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + "/" + strings.TrimPrefix(path, "~/")
		}
	}
	return path
}
