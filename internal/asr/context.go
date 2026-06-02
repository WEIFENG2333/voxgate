package asr

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ContextClient 调用 speech.bytedance.com 的语音个性化接口。
// 这些接口需 Wave 加密 + sami_token，与 ASR 会话相互独立，但靠同一 device_id 关联。
type ContextClient struct {
	deviceID  string
	cdid      string
	device    DeviceProfile
	userAgent string
	http      *http.Client
	wave      *WaveClient
	samiToken string
}

func NewContextClient(creds Credentials, device DeviceProfile, hc *http.Client) *ContextClient {
	if device.Model == "" {
		device = DefaultDevice
	}
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &ContextClient{
		deviceID:  creds.DeviceID,
		cdid:      creds.CDID,
		device:    device,
		userAgent: device.UserAgent(),
		http:      hc,
		wave:      NewWaveClient(creds.DeviceID, AID, hc),
	}
}

// ReportUserWords 上报个人热词，服务端按 device_id 累积，用于增强后续识别。空词条自动忽略。
func (c *ContextClient) ReportUserWords(ctx context.Context, words []string) error {
	items := make([]map[string]string, 0, len(words))
	for _, w := range words {
		if w = strings.TrimSpace(w); w != "" {
			items = append(items, map[string]string{"word": w})
		}
	}
	if len(items) == 0 {
		return nil
	}
	token, err := c.ensureSamiToken(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"user":       c.userBlock(),
		"user_words": items,
	})
	if err != nil {
		return err
	}
	_, err = c.postEncrypted(ctx, UserWordsURL, token, payload)
	return err
}

func (c *ContextClient) userBlock() map[string]any {
	return map[string]any{
		"uid":                "0",
		"did":                c.deviceID,
		"app_name":           Package,
		"app_version":        VersionName,
		"sdk_version":        "",
		"platform":           "android",
		"experience_improve": false,
	}
}

// postEncrypted 用 Wave 加密请求体发往 context 接口，并按响应头解密返回体。
func (c *ContextClient) postEncrypted(ctx context.Context, endpoint, token string, plaintext []byte) ([]byte, error) {
	ciphertext, encHeaders, err := c.wave.Seal(ctx, plaintext)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(ciphertext))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("app_version", VersionName)
	req.Header.Set("app_id", strconv.Itoa(AID))
	req.Header.Set("os_type", "android")
	req.Header.Set("x-api-resource-id", ContextResourceID)
	req.Header.Set("x-api-app-key", SamiAppKey)
	req.Header.Set("x-api-token", token)
	req.Header.Set("x-api-request-id", uuid.NewString())
	for k, v := range encHeaders {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("context %s HTTP %s: %s", endpoint, resp.Status, truncate(body, 200))
	}
	if p := resp.Header.Get("x-tt-e-p"); p != "" {
		nonce, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			return nil, err
		}
		return c.wave.Open(body, nonce)
	}
	return body, nil
}

func (c *ContextClient) ensureSamiToken(ctx context.Context) (string, error) {
	if c.samiToken != "" {
		return c.samiToken, nil
	}
	token, err := c.fetchSamiToken(ctx)
	if err != nil {
		return "", err
	}
	c.samiToken = token
	return token, nil
}

// fetchSamiToken 通过 get_config 用 SamiAppKey 换取 context 接口的 sami_token。
func (c *ContextClient) fetchSamiToken(ctx context.Context) (string, error) {
	cdid := c.cdid
	if cdid == "" {
		cdid = uuid.NewString()
	}
	body := `{"sami_app_key":"` + SamiAppKey + `"}`
	sum := md5.Sum([]byte(body))
	d := c.device
	vc := strconv.Itoa(VersionCode)
	params := url.Values{
		"device_platform": {"android"}, "os": {"android"}, "ssmix": {"a"},
		"_rticket": {strconv.FormatInt(time.Now().UnixMilli(), 10)},
		"cdid":     {cdid}, "channel": {"official"}, "aid": {strconv.Itoa(AID)}, "app_name": {AppName},
		"version_code": {vc}, "version_name": {VersionName}, "manifest_version_code": {vc}, "update_version_code": {vc},
		"resolution": {d.Resolution}, "dpi": {d.DPI}, "device_type": {d.Model}, "device_brand": {d.Brand},
		"language": {"zh"}, "os_api": {strconv.Itoa(d.OSAPI)}, "os_version": {d.OSVersion}, "ac": {"wifi"},
		"use-olympus-account": {"1"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GetConfigURL+"?"+params.Encode(), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("app_version", VersionName)
	req.Header.Set("app_id", strconv.Itoa(AID))
	req.Header.Set("os_type", "Android")
	req.Header.Set("x-ss-stub", strings.ToUpper(hex.EncodeToString(sum[:])))
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get_config HTTP %s: %s", resp.Status, truncate(respBody, 200))
	}
	var out struct {
		Data struct {
			SamiToken string `json:"sami_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", err
	}
	if out.Data.SamiToken == "" {
		return "", fmt.Errorf("get_config: empty sami_token: %s", truncate(respBody, 200))
	}
	return out.Data.SamiToken, nil
}
