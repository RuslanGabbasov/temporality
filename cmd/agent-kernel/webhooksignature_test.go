package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
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
