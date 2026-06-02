package asr

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20"
	"golang.org/x/crypto/hkdf"
)

// waveSession 是一次握手得到的会话密钥与票据。
type waveSession struct {
	key       []byte // ChaCha20 会话密钥
	ticket    string
	expiresAt time.Time
}

// WaveClient 实现 TTNet Wave 应用层加密：ECDH(P-256) + HKDF-SHA256 + ChaCha20，
// 握手向 keyhub 申请会话票据。speech.bytedance.com 的 context 接口要求此加密。
type WaveClient struct {
	deviceID string
	appID    int
	http     *http.Client
	session  *waveSession
}

func NewWaveClient(deviceID string, appID int, hc *http.Client) *WaveClient {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &WaveClient{deviceID: deviceID, appID: appID, http: hc}
}

// Seal 用会话密钥加密明文，返回密文与请求需附带的加密头；会话缺失或过期时自动握手。
func (w *WaveClient) Seal(ctx context.Context, plaintext []byte) (ciphertext []byte, headers map[string]string, err error) {
	if err = w.ensureSession(ctx); err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, 12)
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	ciphertext, err = chacha20Crypt(w.session.key, nonce, plaintext)
	if err != nil {
		return nil, nil, err
	}
	sum := md5.Sum(ciphertext)
	headers = map[string]string{
		"x-tt-e-b":  "1",
		"x-tt-e-t":  w.session.ticket,
		"x-tt-e-p":  base64.StdEncoding.EncodeToString(nonce),
		"x-ss-stub": strings.ToUpper(hex.EncodeToString(sum[:])),
	}
	return ciphertext, headers, nil
}

// Open 用会话密钥解密响应密文，nonce 取自响应头 x-tt-e-p。
func (w *WaveClient) Open(ciphertext, nonce []byte) ([]byte, error) {
	if w.session == nil {
		return nil, fmt.Errorf("wave: no active session")
	}
	return chacha20Crypt(w.session.key, nonce, ciphertext)
}

func (w *WaveClient) ensureSession(ctx context.Context) error {
	if w.session != nil && time.Now().Before(w.session.expiresAt) {
		return nil
	}
	return w.handshake(ctx)
}

func (w *WaveClient) handshake(ctx context.Context) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	ecdhPriv, err := priv.ECDH()
	if err != nil {
		return err
	}
	clientRandom := make([]byte, 32)
	if _, err = rand.Read(clientRandom); err != nil {
		return err
	}

	// 握手请求体：客户端公钥 + 随机数；用同一私钥 ECDSA 签名，置于 x-tt-s-sign。
	reqBody, err := json.Marshal(map[string]any{
		"version": 2,
		"random":  base64.StdEncoding.EncodeToString(clientRandom),
		"app_id":  strconv.Itoa(w.appID),
		"did":     w.deviceID,
		"key_shares": []map[string]string{
			{"curve": "secp256r1", "pubkey": base64.StdEncoding.EncodeToString(ecdhPriv.PublicKey().Bytes())},
		},
		"cipher_suites": []int{waveCipherChaCha20},
	})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(reqBody)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, hash[:])
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, HandshakeURL, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("x-tt-s-sign", base64.StdEncoding.EncodeToString(sig))
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wave handshake HTTP %s: %s", resp.Status, truncate(body, 200))
	}
	var hr struct {
		Random   string `json:"random"`
		KeyShare struct {
			Pubkey string `json:"pubkey"`
		} `json:"key_share"`
		Ticket    string `json:"ticket"`
		TicketExp int    `json:"ticket_exp"`
	}
	if err = json.Unmarshal(body, &hr); err != nil {
		return err
	}

	serverPubBytes, err := base64.StdEncoding.DecodeString(hr.KeyShare.Pubkey)
	if err != nil {
		return err
	}
	serverRandom, err := base64.StdEncoding.DecodeString(hr.Random)
	if err != nil {
		return err
	}
	serverPub, err := ecdh.P256().NewPublicKey(serverPubBytes)
	if err != nil {
		return err
	}
	shared, err := ecdhPriv.ECDH(serverPub)
	if err != nil {
		return err
	}

	// HKDF-SHA256(共享密钥, salt=client_random||server_random) 派生 ChaCha20 会话密钥。
	salt := append(append(make([]byte, 0, 64), clientRandom...), serverRandom...)
	key := make([]byte, 32)
	if _, err = io.ReadFull(hkdf.New(sha256.New, shared, salt, []byte(waveHKDFInfo)), key); err != nil {
		return err
	}

	ttl := hr.TicketExp
	if ttl <= 60 {
		ttl = 600
	}
	w.session = &waveSession{
		key:       key,
		ticket:    hr.Ticket,
		expiresAt: time.Now().Add(time.Duration(ttl-60) * time.Second), // 提前 60s 过期以便续期
	}
	return nil
}

// chacha20Crypt 用 12 字节 nonce、counter 从 0 开始做 ChaCha20，加解密对称。
func chacha20Crypt(key, nonce, data []byte) ([]byte, error) {
	c, err := chacha20.NewUnauthenticatedCipher(key, nonce)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
