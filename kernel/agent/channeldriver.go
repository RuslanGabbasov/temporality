package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ChannelDriver is the single extension point of the delivery layer
// (docs/channels.md §3): one implementation per transport, registered in
// channelDrivers below. The kernel core never branches on a channel type —
// it resolves the driver and hands over the channel's settings map plus the
// normalized outbound payload. Everything channel-specific (protocol,
// payload shape, idempotency quirks, API limits) lives inside the driver.
type ChannelDriver interface {
	// Type is the registry slug ("matrix", "telegram", "slack", "webhook").
	Type() string
	// Spec describes the channel for the kernel and the UI: label, address
	// hint, capabilities, and the settings schema the admin form renders.
	Spec() ChannelSpec
	// IsConfigured reports whether the settings map carries everything the
	// transport needs. Self-contained transports (slack, generic webhook)
	// carry it all in the address and are always ready.
	IsConfigured(settings map[string]string) bool
	// SendText delivers one rendered question to the channel address.
	SendText(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) error
	// SendFile delivers one attachment; channels without FilesOut return
	// ErrChannelFilesUnsupported. The kernel checks capabilities first, so
	// reaching that error means a capability/spec mismatch (a bug).
	SendFile(ctx context.Context, client *http.Client, settings map[string]string, address string, file OutboundFile) error
	// Poller returns the pull-model inbound adapter, or nil when the channel
	// is outbound-only (slack/webhook today). One loop per poller runs in the
	// kernel binary; the adapter is stateless — the loop owns the cursor.
	Poller() ChannelPoller
}

// ErrChannelFilesUnsupported is returned by drivers whose channels cannot
// carry attachments.
var ErrChannelFilesUnsupported = errors.New("channel does not deliver files")

// ChannelSpec is the driver's self-description for the kernel and UI.
type ChannelSpec struct {
	// Label is the human name shown in pickers.
	Label string
	// AddressHint explains what the address field carries for this transport.
	AddressHint string
	// NotConfiguredHint names what the admin has to provide, so the UI can
	// link the missing piece to the transport settings screen.
	NotConfiguredHint string
	// Capabilities declare what the channel can do; the kernel uses them to
	// route (files) and annotate (two-way hint) without knowing the channel.
	Capabilities ChannelCapabilities
	// Settings is the channel's settings schema; the admin form renders from
	// it, so a new channel needs no UI work.
	Settings []SettingField
}

// ChannelCapabilities is the feature matrix of one channel type.
type ChannelCapabilities struct {
	// Text: can carry rendered text questions. Every driver today.
	Text bool
	// TwoWay: a reply in the channel routes back into the paused run, so
	// deliveries carry a "reply here" hint.
	TwoWay bool
	// FilesOut: send_file can push attachments through this channel.
	FilesOut bool
	// FilesIn: inbound delivery accepts user attachments (FilesIn channels
	// implement a poller with file fetch).
	FilesIn bool
}

// SettingField is one admin-editable channel setting.
type SettingField struct {
	// Name is the payload key and the env suffix:
	// KERNEL_CHANNEL__<TYPE>__<NAME>.
	Name string
	// Label is the human name for the admin form.
	Label string
	// Secret fields are never echoed back — set + SecretHint only.
	Secret bool
}

// OutboundText is the channel-neutral question payload. Body is the fully
// rendered notification for text transports; the envelope fields let
// structured transports (the generic webhook) build their own payload shape.
type OutboundText struct {
	Body           string
	Question       string
	Context        string
	Options        []string
	Project        string
	RunID          string
	OperationID    string
	AgentID        string
	TimeoutSeconds int
	TimeoutPolicy  string
	// AnswerURL is the ready answer link ("" when the UI URL is unset).
	AnswerURL string
}

// OutboundFile is one attachment pushed through a FilesOut channel.
type OutboundFile struct {
	Name    string
	Note    string
	Payload []byte
}

// channelDrivers lists the transports in preference order — the order the
// recipient's channel picker falls back through. Adding a channel is one
// entry here plus the driver implementation; everything else (recipient
// resolution, channel pick, send_file, settings, UI) is generic.
//
// Drivers are stateless: per-delivery state (HTTP client, settings) arrives
// as arguments, so the registry is safe to share across activities.
var channelDrivers = []ChannelDriver{
	&matrixDriver{},
	&telegramDriver{},
	&slackDriver{},
	&webhookDriver{},
}

// channelDriverByType resolves a registry slug; nil when unknown.
func channelDriverByType(channelType string) ChannelDriver {
	for _, driver := range channelDrivers {
		if driver.Type() == channelType {
			return driver
		}
	}
	return nil
}

// ChannelDrivers exposes the ordered registry — the admin settings API walks
// it to render the per-channel schema, the same list delivery dispatches
// through.
func ChannelDrivers() []ChannelDriver { return channelDrivers }

// setting reads one trimmed value from a channel settings map.
func setting(settings map[string]string, key string) string {
	return strings.TrimSpace(settings[key])
}

// postJSON issues a JSON request through the given client and returns the
// body with its status — the shared plumbing of the HTTP-based drivers.
func postJSON(ctx context.Context, client *http.Client, method, endpoint string, payload any, headers map[string]string) (string, int, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	raw := make([]byte, 4096)
	n, _ := resp.Body.Read(raw)
	return string(raw[:n]), resp.StatusCode, nil
}

// channelSettingUnconfiguredDetail is the unavailable reason send_file
// reports when a channel is enabled for the recipient but the kernel lacks
// the transport credentials.
func channelSettingUnconfiguredDetail(channelType string) string {
	return fmt.Sprintf("%s transport is not configured", channelType)
}
