package asr

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func TestContextClientReportUserWordsPostsEncryptedWords(t *testing.T) {
	var waveKey []byte
	var gotWords []string

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/get_config":
			if r.Header.Get("x-ss-stub") == "" {
				t.Errorf("missing x-ss-stub")
			}
			return contextTestJSONResponse(200, `{"data":{"sami_token":"test-token"}}`), nil
		case "/handshake":
			if r.Header.Get("User-Agent") != "ua" {
				t.Errorf("handshake User-Agent = %q, want ua", r.Header.Get("User-Agent"))
			}
			var req struct {
				Random    string `json:"random"`
				KeyShares []struct {
					Pubkey string `json:"pubkey"`
				} `json:"key_shares"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("bad handshake request: %v", err)
				return contextTestJSONResponse(400, `{}`), nil
			}
			clientRandom, err := base64.StdEncoding.DecodeString(req.Random)
			if err != nil {
				t.Errorf("bad client random: %v", err)
				return contextTestJSONResponse(400, `{}`), nil
			}
			clientPubBytes, err := base64.StdEncoding.DecodeString(req.KeyShares[0].Pubkey)
			if err != nil {
				t.Errorf("bad client public key: %v", err)
				return contextTestJSONResponse(400, `{}`), nil
			}
			clientPub, err := ecdh.P256().NewPublicKey(clientPubBytes)
			if err != nil {
				t.Errorf("bad client public key: %v", err)
				return contextTestJSONResponse(400, `{}`), nil
			}
			serverPriv, err := ecdh.P256().GenerateKey(rand.Reader)
			if err != nil {
				t.Errorf("server key: %v", err)
				return contextTestJSONResponse(500, `{}`), nil
			}
			shared, err := serverPriv.ECDH(clientPub)
			if err != nil {
				t.Errorf("shared key: %v", err)
				return contextTestJSONResponse(500, `{}`), nil
			}
			serverRandom := make([]byte, 32)
			if _, err := rand.Read(serverRandom); err != nil {
				t.Errorf("server random: %v", err)
				return contextTestJSONResponse(500, `{}`), nil
			}
			waveKey, err = deriveWaveKey(shared, clientRandom, serverRandom)
			if err != nil {
				t.Errorf("derive key: %v", err)
				return contextTestJSONResponse(500, `{}`), nil
			}
			body, _ := json.Marshal(map[string]any{
				"random": base64.StdEncoding.EncodeToString(serverRandom),
				"key_share": map[string]string{
					"pubkey": base64.StdEncoding.EncodeToString(serverPriv.PublicKey().Bytes()),
				},
				"ticket":     "ticket",
				"ticket_exp": 600,
			})
			return contextTestBytesResponse(200, body), nil
		case "/user_words":
			if r.Header.Get("x-api-token") != "test-token" {
				t.Errorf("x-api-token = %q, want test-token", r.Header.Get("x-api-token"))
			}
			nonce, err := base64.StdEncoding.DecodeString(r.Header.Get("x-tt-e-p"))
			if err != nil {
				t.Errorf("bad nonce: %v", err)
				return contextTestJSONResponse(400, `{}`), nil
			}
			ciphertext, _ := io.ReadAll(r.Body)
			plaintext, err := chacha20Crypt(waveKey, nonce, ciphertext)
			if err != nil {
				t.Errorf("decrypt: %v", err)
				return contextTestJSONResponse(400, `{}`), nil
			}
			var body struct {
				UserWords []struct {
					Word string `json:"word"`
				} `json:"user_words"`
			}
			if err := json.Unmarshal(plaintext, &body); err != nil {
				t.Errorf("bad encrypted body: %v", err)
				return contextTestJSONResponse(400, `{}`), nil
			}
			for _, item := range body.UserWords {
				gotWords = append(gotWords, item.Word)
			}
			return contextTestJSONResponse(200, `{}`), nil
		default:
			return contextTestJSONResponse(404, `{}`), nil
		}
	})

	httpClient := &http.Client{Transport: transport}
	client := NewContextClient(Credentials{DeviceID: "device", CDID: "cdid"}, "ua", httpClient)
	client.getConfigURL = "https://voxgate.test/get_config"
	client.userWordsURL = "https://voxgate.test/user_words"
	client.wave.handshakeURL = "https://voxgate.test/handshake"

	if err := client.ReportUserWords(context.Background(), []string{" Claude Code ", "", "Anthropic"}); err != nil {
		t.Fatalf("ReportUserWords returned error: %v", err)
	}
	want := []string{"Claude Code", "Anthropic"}
	if !reflect.DeepEqual(gotWords, want) {
		t.Fatalf("reported words = %#v, want %#v", gotWords, want)
	}
}

func TestContextClientReportUserWordsIgnoresEmptyWords(t *testing.T) {
	client := NewContextClient(Credentials{DeviceID: "device"}, "", nil)
	if err := client.ReportUserWords(context.Background(), []string{" ", ""}); err != nil {
		t.Fatalf("ReportUserWords returned error: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func contextTestJSONResponse(status int, body string) *http.Response {
	return contextTestBytesResponse(status, []byte(body))
}

func contextTestBytesResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}
