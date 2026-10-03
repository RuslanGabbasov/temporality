package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Notification delivery for human interactions
// (docs/triggers-and-escalations.md §6-7, §14). The agent addresses a user;
// the kernel resolves the recipient's profile, picks the preferred channel
// and hands the message to the matching transport adapter. Delivery problems
// never fail the run — they surface as notification.sent events with a
// failed status and a bounded detail.

// ActivityNotifyChannel delivers one ask_human request to the recipient's
// preferred channel. The web channel is a no-op: the UI inbox already
// renders pending questions from the event stream.
const ActivityNotifyChannel = "kernel.notify_channel"

// NotifyChannelRequest is the workflow-side input for one delivery attempt.
type NotifyChannelRequest struct {
	Project        string
	RunID          string
	OperationID    string
	Recipient      string // workspace user id, optionally "user:<id>" prefixed
	ActorID        string // run actor, used when the agent named no recipient
	Question       string
	Context        string
	Options        []string
	TimeoutSeconds int
}

// NotificationDelivery is the outcome recorded on the notification.sent
// event: which transport carried (or would have carried) the message.
type NotificationDelivery struct {
	Recipient string `json:"recipient"`
	Channel   string `json:"channel"`
	Status    string `json:"status"` // delivered | skipped | failed
	Detail    string `json:"detail,omitempty"`
}

// recipientChannels is the profile slice the delivery resolution needs.
type recipientChannels struct {
	Channels  []recipientChannel `json:"channels"`
	Preferred string             `json:"preferred_channel"`
}

type recipientChannel struct {
	Type    string `json:"type"`
	Address string `json:"address"`
	Enabled bool   `json:"enabled"`
}

// NotifyChannel is the activity entry point. It never returns an error:
// delivery failures are part of the result so a broken transport cannot
// take the paused run down with it.
func (a *Activities) NotifyChannel(ctx context.Context, request NotifyChannelRequest) (NotificationDelivery, error) {
	return a.deliverNotification(ctx, request), nil
}

func (a *Activities) deliverNotification(ctx context.Context, request NotifyChannelRequest) NotificationDelivery {
	recipient := normalizeRecipient(request.Recipient)
	if recipient == "" {
		recipient = normalizeRecipient(request.ActorID)
	}
	if recipient == "" {
		return NotificationDelivery{Channel: "web", Status: "skipped", Detail: "no recipient"}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	profile, ok := a.fetchRecipientProfile(ctx, recipient)
	if !ok {
		return NotificationDelivery{Recipient: recipient, Channel: "web", Status: "skipped", Detail: "recipient is not a workspace user"}
	}
	channel, mode := pickChannel(profile, channelConfigured)
	if mode != "web" {
		text := notificationText(request)
		var err error
		switch channel.Type {
		case "matrix":
			err = a.sendMatrix(ctx, channel.Address, text)
		case "telegram":
			err = a.sendTelegram(ctx, channel.Address, text)
		}
		if err != nil {
			return NotificationDelivery{Recipient: recipient, Channel: channel.Type, Status: "failed", Detail: truncate(err.Error(), 300)}
		}
		return NotificationDelivery{Recipient: recipient, Channel: channel.Type, Status: "delivered"}
	}
	detail := "web inbox"
	if profile.Preferred != "" && profile.Preferred != "web" {
		detail = fmt.Sprintf("preferred channel %s unavailable, fell back to web inbox", profile.Preferred)
	}
	return NotificationDelivery{Recipient: recipient, Channel: "web", Status: "delivered", Detail: detail}
}

// normalizeRecipient accepts "user:<id>" spellings from tool arguments.
func normalizeRecipient(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "user:")
	return strings.TrimSpace(value)
}

// fetchRecipientProfile resolves a user id (and, failing that, a user name)
// through the kernel's own workspace API.
func (a *Activities) fetchRecipientProfile(ctx context.Context, recipient string) (recipientChannels, bool) {
	var profile recipientChannels
	if recipient == "" {
		return profile, false
	}
	endpoint := fmt.Sprintf("%s/v1/workspace/users/%s", a.WorkspaceURL, url.PathEscape(recipient))
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err == nil && status == http.StatusOK {
		if json.Unmarshal([]byte(body), &profile) == nil {
			return profile, true
		}
	}
	// The actor of a chat run is the user's display name: fall back to a list
	// lookup before giving up.
	endpoint = fmt.Sprintf("%s/v1/workspace/users", a.WorkspaceURL)
	body, status, err = a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil || status != http.StatusOK {
		return profile, false
	}
	var list struct {
		Users []struct {
			ID        string             `json:"id"`
			Name      string             `json:"name"`
			Channels  []recipientChannel `json:"channels"`
			Preferred string             `json:"preferred_channel"`
		} `json:"users"`
	}
	if json.Unmarshal([]byte(body), &list) != nil {
		return profile, false
	}
	for _, user := range list.Users {
		if user.ID == recipient || user.Name == recipient {
			return recipientChannels{Channels: user.Channels, Preferred: user.Preferred}, true
		}
	}
	return profile, false
}

// channelConfigured reports whether the kernel can send through a transport.
func channelConfigured(channelType string) bool {
	switch channelType {
	case "matrix":
		return strings.TrimSpace(os.Getenv("KERNEL_MATRIX_HOMESERVER")) != "" && strings.TrimSpace(os.Getenv("KERNEL_MATRIX_ACCESS_TOKEN")) != ""
	case "telegram":
		return strings.TrimSpace(os.Getenv("KERNEL_TELEGRAM_BOT_TOKEN")) != ""
	}
	return false
}

// pickChannel applies the recipient policy (§6): preferred channel when it
// is enabled and configured, otherwise the first enabled+configured channel,
// otherwise the web inbox. The second return value is the delivery mode.
func pickChannel(profile recipientChannels, configured func(string) bool) (recipientChannel, string) {
	enabled := make([]recipientChannel, 0, len(profile.Channels))
	for _, channel := range profile.Channels {
		if channel.Enabled && channel.Address != "" && configured(channel.Type) {
			enabled = append(enabled, channel)
		}
	}
	if profile.Preferred != "" && profile.Preferred != "web" {
		for _, channel := range enabled {
			if channel.Type == profile.Preferred {
				return channel, channel.Type
			}
		}
	}
	if len(enabled) > 0 {
		return enabled[0], enabled[0].Type
	}
	return recipientChannel{}, "web"
}

// notificationText renders the human-facing message for external transports.
// The answer itself always happens in the product UI; external channels
// carry the question and a link.
func notificationText(request NotifyChannelRequest) string {
	var builder strings.Builder
	builder.WriteString("Temporality: an agent is waiting for your answer\n\n")
	builder.WriteString(request.Question)
	if request.Context != "" {
		builder.WriteString("\n\nContext: ")
		builder.WriteString(request.Context)
	}
	if len(request.Options) > 0 {
		builder.WriteString("\nOptions: ")
		builder.WriteString(strings.Join(request.Options, " / "))
	}
	builder.WriteString("\n\nProject ")
	builder.WriteString(request.Project)
	builder.WriteString(" · run ")
	builder.WriteString(request.RunID)
	if request.TimeoutSeconds > 0 {
		builder.WriteString(" · waiting ")
		builder.WriteString(strconv.Itoa(request.TimeoutSeconds))
		builder.WriteString("s")
	}
	if uiURL := strings.TrimSpace(os.Getenv("KERNEL_UI_URL")); uiURL != "" {
		builder.WriteString("\nAnswer here: ")
		builder.WriteString(strings.TrimRight(uiURL, "/"))
		builder.WriteString("/agents?project=")
		builder.WriteString(url.QueryEscape(request.Project))
	}
	return builder.String()
}

// sendMatrix posts a text message to a room via the client-server API using
// the kernel's shared bot account.
func (a *Activities) sendMatrix(ctx context.Context, roomID, text string) error {
	homeserver := strings.TrimRight(strings.TrimSpace(os.Getenv("KERNEL_MATRIX_HOMESERVER")), "/")
	accessToken := strings.TrimSpace(os.Getenv("KERNEL_MATRIX_ACCESS_TOKEN"))
	if homeserver == "" || accessToken == "" || roomID == "" {
		return fmt.Errorf("matrix transport is not configured")
	}
	txnID := "temporality-" + transactionFingerprint(roomID, text)
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s", homeserver, url.PathEscape(roomID), txnID)
	payload := map[string]any{"msgtype": "m.text", "body": text}
	body, status, err := a.postJSON(ctx, http.MethodPut, endpoint, payload, map[string]string{"Authorization": "Bearer " + accessToken})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("matrix returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// sendTelegram posts a text message through the Bot API.
func (a *Activities) sendTelegram(ctx context.Context, chatID, text string) error {
	botToken := strings.TrimSpace(os.Getenv("KERNEL_TELEGRAM_BOT_TOKEN"))
	if botToken == "" || chatID == "" {
		return fmt.Errorf("telegram transport is not configured")
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	payload := map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true}
	body, status, err := a.postJSON(ctx, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("telegram returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// postJSON issues a JSON request and returns the body with its status.
func (a *Activities) postJSON(ctx context.Context, method, endpoint string, payload any, headers map[string]string) (string, int, error) {
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
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	raw := make([]byte, 4096)
	n, _ := resp.Body.Read(raw)
	return string(raw[:n]), resp.StatusCode, nil
}

// transactionFingerprint derives a stable-but-unique Matrix txn id so retries
// of the same notification deduplicate server-side.
func transactionFingerprint(roomID, text string) string {
	digest := sha256.Sum256([]byte(roomID + "\x00" + text))
	return hex.EncodeToString(digest[:])[:16]
}
