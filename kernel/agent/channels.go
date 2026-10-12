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
// workspace database (channel_transport payload, edited in the UI) with the
// KERNEL_CHANNEL__<TYPE>__<FIELD> environment variables as fallback (legacy
// KERNEL_MATRIX_* & co remain aliases). Either source alone works; the
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

// ChannelSettings is the effective channel configuration: per-channel
// settings maps (database payload over env) plus the global UI base URL.
// Drivers own their field names — the kernel only routes whole maps by
// channel type, so a new channel needs no kernel changes. A missing channel
// key means "no settings stored", which drivers read as not configured.
type ChannelSettings struct {
	Channels map[string]map[string]string
	UIURL    string
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

// channelEnvAliases maps the legacy credential environment onto (channel,
// field) pairs, so env-only deployments keep working unchanged. Canonical
// names (KERNEL_CHANNEL__<TYPE>__<FIELD>) win when both are set.
var channelEnvAliases = map[string][2]string{
	"KERNEL_MATRIX_HOMESERVER":   {"matrix", "homeserver"},
	"KERNEL_MATRIX_ACCESS_TOKEN": {"matrix", "access_token"},
	"KERNEL_TELEGRAM_BOT_TOKEN":  {"telegram", "bot_token"},
	"KERNEL_WEBHOOK_SECRET":      {"webhook", "secret"},
}

// channelEnvName builds the canonical env var of one driver setting.
func channelEnvName(channelType, field string) string {
	return fmt.Sprintf("KERNEL_CHANNEL__%s__%s", strings.ToUpper(channelType), strings.ToUpper(field))
}

// ChannelSettingsFromEnv reads the credential environment: canonical
// KERNEL_CHANNEL__<TYPE>__<FIELD> variables for every registered driver
// setting, plus the legacy aliases, plus the global UI URL. It stays the
// source of truth when no database overlay exists and the fallback per field
// when the overlay is partial.
func ChannelSettingsFromEnv() ChannelSettings {
	channels := map[string]map[string]string{}
	set := func(channelType, field, value string) {
		if value == "" {
			return
		}
		if channels[channelType] == nil {
			channels[channelType] = map[string]string{}
		}
		channels[channelType][field] = value
	}
	for envName, target := range channelEnvAliases {
		set(target[0], target[1], strings.TrimSpace(os.Getenv(envName)))
	}
	for _, driver := range channelDrivers {
		for _, field := range driver.Spec().Settings {
			set(driver.Type(), field.Name, strings.TrimSpace(os.Getenv(channelEnvName(driver.Type(), field.Name))))
		}
	}
	return ChannelSettings{Channels: channels, UIURL: strings.TrimSpace(os.Getenv("KERNEL_UI_URL"))}
}

// OverlayEnv fills every empty field from the environment: the database
// overlay wins per field, env covers the rest. An empty stored value does
// not clear an env value — clearing is an explicit delete in the payload.
func (s ChannelSettings) OverlayEnv() ChannelSettings {
	env := ChannelSettingsFromEnv()
	types := map[string]bool{}
	for channelType := range s.Channels {
		types[channelType] = true
	}
	for channelType := range env.Channels {
		types[channelType] = true
	}
	channels := make(map[string]map[string]string, len(types))
	for channelType := range types {
		merged := map[string]string{}
		for field, value := range env.Channels[channelType] {
			merged[field] = value
		}
		for field, value := range s.Channels[channelType] {
			if value != "" {
				merged[field] = value
			}
		}
		channels[channelType] = merged
	}
	merged := ChannelSettings{Channels: channels}
	if s.UIURL != "" {
		merged.UIURL = s.UIURL
	} else {
		merged.UIURL = env.UIURL
	}
	return merged
}

// Configured reports whether the kernel holds the credentials a transport
// needs. The answer comes from the channel's driver — the kernel does not
// know which fields matter. Self-contained transports are always configured.
func (s ChannelSettings) Configured(channelType string) bool {
	driver := channelDriverByType(channelType)
	if driver == nil {
		return false
	}
	return driver.IsConfigured(s.Channels[channelType])
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
func channelRegistry(settings ChannelSettings) []ChannelTypeSpec {
	specs := make([]ChannelTypeSpec, 0, len(channelDrivers))
	for _, driver := range channelDrivers {
		spec := driver.Spec()
		specs = append(specs, ChannelTypeSpec{
			Type:              driver.Type(),
			Label:             spec.Label,
			AddressHint:       spec.AddressHint,
			Configured:        driver.IsConfigured(settings.Channels[driver.Type()]),
			NotConfiguredHint: spec.NotConfiguredHint,
		})
	}
	return specs
}

// ChannelTypes returns the registry for the env-only configuration surface.
func ChannelTypes() []ChannelTypeSpec { return channelRegistry(ChannelSettingsFromEnv()) }

// ChannelTypesWith returns the registry computed from the effective settings
// (database overlay over env) — the live view the API serves.
func ChannelTypesWith(settings ChannelSettings) []ChannelTypeSpec {
	return channelRegistry(settings.OverlayEnv())
}

// transports resolves the effective channel settings for one delivery: the
// database overlay injected by main when available, env as the fallback. A
// broken loader degrades to env rather than silencing delivery.
func (a *Activities) transports(ctx context.Context) ChannelSettings {
	if a.ChannelSettings != nil {
		if stored, err := a.ChannelSettings(ctx); err == nil {
			return stored.OverlayEnv()
		}
	}
	return ChannelSettingsFromEnv()
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
