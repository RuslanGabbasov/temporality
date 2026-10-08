package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// The channel registry (docs/triggers-and-escalations.md §7): the transports
// the kernel can deliver human requests through. Adding a channel type is one
// registry entry plus one sender — user channel rows are type-agnostic, so no
// storage migration is involved. The registry is also served to the UI
// (GET /v1/workspace/channel-types) so users only ever pick from what the
// kernel can actually deliver.

// ChannelTypeSpec describes one deliverable transport for the UI.
type ChannelTypeSpec struct {
	Type string `json:"type"`
	// Label is the human name shown in pickers.
	Label string `json:"label"`
	// AddressHint explains what the address field carries for this transport.
	AddressHint string `json:"address_hint"`
	// Configured reports whether the kernel can deliver through the transport
	// right now. Bot-mediated transports (matrix, telegram) need kernel-side
	// credentials; self-contained ones (slack, webhook) carry everything in
	// the address and are always ready.
	Configured bool `json:"configured"`
	// NotConfiguredHint names the missing environment for bot-mediated
	// transports, so the UI can tell the user what to ask the admin for.
	NotConfiguredHint string `json:"not_configured_hint,omitempty"`
}

// channelRegistry lists the transports in preference order. The set is
// intentionally small: matrix and telegram are bot-mediated; slack rides an
// incoming webhook; webhook is the generic escape hatch any external system
// (Slack, Discord, Mattermost, a corporate messenger gateway) can answer.
func channelRegistry() []ChannelTypeSpec {
	return []ChannelTypeSpec{
		{
			Type:              "matrix",
			Label:             "Matrix",
			AddressHint:       "Matrix room ID, e.g. !room:matrix.org — the kernel bot must have joined it",
			Configured:        channelConfigured("matrix"),
			NotConfiguredHint: "KERNEL_MATRIX_HOMESERVER / KERNEL_MATRIX_ACCESS_TOKEN",
		},
		{
			Type:              "telegram",
			Label:             "Telegram",
			AddressHint:       "Telegram chat ID, e.g. 123456789 — the user must have started the bot",
			Configured:        channelConfigured("telegram"),
			NotConfiguredHint: "KERNEL_TELEGRAM_BOT_TOKEN",
		},
		{
			Type:        "slack",
			Label:       "Slack",
			AddressHint: "Slack incoming webhook URL, https://hooks.slack.com/services/…",
			Configured:  true,
		},
		{
			Type:        "webhook",
			Label:       "Webhook",
			AddressHint: "HTTPS URL that accepts a JSON POST with the question payload",
			Configured:  true,
		},
	}
}

// ChannelTypes returns the registry for the API surface.
func ChannelTypes() []ChannelTypeSpec { return channelRegistry() }

// channelConfigured reports whether the kernel holds the credentials a
// transport needs. Self-contained transports are always configured.
func channelConfigured(channelType string) bool {
	switch channelType {
	case "matrix":
		return strings.TrimSpace(os.Getenv("KERNEL_MATRIX_HOMESERVER")) != "" && strings.TrimSpace(os.Getenv("KERNEL_MATRIX_ACCESS_TOKEN")) != ""
	case "telegram":
		return strings.TrimSpace(os.Getenv("KERNEL_TELEGRAM_BOT_TOKEN")) != ""
	case "slack", "webhook":
		return true
	}
	return false
}

// sendSlack posts the message through a per-user incoming webhook: the user
// creates the webhook in their Slack workspace, the address is the URL.
func (a *Activities) sendSlack(ctx context.Context, webhookURL, text string) error {
	parsed, err := parseHTTPURL(webhookURL)
	if err != nil {
		return err
	}
	payload := map[string]any{"text": text}
	body, status, err := a.postJSON(ctx, http.MethodPost, parsed, payload, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("slack returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// sendWebhook posts the full question envelope to any HTTPS endpoint — the
// extensible transport. When KERNEL_WEBHOOK_SECRET is set the raw body is
// signed (X-Temporality-Signature: sha256-<hmac>) so the receiver can verify
// origin and integrity.
func (a *Activities) sendWebhook(ctx context.Context, target string, request NotifyChannelRequest) error {
	parsed, err := parseHTTPURL(target)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(webhookEnvelope(request))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed, strings.NewReader(string(encoded)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret := strings.TrimSpace(os.Getenv("KERNEL_WEBHOOK_SECRET")); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(encoded)
		req.Header.Set("X-Temporality-Signature", "sha256-"+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := a.HTTP.Do(req)
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

// webhookEnvelope is the payload generic webhooks receive: everything needed
// to identify the ask and build an answer link.
func webhookEnvelope(request NotifyChannelRequest) map[string]any {
	envelope := map[string]any{
		"type":            "temporality.human_request/v1",
		"project":         request.Project,
		"run_id":          request.RunID,
		"operation_id":    request.OperationID,
		"agent_id":        request.AgentID,
		"question":        request.Question,
		"context":         request.Context,
		"options":         request.Options,
		"timeout_seconds": request.TimeoutSeconds,
		"timeout_policy":  request.TimeoutPolicy,
	}
	if envelope["options"] == nil {
		envelope["options"] = []string{}
	}
	if uiURL := strings.TrimSpace(os.Getenv("KERNEL_UI_URL")); uiURL != "" {
		envelope["answer_url"] = strings.TrimRight(uiURL, "/") + "/agents?project=" + url.QueryEscape(request.Project)
	}
	return envelope
}

// parseHTTPURL accepts only full http(s) URLs — user-provided webhook targets
// must be unambiguous, never a bare host or another scheme.
func parseHTTPURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("webhook address must be a full http(s) URL")
	}
	return parsed.String(), nil
}
