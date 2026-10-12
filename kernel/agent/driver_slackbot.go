package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// slackBotDriver is the two-way Slack transport (docs/channels.md §9): a
// Slack app bot token posts questions through chat.postMessage, and the
// Events API pushes user messages to the kernel's webhook route. Threads are
// carried as ReplyTo (the question's ts) so future thread-aware matching has
// the data without this driver keeping state. The incoming-webhook slack
// driver stays separate (outbound-only, self-contained addresses).
type slackBotDriver struct{}

func (slackBotDriver) Type() string { return "slackbot" }

// Poller: slack inbound is push — Events API (slackEventsWebhook below).
func (slackBotDriver) Poller() ChannelPoller { return nil }

func (slackBotDriver) Webhook() ChannelWebhook { return slackEventsWebhook{} }

func (slackBotDriver) Spec() ChannelSpec {
	return ChannelSpec{
		Label:             "Slack (bot)",
		AddressHint:       "Slack channel ID, e.g. C0123456789 — the bot must be a member",
		NotConfiguredHint: "Slack bot token + signing secret",
		Capabilities: ChannelCapabilities{
			Text:   true,
			TwoWay: true,
		},
		Settings: []SettingField{
			{Name: "bot_token", Label: "Bot token (xoxb-…)", Secret: true},
			{Name: "signing_secret", Label: "Signing secret", Secret: true},
		},
	}
}

func (slackBotDriver) IsConfigured(settings map[string]string) bool {
	return setting(settings, "bot_token") != "" && setting(settings, "signing_secret") != ""
}

// Self resolves the bot's own Slack user id (auth.test) so the inbound router
// can exclude the account's own posts from reply matching; bot-authored
// events are already skipped in Parse, this is the belt on top.
func (d slackBotDriver) Self(ctx context.Context, client *http.Client, settings map[string]string) (string, error) {
	token := TrimBearerPrefix(setting(settings, "bot_token"))
	if token == "" {
		return "", errors.New("slack bot transport is not configured")
	}
	body, err := slackAPI(ctx, client, http.MethodPost, slackAPIBase+"/auth.test", token, nil)
	if err != nil {
		return "", err
	}
	var decoded struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", err
	}
	if !decoded.OK {
		return "", fmt.Errorf("slack auth.test: %s", decoded.Error)
	}
	return decoded.UserID, nil
}

// slackAPIBase is the Web API root; a package variable so tests can point
// it at a fake server.
var slackAPIBase = "https://slack.com/api"

func (slackBotDriver) SendText(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) error {
	token := TrimBearerPrefix(setting(settings, "bot_token"))
	channelID := strings.TrimSpace(address)
	if token == "" || channelID == "" {
		return errors.New("slack bot transport is not configured")
	}
	// The transport is two-way: replies in the channel route back into the
	// paused run, so say so next to the question.
	payload := map[string]any{"channel": channelID, "text": msg.Body + "\n\nReply in this channel to answer."}
	body, err := slackAPI(ctx, client, http.MethodPost, slackAPIBase+"/chat.postMessage", token, payload)
	if err != nil {
		return err
	}
	var decoded struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	if !decoded.OK {
		return fmt.Errorf("slack chat.postMessage: %s", decoded.Error)
	}
	return nil
}

func (slackBotDriver) SendFile(context.Context, *http.Client, map[string]string, string, OutboundFile) error {
	return ErrChannelFilesUnsupported
}

// slackAPI issues one Slack Web API call and returns the body; Slack answers
// 200 with {"ok": false, "error": …} on failures, so the caller must decode
// the envelope — the status alone proves nothing.
func slackAPI(ctx context.Context, client *http.Client, method, endpoint, token string, payload any) ([]byte, error) {
	encoded, err := json.Marshal(payloadOrEmpty(payload))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("slack returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

func payloadOrEmpty(payload any) any {
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

// slackEventsWebhook adapts the Slack Events API to the ChannelWebhook
// contract: signed JSON envelopes, url_verification handshake, message
// events with thread pointers.
type slackEventsWebhook struct{}

func (slackEventsWebhook) Route() string { return "POST /v1/channels/slackbot/events" }

// slackSignatureSkew bounds how old a signed request may be — the replay
// guard Slack recommends alongside the signature check.
const slackSignatureSkew = 5 * time.Minute

func (slackEventsWebhook) VerifyRequest(r *http.Request, body []byte, settings map[string]string) error {
	secret := setting(settings, "signing_secret")
	if secret == "" {
		return errors.New("slack signing secret is not configured")
	}
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	signature := strings.TrimSpace(r.Header.Get("X-Slack-Signature"))
	if timestamp == "" || signature == "" {
		return errors.New("missing slack signature headers")
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("malformed slack timestamp")
	}
	age := time.Since(time.Unix(seconds, 0))
	if age < -slackSignatureSkew || age > slackSignatureSkew {
		return errors.New("slack signature timestamp outside the freshness window")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return errors.New("slack signature mismatch")
	}
	return nil
}

// slackEventsEnvelope is the Events API payload surface this driver reads.
type slackEventsEnvelope struct {
	Type  string `json:"type"`
	Event struct {
		Type     string `json:"type"`
		Subtype  string `json:"subtype"`
		Text     string `json:"text"`
		User     string `json:"user"`
		BotID    string `json:"bot_id"`
		Channel  string `json:"channel"`
		TS       string `json:"ts"`
		ThreadTS string `json:"thread_ts"`
	} `json:"event"`
	EventID string `json:"event_id"`
}

func (slackEventsWebhook) Parse(r *http.Request, body []byte) []InboundMessage {
	var envelope slackEventsEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	switch envelope.Type {
	case "url_verification":
		return nil // handshake — Respond echoes the challenge
	case "event_callback":
	default:
		return nil // retries/acks/foreign envelopes carry no messages
	}
	event := envelope.Event
	if event.Type != "message" || event.User == "" || event.BotID != "" || event.Subtype != "" {
		// Bot posts (including this account's own questions), edits and
		// channel joins are not human answers — skip them before matching.
		return nil
	}
	timestamp := time.Now().UTC()
	if parsed, err := strconv.ParseFloat(event.TS, 64); err == nil {
		timestamp = time.Unix(int64(parsed), 0).UTC()
	}
	id := envelope.EventID
	if id == "" {
		id = event.TS
	}
	return []InboundMessage{{
		ID:        id,
		Channel:   "slackbot",
		ChatID:    event.Channel,
		Sender:    event.User,
		Text:      event.Text,
		Timestamp: timestamp,
		ReplyTo:   event.ThreadTS,
	}}
}

func (slackEventsWebhook) Respond(w http.ResponseWriter, r *http.Request, body []byte) {
	var envelope slackEventsEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Type == "url_verification" {
		var challenge struct {
			Challenge string `json:"challenge"`
		}
		_ = json.Unmarshal(body, &challenge)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(challenge.Challenge))
		return
	}
	w.WriteHeader(http.StatusOK)
}
