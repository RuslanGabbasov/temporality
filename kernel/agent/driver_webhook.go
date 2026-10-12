package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// webhookDriver posts the full question envelope to any HTTPS endpoint —
// the extensible transport. When a signing secret is configured (database
// overlay or env) the raw body is signed (X-Temporality-Signature:
// sha256-<hmac>) so the receiver can verify origin and integrity. The
// answer itself happens in the product UI; the envelope carries the link.
type webhookDriver struct{}

func (webhookDriver) Type() string { return "webhook" }

// Poller: the generic webhook is outbound-only — answers happen in the UI.
func (webhookDriver) Poller() ChannelPoller { return nil }

// Webhook: outbound-only transport; inbound is the product UI.
func (webhookDriver) Webhook() ChannelWebhook { return nil }

func (webhookDriver) Spec() ChannelSpec {
	return ChannelSpec{
		Label:        "Webhook",
		AddressHint:  "HTTPS URL that accepts a JSON POST with the question payload",
		Capabilities: ChannelCapabilities{Text: true},
		Settings: []SettingField{
			{Name: "secret", Label: "Signature secret", Secret: true},
		},
	}
}

// IsConfigured: the target URL carries everything — always ready. The
// signature secret is optional hardening, not a precondition.
func (webhookDriver) IsConfigured(map[string]string) bool { return true }

func (webhookDriver) SendText(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) error {
	parsed, err := parseHTTPURL(address)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(webhookEnvelope(msg))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed, strings.NewReader(string(encoded)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret := setting(settings, "secret"); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(encoded)
		req.Header.Set("X-Temporality-Signature", "sha256-"+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw := make([]byte, 512)
		n, _ := resp.Body.Read(raw)
		return fmt.Errorf("webhook returned %d: %s", resp.StatusCode, truncate(string(raw[:n]), 200))
	}
	return nil
}

func (webhookDriver) SendFile(context.Context, *http.Client, map[string]string, string, OutboundFile) error {
	return ErrChannelFilesUnsupported
}

// webhookEnvelope is the payload generic webhooks receive: everything needed
// to identify the ask and build an answer link.
func webhookEnvelope(msg OutboundText) map[string]any {
	envelope := map[string]any{
		"type":            "temporality.human_request/v1",
		"project":         msg.Project,
		"run_id":          msg.RunID,
		"operation_id":    msg.OperationID,
		"agent_id":        msg.AgentID,
		"question":        msg.Question,
		"context":         msg.Context,
		"options":         msg.Options,
		"timeout_seconds": msg.TimeoutSeconds,
		"timeout_policy":  msg.TimeoutPolicy,
	}
	if envelope["options"] == nil {
		envelope["options"] = []string{}
	}
	if msg.AnswerURL != "" {
		envelope["answer_url"] = msg.AnswerURL
	}
	return envelope
}
