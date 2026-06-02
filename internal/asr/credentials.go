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

	"github.com/WEIFENG2333/voxgate/internal/paths"
	"github.com/google/uuid"
)

// Credentials 是发起 ASR 所需的设备凭证。
// Token 恒为内置 appKey；Dynamic 保存 settings 下发、可动态覆盖的会话参数。
type Credentials struct {
	DeviceID         string         `json:"device_id"`
	InstallID        string         `json:"install_id"`
	CDID             string         `json:"cdid"`
	OpenUDID         string         `json:"openudid"`
	ClientUDID       string         `json:"clientudid"`
	Token            string         `json:"token"` // = BuiltinASRAppKey
	TokenUpdatedAtMS int64          `json:"token_updated_at_ms"`
	Dynamic          map[string]any `json:"dynamic_config,omitempty"`
}

// CredentialManager 创建并维护设备凭证。
type CredentialManager struct {
	Path      string
	UserAgent string
	HTTP      *http.Client
	Device    DeviceProfile
}

func (m *CredentialManager) prepare() {
	if m.Device.Model == "" {
		m.Device = DefaultDevice
	}
	if m.UserAgent == "" {
		m.UserAgent = m.Device.UserAgent()
	}
	if m.HTTP == nil {
		m.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
}

func DefaultCredentialPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "voxgate", "credentials.json")
	}
	return filepath.Join(os.TempDir(), "voxgate-credentials.json")
}

// Ensure 返回可用凭证：必要时注册设备；ASR appKey 内置（无需网络）；
// 按需拉取 settings 动态配置。forceRefresh 强制刷新动态配置。
func (m CredentialManager) Ensure(ctx context.Context, forceRefresh bool) (Credentials, error) {
	m.prepare()
	creds, _ := LoadCredentials(m.Path)
	if creds.DeviceID == "" {
		var err error
		creds, err = m.Register(ctx)
		if err != nil {
			return Credentials{}, err
		}
	}
	creds.Token = BuiltinASRAppKey // appKey 内置固定
	stale := forceRefresh || creds.Dynamic == nil ||
		time.Since(time.UnixMilli(creds.TokenUpdatedAtMS)) >= settingsRefreshInterval
	if stale {
		if dyn, err := m.FetchSettings(ctx, creds.DeviceID, creds.CDID); err == nil && dyn != nil {
			creds.Dynamic = dyn
		}
		creds.TokenUpdatedAtMS = time.Now().UnixMilli()
	}
	_ = SaveCredentials(m.Path, creds)
	return creds, nil
}

// Reissue 重新注册一个全新设备身份（用于服务端拒绝旧设备时）。
func (m CredentialManager) Reissue(ctx context.Context) (Credentials, error) {
	m.prepare()
	creds, err := m.Register(ctx)
	if err != nil {
		return Credentials{}, err
	}
	creds.Token = BuiltinASRAppKey
	if dyn, err := m.FetchSettings(ctx, creds.DeviceID, creds.CDID); err == nil && dyn != nil {
		creds.Dynamic = dyn
	}
	creds.TokenUpdatedAtMS = time.Now().UnixMilli()
	_ = SaveCredentials(m.Path, creds)
	return creds, nil
}

func LoadCredentials(path string) (Credentials, error) {
	if path == "" {
		path = DefaultCredentialPath()
	}
	data, err := os.ReadFile(paths.Expand(path))
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
	path = paths.Expand(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Register 注册设备，返回 device_id / install_id。
func (m CredentialManager) Register(ctx context.Context) (Credentials, error) {
	m.prepare()
	d := m.Device
	cdid := uuid.NewString()
	openudid, err := randomHex(8)
	if err != nil {
		return Credentials{}, err
	}
	clientudid := uuid.NewString()
	osAPI := strconv.Itoa(d.OSAPI)
	vc := strconv.Itoa(VersionCode)
	body := map[string]any{
		"magic_tag": "ss_app_log",
		"header": map[string]any{
			"device_id": 0, "install_id": 0, "aid": AID, "app_name": AppName,
			"version_code": VersionCode, "version_name": VersionName,
			"manifest_version_code": VersionCode, "update_version_code": VersionCode,
			"channel": "official", "package": Package, "device_platform": "android", "os": "android",
			"os_api": osAPI, "os_version": d.OSVersion,
			"device_type": d.Model, "device_brand": d.Brand, "device_model": d.Model,
			"device_manufacturer": d.Manufacturer,
			"resolution":          d.Resolution, "dpi": d.DPI, "language": "zh", "timezone": 8, "access": "wifi",
			"rom": d.ROMBuild, "rom_version": d.ROMBuild, "region": "CN", "tz_name": "Asia/Shanghai",
			"tz_offset": 28800, "sim_region": "cn", "carrier_region": "cn", "cpu_abi": d.CPUABI, "build_serial": "unknown",
			"not_request_sender": 0, "sig_hash": "", "google_aid": "", "mc": "", "serial_number": "",
			"openudid": openudid, "clientudid": clientudid, "cdid": cdid,
		},
		"_gen_time": time.Now().UnixMilli(),
	}
	params := url.Values{
		"device_platform": {"android"}, "os": {"android"}, "ssmix": {"a"}, "_rticket": {strconv.FormatInt(time.Now().UnixMilli(), 10)},
		"cdid": {cdid}, "channel": {"official"}, "aid": {strconv.Itoa(AID)}, "app_name": {AppName},
		"version_code": {vc}, "version_name": {VersionName}, "manifest_version_code": {vc}, "update_version_code": {vc},
		"resolution": {d.Resolution}, "dpi": {d.DPI}, "device_type": {d.Model}, "device_brand": {d.Brand},
		"language": {"zh"}, "os_api": {osAPI}, "os_version": {d.OSVersion}, "ac": {"wifi"},
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
		Token:      BuiltinASRAppKey,
	}, nil
}

// FetchSettings 拉取 settings 下发的 asr_config（动态会话配置）。
// 返回的 app_key 是 bot key，不用作语音 token。
func (m CredentialManager) FetchSettings(ctx context.Context, deviceID, cdid string) (map[string]any, error) {
	m.prepare()
	body := "body=null"
	sum := md5.Sum([]byte(body))
	vc := strconv.Itoa(VersionCode)
	params := url.Values{
		"device_platform": {"android"}, "os": {"android"}, "ssmix": {"a"}, "channel": {"official"},
		"aid": {strconv.Itoa(AID)}, "app_name": {AppName}, "version_code": {vc}, "version_name": {VersionName},
		"device_id": {deviceID}, "cdid": {cdid}, "_rticket": {strconv.FormatInt(time.Now().UnixMilli(), 10)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SettingsURL+"?"+params.Encode(), strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", m.UserAgent)
	req.Header.Set("x-ss-stub", strings.ToUpper(hex.EncodeToString(sum[:])))
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("settings HTTP %s: %s", resp.Status, string(respBody))
	}
	var out struct {
		Data struct {
			Settings struct {
				ASRConfig map[string]any `json:"asr_config"`
			} `json:"settings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	return out.Data.Settings.ASRConfig, nil
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
