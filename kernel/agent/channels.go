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
//
// Transport credentials live in two layers: the admin-editable overlay in the
// workspace database (channel_transport row, edited in the UI) with the
// KERNEL_* environment variables as fallback. Either source alone works; the
// database wins per field.

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
	// NotConfiguredHint names what the admin has to provide, so the UI can
	// link the missing piece to the transport settings screen.
	NotConfiguredHint string `json:"not_configured_hint,omitempty"`
}

// TransportSettings is the effective credential set of the delivery
// transports: database overlay merged over the environment. The zero value
// means "env only" — which is exactly the pre-database behavior.
type TransportSettings struct {
	MatrixHomeserver  string
	MatrixAccessToken string
	TelegramBotToken  string
	WebhookSecret     string
	UIURL             string
}

// TrimBearerPrefix normalizes a pasted access token: admins routinely copy
// the whole "Authorization: Bearer …" header value (or "Bearer …" alone)
// into the token field, and sending "Bearer Bearer …" makes homeservers
// reject the request with M_MISSING_TOKEN. Only the bare token is kept.
func TrimBearerPrefix(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 7 && strings.EqualFold(value[:7], "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return value
}

// TransportSettingsFromEnv reads the legacy credential environment. It stays
// the source of truth when no database overlay exists and the fallback per
// field when the overlay is partial.
func TransportSettingsFromEnv() TransportSettings {
	return TransportSettings{
		MatrixHomeserver:  strings.TrimSpace(os.Getenv("KERNEL_MATRIX_HOMESERVER")),
		MatrixAccessToken: strings.TrimSpace(os.Getenv("KERNEL_MATRIX_ACCESS_TOKEN")),
		TelegramBotToken:  strings.TrimSpace(os.Getenv("KERNEL_TELEGRAM_BOT_TOKEN")),
		WebhookSecret:     strings.TrimSpace(os.Getenv("KERNEL_WEBHOOK_SECRET")),
		UIURL:             strings.TrimSpace(os.Getenv("KERNEL_UI_URL")),
	}
}

// OverlayEnv fills every empty field from the environment: the database
// overlay wins per field, env covers the rest.
func (s TransportSettings) OverlayEnv() TransportSettings {
	env := TransportSettingsFromEnv()
	if s.MatrixHomeserver == "" {
		s.MatrixHomeserver = env.MatrixHomeserver
	}
	if s.MatrixAccessToken == "" {
		s.MatrixAccessToken = env.MatrixAccessToken
	}
	if s.TelegramBotToken == "" {
		s.TelegramBotToken = env.TelegramBotToken
	}
	if s.WebhookSecret == "" {
		s.WebhookSecret = env.WebhookSecret
	}
	if s.UIURL == "" {
		s.UIURL = env.UIURL
	}
	return s
}

// Configured reports whether the kernel holds the credentials a transport
// needs. Self-contained transports are always configured.
func (s TransportSettings) Configured(channelType string) bool {
	switch channelType {
	case "matrix":
		return s.MatrixHomeserver != "" && s.MatrixAccessToken != ""
	case "telegram":
		return s.TelegramBotToken != ""
	case "slack", "webhook":
		return true
	}
	return false
}

// SecretHint renders a non-reversible display form of a secret for admin
// screens: never the value itself, just proof it is set.
func SecretHint(secret string) string {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 4 {
		return "…"
	}
	return "…" + trimmed[len(trimmed)-4:]
}

// channelRegistry lists the transports in preference order. The set is
// intentionally small: matrix and telegram are bot-mediated; slack rides an
// incoming webhook; webhook is the generic escape hatch any external system
// (Slack, Discord, Mattermost, a corporate messenger gateway) can answer.
func channelRegistry(settings TransportSettings) []ChannelTypeSpec {
	return []ChannelTypeSpec{
		{
			Type:              "matrix",
			Label:             "Matrix",
			AddressHint:       "Matrix room ID, e.g. !room:matrix.org — the kernel bot must have joined it",
			Configured:        settings.Configured("matrix"),
			NotConfiguredHint: "Matrix homeserver + bot access token",
		},
		{
			Type:              "telegram",
			Label:             "Telegram",
			AddressHint:       "Telegram chat ID, e.g. 123456789 — the user must have started the bot",
			Configured:        settings.Configured("telegram"),
			NotConfiguredHint: "Telegram bot token",
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

// ChannelTypes returns the registry for the env-only configuration surface.
func ChannelTypes() []ChannelTypeSpec { return channelRegistry(TransportSettingsFromEnv()) }

// ChannelTypesWith returns the registry computed from the effective settings
// (database overlay over env) — the live view the API serves.
func ChannelTypesWith(settings TransportSettings) []ChannelTypeSpec {
	return channelRegistry(settings.OverlayEnv())
}

// transports resolves the effective transport settings for one delivery: the
// database overlay injected by main when available, env as the fallback. A
// broken loader degrades to env rather than silencing delivery.
func (a *Activities) transports(ctx context.Context) TransportSettings {
	if a.ChannelSettings != nil {
		if stored, err := a.ChannelSettings(ctx); err == nil {
			return stored.OverlayEnv()
		}
	}
	return TransportSettingsFromEnv()
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
// extensible transport. When a signing secret is configured (database
// overlay or env) the raw body is signed (X-Temporality-Signature:
// sha256-<hmac>) so the receiver can verify origin and integrity.
func (a *Activities) sendWebhook(ctx context.Context, target string, request NotifyChannelRequest, settings TransportSettings) error {
	parsed, err := parseHTTPURL(target)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(webhookEnvelope(request, settings.UIURL))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed, strings.NewReader(string(encoded)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if secret := strings.TrimSpace(settings.WebhookSecret); secret != "" {
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
func webhookEnvelope(request NotifyChannelRequest, uiURL string) map[string]any {
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
	if trimmed := strings.TrimSpace(uiURL); trimmed != "" {
		envelope["answer_url"] = strings.TrimRight(trimmed, "/") + "/agents?project=" + url.QueryEscape(request.Project)
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
