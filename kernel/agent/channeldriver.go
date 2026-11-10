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
	// does not poll (push channels and outbound-only transports). One loop per
	// poller runs in the kernel binary; the adapter is stateless — the loop
	// owns the cursor.
	Poller() ChannelPoller
	// Webhook returns the push-model inbound adapter, or nil for pull and
	// outbound-only channels (docs/channels.md §3). A driver implements
	// exactly one inbound model; the kernel registers the webhook's route and
	// feeds parsed messages into the shared inbound pipeline.
	Webhook() ChannelWebhook
}

// ErrChannelFilesUnsupported is returned by drivers whose channels cannot
// carry attachments.
var ErrChannelFilesUnsupported = errors.New("channel does not deliver files")

// ThreadedTextPoster is the optional driver extension for thread-capable
// transports: same delivery contract as SendText, but it returns the posted
// message's channel id (slack ts) so the kernel can pin the conversation
// thread — follow-ups join it and threaded replies match their ask exactly
// (docs/channels.md §9). The kernel uses it when the driver implements it;
// SendText stays the mandatory baseline.
type ThreadedTextPoster interface {
	SendTextThreaded(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) (postedID string, err error)
}

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
	// Threads: the channel carries conversation threads (slack threads,
	// mattermost root posts). Questions can join a thread via OutboundText.
	// Thread and threaded replies carry ReplyTo — see docs/channels.md §9.
	Threads bool
	// FilesOut: send_file can push attachments through this channel.
	FilesOut bool
	// FilesIn: inbound delivery accepts user attachments (FilesIn channels
	// implement a poller with file fetch).
	FilesIn bool
	// Interactive: the channel renders Options as tappable buttons and
	// routes the presses back (telegram inline keyboard, docs/interactive-
	// HITL.md Этап D). Text remains the universal fallback.
	Interactive bool
	// Rich: the channel renders structured markdown (headings, tables,
	// details) beyond the plain-text baseline (telegram sendRichMessage).
	Rich bool
	// Widgets: the channel carries interactive HTML widgets derived from a
	// form schema (Bot API 10.4 sendWidget, docs/interactive-HITL.md).
	Widgets bool
	// Drafts: the channel streams ephemeral previews of content being
	// generated (telegram sendMessageDraft).
	Drafts bool
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
	// Thread posts this message into an existing conversation thread when
	// the transport carries threads (Capabilities.Threads); "" = top-level.
	// The kernel fills it from the previous ask's thread_ref so follow-up
	// questions of one run stay in one conversation (docs/channels.md §9).
	Thread string
	// Lang picks the language of transport-appended canned text — "" and
	// unknown values fall back to English. The dialog language is detected
	// from the user's message (agent.DetectLang).
	Lang string
	// Schema is the optional form (docs/interactive-HITL.md §7) attached to
	// the ask: channels that can render structured questions (matrix HTML
	// cards, telegram widgets) build a field table from it; text-only
	// transports get the field list in the plain body. nil = plain question.
	Schema json.RawMessage
	// FormURL is the hosted one-click form for this ask (docs/interactive-
	// HITL.md §6.2): a scoped-secret page answerable from any browser, no
	// harness account. Empty when the kernel has no public URL — channels fall
	// back to the in-harness answer link.
	FormURL string
	// Buttons are the interactive answer shortcuts (Capabilities.Interactive):
	// each Token is the kernel's opaque routing key the channel delivers back
	// verbatim on a press (telegram callback_data, 1-64 bytes). The kernel
	// keeps tokens short and degrades to the plain body when a channel cannot
	// carry them. nil = text-only question.
	Buttons [][]OutboundButton
}

// OutboundButton is one interactive keyboard button: Text is the label,
// Token the routing key returned by the channel when the user taps it.
type OutboundButton struct {
	Text  string
	Token string
}

// Optional driver extensions. Each is resolved by the kernel through a type
// assertion, never by a channel-type branch: a driver implements what its
// transport can carry, and every interactive surface keeps the text
// baseline as the fallback (docs/channels.md §3).

// RichTextPoster is the structured-markdown extension: headings, tables and
// details render natively instead of flat text. Returns the posted message's
// channel id when the transport reports one (best-effort, "" otherwise).
type RichTextPoster interface {
	SendTextRich(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) (postedID string, err error)
}

// CallbackResponder acknowledges a button press (telegram
// answerCallbackQuery): the tap receipt the user sees until the real answer
// lands. text renders as a toast; empty text just clears the spinner.
type CallbackResponder interface {
	AnswerCallback(ctx context.Context, client *http.Client, settings map[string]string, queryID, text string) error
}

// MessageEditor rewrites a previously posted message — resolving an ask in
// place (buttons out, verdict line in) instead of posting a follow-up.
type MessageEditor interface {
	EditMessage(ctx context.Context, client *http.Client, settings map[string]string, address, messageID, text string, buttons [][]OutboundButton) error
}

// OutboundWidget carries the channel-neutral widget model (widgetform.go):
// the kernel builds semantics from the ask's schema, the driver renders it
// into the channel's widget protocol and carries the interaction back.
type OutboundWidget struct {
	// Model is the WidgetModel JSON: question, fields, current state.
	Model json.RawMessage
	// FormURL attaches the hosted-form fallback button to the widget
	// message (long values and restricted chats degrade to it).
	FormURL string
	// Lang for driver-appended canned labels.
	Lang string
}

// WidgetPoster is the interactive HTML-widget extension (Bot API 10.4
// sendWidget): a schema ask renders as a form the user fills inside the
// chat. SendWidget returns the posted message's channel id — the kernel
// pins it so widget queries resolve back to the ask.
type WidgetPoster interface {
	SendWidget(ctx context.Context, client *http.Client, settings map[string]string, address string, w OutboundWidget) (messageID string, err error)
	// EditWidgetData pushes a new widget model (state update) into the
	// message's widget.
	EditWidgetData(ctx context.Context, client *http.Client, settings map[string]string, address, messageID, model string) error
	// AnswerWidget acknowledges one widget query within the transport's
	// answer window; data rides to that widget instance only.
	AnswerWidget(ctx context.Context, client *http.Client, settings map[string]string, queryID, data string) error
}

// DraftStreamer streams an ephemeral preview of a message being generated
// (telegram sendMessageDraft): the preview lives ~30 seconds and is replaced
// by the real message, never persisted by the transport.
type DraftStreamer interface {
	SendDraft(ctx context.Context, client *http.Client, settings map[string]string, address string, draftID int64, text string) error
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
	&slackBotDriver{},
	&mattermostDriver{},
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

// ChannelDriverByTypePublic is the server-package face of the registry:
// readiness checks probe whether a transport is configured without reaching
// into driver internals.
func ChannelDriverByTypePublic(channelType string) ChannelDriver {
	return channelDriverByType(channelType)
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
