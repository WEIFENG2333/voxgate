package asr

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	RegisterURL          = "https://log.snssdk.com/service/2/device_register/"
	SettingsURL          = "https://is.snssdk.com/service/settings/v3/"
	WebSocketURL         = "wss://frontier-audio-ime-ws.doubao.com/ocean/api/v1/ws"
	AID                  = 401734
	DefaultUserAgent     = "com.bytedance.android.doubaoime/100102018 (Linux; U; Android 16; en_US; Pixel 7 Pro; Build/BP2A.250605.031.A2; Cronet/TTNetVersion:94cf429a 2025-11-17 QuicVersion:1f89f732 2025-05-08)"
	tokenRefreshInterval = 12 * time.Hour
)

type Credentials struct {
	DeviceID         string `json:"device_id"`
	InstallID        string `json:"install_id"`
	CDID             string `json:"cdid"`
	OpenUDID         string `json:"openudid"`
	ClientUDID       string `json:"clientudid"`
	Token            string `json:"token"`
	TokenUpdatedAtMS int64  `json:"token_updated_at_ms"`
}

type CredentialManager struct {
	Path      string
	UserAgent string
	HTTP      *http.Client
}

func DefaultCredentialPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "ime-asr", "credentials.json")
	}
	return filepath.Join(os.TempDir(), "ime-asr-credentials.json")
}

func (m CredentialManager) Ensure(ctx context.Context, forceRefresh bool) (Credentials, error) {
	if m.UserAgent == "" {
		m.UserAgent = DefaultUserAgent
	}
	if m.HTTP == nil {
		m.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	creds, _ := LoadCredentials(m.Path)
	if creds.DeviceID == "" {
		var err error
		creds, err = m.Register(ctx)
		if err != nil {
			return Credentials{}, err
		}
	}
	if !forceRefresh && creds.Token != "" && time.Since(time.UnixMilli(creds.TokenUpdatedAtMS)) < tokenRefreshInterval {
		return creds, nil
	}
	token, err := m.FetchToken(ctx, creds.DeviceID, creds.CDID)
	if err != nil {
		if creds.Token != "" {
			return creds, nil
		}
		return Credentials{}, err
	}
	creds.Token = token
	creds.TokenUpdatedAtMS = time.Now().UnixMilli()
	if err := SaveCredentials(m.Path, creds); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}

func LoadCredentials(path string) (Credentials, error) {
	if path == "" {
		path = DefaultCredentialPath()
	}
	data, err := os.ReadFile(expandHome(path))
	if err != nil {
		return Credentials{}, err
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}

func SaveCredentials(path string, creds Credentials) error {
	if path == "" {
		path = DefaultCredentialPath()
	}
	path = expandHome(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (m CredentialManager) Register(ctx context.Context) (Credentials, error) {
	cdid := uuid.NewString()
	openudid, err := randomHex(8)
	if err != nil {
		return Credentials{}, err
	}
	clientudid := uuid.NewString()
	body := map[string]any{
		"magic_tag": "ss_app_log",
		"header": map[string]any{
			"device_id": 0, "install_id": 0, "aid": AID, "app_name": "oime",
			"version_code": 100102018, "version_name": "1.1.2", "manifest_version_code": 100102018, "update_version_code": 100102018,
			"channel": "official", "package": "com.bytedance.android.doubaoime", "device_platform": "android", "os": "android",
			"os_api": "34", "os_version": "16", "device_type": "Pixel 7 Pro", "device_brand": "google", "device_model": "Pixel 7 Pro",
			"resolution": "1080*2400", "dpi": "420", "language": "zh", "timezone": 8, "access": "wifi",
			"rom": "UP1A.231005.007", "rom_version": "UP1A.231005.007", "region": "CN", "tz_name": "Asia/Shanghai",
			"tz_offset": 28800, "sim_region": "cn", "carrier_region": "cn", "cpu_abi": "arm64-v8a", "build_serial": "unknown",
			"not_request_sender": 0, "sig_hash": "", "google_aid": "", "mc": "", "serial_number": "",
			"openudid": openudid, "clientudid": clientudid, "cdid": cdid,
		},
		"_gen_time": time.Now().UnixMilli(),
	}
	params := url.Values{
		"device_platform": {"android"}, "os": {"android"}, "ssmix": {"a"}, "_rticket": {strconv.FormatInt(time.Now().UnixMilli(), 10)},
		"cdid": {cdid}, "channel": {"official"}, "aid": {strconv.Itoa(AID)}, "app_name": {"oime"},
		"version_code": {"100102018"}, "version_name": {"1.1.2"}, "manifest_version_code": {"100102018"}, "update_version_code": {"100102018"},
		"resolution": {"1080*2400"}, "dpi": {"420"}, "device_type": {"Pixel 7 Pro"}, "device_brand": {"google"},
		"language": {"zh"}, "os_api": {"34"}, "os_version": {"16"}, "ac": {"wifi"},
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, RegisterURL+"?"+params.Encode(), bytes.NewReader(data))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", m.UserAgent)
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return Credentials{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Credentials{}, fmt.Errorf("device register HTTP %s: %s", resp.Status, string(respBody))
	}
	var out map[string]any
	if err := json.Unmarshal(respBody, &out); err != nil {
		return Credentials{}, err
	}
	deviceID := jsonString(out["device_id_str"])
	if deviceID == "" || deviceID == "0" {
		deviceID = jsonString(out["device_id"])
	}
	if deviceID == "" || deviceID == "0" {
		return Credentials{}, errors.New("device register: missing device_id")
	}
	return Credentials{
		DeviceID:   deviceID,
		InstallID:  jsonString(out["install_id"]),
		CDID:       cdid,
		OpenUDID:   openudid,
		ClientUDID: clientudid,
	}, nil
}

func (m CredentialManager) FetchToken(ctx context.Context, deviceID, cdid string) (string, error) {
	body := "body=null"
	sum := md5.Sum([]byte(body))
	params := url.Values{
		"device_platform": {"android"}, "os": {"android"}, "ssmix": {"a"}, "channel": {"official"},
		"aid": {strconv.Itoa(AID)}, "app_name": {"oime"}, "version_code": {"100102018"}, "version_name": {"1.1.2"},
		"device_id": {deviceID}, "cdid": {cdid}, "_rticket": {strconv.FormatInt(time.Now().UnixMilli(), 10)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SettingsURL+"?"+params.Encode(), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", m.UserAgent)
	req.Header.Set("x-ss-stub", strings.ToUpper(hex.EncodeToString(sum[:])))
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("settings HTTP %s: %s", resp.Status, string(respBody))
	}
	var out struct {
		Data struct {
			Settings struct {
				ASRConfig struct {
					AppKey string `json:"app_key"`
				} `json:"asr_config"`
			} `json:"settings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", err
	}
	if out.Data.Settings.ASRConfig.AppKey == "" {
		return "", errors.New("settings: missing asr_config.app_key")
	}
	return out.Data.Settings.ASRConfig.AppKey, nil
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func jsonString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case json.Number:
		return x.String()
	default:
		return ""
	}
}
