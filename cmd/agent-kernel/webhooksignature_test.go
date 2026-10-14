package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/temporality-project/temporality/workspace"
)

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func requestWithHeader(header, value string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/workspace/webhook/p/hook", nil)
	r.Header.Set(header, value)
	return r
}

// A trigger with a secret only accepts deliveries signed with it: the native
// X-Temporality-Signature form and the GitHub-style X-Hub-Signature-256 both
// verify; a wrong signature, a wrong secret or no signature at all fail.
// A per-trigger delivery token matches only its own stored hash.
func TestWebhookTokenMatches(t *testing.T) {
	token := "whk_AbCdEf1234567890"
	digest := sha256.Sum256([]byte(token))
	stored := hex.EncodeToString(digest[:])
	if !webhookTokenMatches(token, stored) {
		t.Fatal("correct token must match its hash")
	}
	if webhookTokenMatches("whk_other", stored) {
		t.Fatal("a different token must not match")
	}
	if webhookTokenMatches("", stored) {
		t.Fatal("an empty token must not match")
	}
}

// A config round-trip through PUT must keep the server-stored delivery-token
// hash whatever the client sent — placeholder, missing key, forged hash — and
// must never let a client install a hash of its own.
func TestPreserveWebhookTokenHash(t *testing.T) {
	stored := hex.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	incoming := func(extra string) json.RawMessage {
		return json.RawMessage(`{"path":"pr-created","secret":"s"` + extra + `}`)
	}
	cases := []struct {
		name        string
		incoming    json.RawMessage
		storedHash  string
		triggerType string
		want        string
	}{
		{"placeholder from the form", incoming(`,"auth_token_hash":"set"`), stored, "webhook", stored},
		{"missing key", incoming(""), stored, "webhook", stored},
		{"forged hash", incoming(`,"auth_token_hash":"deadbeef"`), stored, "webhook", stored},
		{"already correct stays untouched", incoming(`,"auth_token_hash":"` + stored + `"`), stored, "webhook", stored},
		{"no stored hash: client hash is dropped", incoming(`,"auth_token_hash":"deadbeef"`), "", "webhook", ""},
		{"non-webhook type passes through", incoming(`,"auth_token_hash":"deadbeef"`), stored, "schedule", "deadbeef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := preserveWebhookTokenHash(tc.incoming, tc.storedHash, tc.triggerType)
			var cfg workspace.WebhookConfig
			if err := json.Unmarshal(got, &cfg); err != nil {
				t.Fatalf("result is not valid webhook config: %v", err)
			}
			if cfg.AuthTokenHash != tc.want {
				t.Fatalf("auth_token_hash = %q, want %q", cfg.AuthTokenHash, tc.want)
			}
			if cfg.Path != "pr-created" {
				t.Fatalf("path must survive the merge, got %q", cfg.Path)
			}
		})
	}
}

func TestWebhookStoredTokenHash(t *testing.T) {
	if got := webhookStoredTokenHash(json.RawMessage(`{"path":"p","auth_token_hash":"abc"}`)); got != "abc" {
		t.Fatalf("stored hash = %q, want abc", got)
	}
	if got := webhookStoredTokenHash(json.RawMessage(`{"path":"p"}`)); got != "" {
		t.Fatalf("absent hash must read as empty, got %q", got)
	}
	if got := webhookStoredTokenHash(json.RawMessage(`not json`)); got != "" {
		t.Fatalf("unparsable config must read as empty, got %q", got)
	}
}

func TestWebhookSignatureValid(t *testing.T) {
	body := []byte(`{"eventType":"git.pullrequest.created"}`)
	secret := "hmac-secret"
	signature := signBody(secret, body)

	cases := []struct {
		name   string
		header string
		value  string
		want   bool
	}{
		{"native form", "X-Temporality-Signature", "sha256-" + signature, true},
		{"native form uppercase hex", "X-Temporality-Signature", "sha256-" + signature, true},
		{"github form", "X-Hub-Signature-256", "sha256=" + signature, true},
		{"github form without prefix", "X-Hub-Signature-256", signature, true},
		{"wrong signature", "X-Temporality-Signature", "sha256-" + signBody("other", body), false},
		{"missing signature", "X-Temporality-Signature", "", false},
		{"no headers at all", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := requestWithHeader(tc.header, tc.value)
			if got := webhookSignatureValid(r, body, secret); got != tc.want {
				t.Fatalf("webhookSignatureValid(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
