package agent

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// The channel registry (docs/triggers-and-escalations.md §7, docs/channels.md):
// the transports the kernel can deliver human requests through. The registry
// is the ordered list of channel drivers (channeldriver.go) — adding a
// channel type is one driver plus one registration line, and user channel
// rows are type-agnostic, so no storage migration is involved. The registry
// is also served to the UI (GET /v1/workspace/channel-types) so users only
// ever pick from what the kernel can actually deliver.
//
// Transport credentials live in two layers: the admin-editable overlay in the
// workspace database (channel_transport row, edited in the UI) with the
// KERNEL_* environment variables as fallback. Either source alone works; the
// database wins per field. Drivers receive the effective values as a
// map[string]string and own their field names.

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

// channelMaps splits the typed overlay into per-channel settings maps the
// drivers consume. UIURL is deliberately absent: it is a global link builder
// input, not a channel credential — it travels inside OutboundText.
func (s TransportSettings) channelMaps() map[string]map[string]string {
	return map[string]map[string]string{
		"matrix":   {"homeserver": s.MatrixHomeserver, "access_token": s.MatrixAccessToken},
		"telegram": {"bot_token": s.TelegramBotToken},
		"slack":    {},
		"webhook":  {"secret": s.WebhookSecret},
	}
}

// Configured reports whether the kernel holds the credentials a transport
// needs. The answer comes from the channel's driver — the kernel does not
// know which fields matter. Self-contained transports are always configured.
func (s TransportSettings) Configured(channelType string) bool {
	driver := channelDriverByType(channelType)
	if driver == nil {
		return false
	}
	return driver.IsConfigured(s.channelMaps()[channelType])
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

// channelRegistry projects the driver list into the UI shape, computing the
// configured flag from the effective settings. The order is the registry's
// preference order — the recipient's channel picker falls back through it.
func channelRegistry(settings TransportSettings) []ChannelTypeSpec {
	perChannel := settings.channelMaps()
	specs := make([]ChannelTypeSpec, 0, len(channelDrivers))
	for _, driver := range channelDrivers {
		spec := driver.Spec()
		specs = append(specs, ChannelTypeSpec{
			Type:              driver.Type(),
			Label:             spec.Label,
			AddressHint:       spec.AddressHint,
			Configured:        driver.IsConfigured(perChannel[driver.Type()]),
			NotConfiguredHint: spec.NotConfiguredHint,
		})
	}
	return specs
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

// parseHTTPURL accepts only full http(s) URLs — user-provided webhook targets
// must be unambiguous, never a bare host or another scheme.
func parseHTTPURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("webhook address must be a full http(s) URL")
	}
	return parsed.String(), nil
}
