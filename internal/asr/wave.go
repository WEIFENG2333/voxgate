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

type waveSession struct {
	key       []byte
	ticket    string
	expiresAt time.Time
}

type WaveClient struct {
	deviceID     string
	appID        int
	userAgent    string
	http         *http.Client
	handshakeURL string
	session      *waveSession
}

func NewWaveClient(deviceID string, appID int, hc *http.Client) *WaveClient {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &WaveClient{deviceID: deviceID, appID: appID, userAgent: DefaultUserAgent, http: hc, handshakeURL: HandshakeURL}
}

func (w *WaveClient) Seal(ctx context.Context, plaintext []byte) ([]byte, map[string]string, error) {
	if err := w.ensureSession(ctx); err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	ciphertext, err := chacha20Crypt(w.session.key, nonce, plaintext)
	if err != nil {
		return nil, nil, err
	}
	sum := md5.Sum(ciphertext)
	return ciphertext, map[string]string{
		"x-tt-e-b":  "1",
		"x-tt-e-t":  w.session.ticket,
		"x-tt-e-p":  base64.StdEncoding.EncodeToString(nonce),
		"x-ss-stub": strings.ToUpper(hex.EncodeToString(sum[:])),
	}, nil
}

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
	body, err := json.Marshal(map[string]any{
		"version": 2,
		"random":  base64.StdEncoding.EncodeToString(clientRandom),
		"app_id":  strconv.Itoa(w.appID),
		"did":     w.deviceID,
		"key_shares": []map[string]string{{
			"curve":  "secp256r1",
			"pubkey": base64.StdEncoding.EncodeToString(ecdhPriv.PublicKey().Bytes()),
		}},
		"cipher_suites": []int{waveCipherChaCha20},
	})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(body)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, hash[:])
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.handshakeURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", w.userAgent)
	req.Header.Set("x-tt-s-sign", base64.StdEncoding.EncodeToString(sig))
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wave handshake HTTP %s: %s", resp.Status, truncate(respBody, 200))
	}
	var out struct {
		Random   string `json:"random"`
		KeyShare struct {
			Pubkey string `json:"pubkey"`
		} `json:"key_share"`
		Ticket    string `json:"ticket"`
		TicketExp int    `json:"ticket_exp"`
	}
	if err = json.Unmarshal(respBody, &out); err != nil {
		return err
	}
	serverPubBytes, err := base64.StdEncoding.DecodeString(out.KeyShare.Pubkey)
	if err != nil {
		return err
	}
	serverRandom, err := base64.StdEncoding.DecodeString(out.Random)
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
	key, err := deriveWaveKey(shared, clientRandom, serverRandom)
	if err != nil {
		return err
	}
	ttl := out.TicketExp
	if ttl <= 60 {
		ttl = 600
	}
	w.session = &waveSession{
		key:       key,
		ticket:    out.Ticket,
		expiresAt: time.Now().Add(time.Duration(ttl-60) * time.Second),
	}
	return nil
}

func deriveWaveKey(shared, clientRandom, serverRandom []byte) ([]byte, error) {
	salt := append(append(make([]byte, 0, len(clientRandom)+len(serverRandom)), clientRandom...), serverRandom...)
	key := make([]byte, chacha20.KeySize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, salt, []byte(waveHKDFInfo)), key); err != nil {
		return nil, err
	}
	return key, nil
}

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
