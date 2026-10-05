package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
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
	Recipient      string // logical recipient: user:id, role:x, org:unit, project_owner, or ''
	ActorID        string // run actor, used when the agent named no recipient
	AgentID        string
	Question       string
	Context        string
	Options        []string
	TimeoutSeconds int
	TimeoutPolicy  string
	// ExecutionIdentityID scopes whom this run may address (§30); empty for
	// manual runs without an identity.
	ExecutionIdentityID string
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

// validTimeoutPolicy reports whether s is one of the §33 policies. The
// empty string is valid (the workflow defaults it to fallback).
func validTimeoutPolicy(s string) bool {
	switch s {
	case "", "fail", "retry", "fallback", "escalate", "cancel":
		return true
	}
	return false
}

// ActivityCloseHumanRequest closes a human_request row when its wait ends
// (docs/org-structure.md §28): answered, cancelled or expired. Called from
// the workflow after awaitApproval resolves; the lazy sweep in the list
// endpoint is the safety net.
const ActivityCloseHumanRequest = "kernel.close_human_request"

// CloseHumanRequestInput is the workflow-side input for the close activity.
type CloseHumanRequestInput struct {
	OperationID string
	Status      string // answered | cancelled | expired
	Response    string
	ActorID     string
}

// CloseHumanRequest never fails the run: a storage problem leaves the row to
// the lazy expiry sweep (expired) or keeps it delivered (answered rows keep
// pointing at the resolved answer in events).
func (a *Activities) CloseHumanRequest(ctx context.Context, input CloseHumanRequestInput) error {
	endpoint := fmt.Sprintf("%s/v1/workspace/human-requests/%s/close", a.WorkspaceURL, url.PathEscape(input.OperationID))
	body := map[string]any{"status": input.Status, "response": input.Response, "actor_id": input.ActorID}
	_, _, err := a.workspaceDo(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		slog.Error("close human request", "operation", input.OperationID, "status", input.Status, "error", err)
	}
	return nil
}

func (a *Activities) deliverNotification(ctx context.Context, request NotifyChannelRequest) NotificationDelivery {
	delivery := a.resolveAndDeliver(ctx, request)
	// Persist the request as a first-class entity (§28): pending → delivered
	// (or rejected when §30 forbids the recipient). Delivery problems never
	// fail the run — they are part of the recorded outcome.
	a.recordHumanRequest(ctx, request, delivery)
	return delivery
}

// resolveAndDeliver runs recipient resolution (§26) and channel delivery.
func (a *Activities) resolveAndDeliver(ctx context.Context, request NotifyChannelRequest) NotificationDelivery {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	denied := func(reason string) NotificationDelivery {
		return NotificationDelivery{Recipient: strings.TrimPrefix(request.Recipient, "user:"), Channel: "web", Status: "rejected", Detail: reason}
	}
	users, ok := a.fetchWorkspaceUsers(ctx)
	if !ok {
		return denied("workspace user list unavailable")
	}
	target, found := ResolveRecipient(request.Recipient, request.ActorID, users)
	if !found {
		return denied("recipient could not be resolved to a workspace user")
	}
	// §30: an execution identity restricts whom the run may address. The
	// check runs at ask time with the identity's current human_targets.
	if request.ExecutionIdentityID != "" {
		allowed, detail := a.identityAllowsHuman(ctx, request.ExecutionIdentityID, target.ID)
		if !allowed {
			return denied(detail)
		}
	}
	channel, mode := pickChannel(recipientChannels{Channels: target.Channels, Preferred: target.Preferred}, channelConfigured)
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
			return NotificationDelivery{Recipient: target.ID, Channel: channel.Type, Status: "failed", Detail: truncate(err.Error(), 300)}
		}
		return NotificationDelivery{Recipient: target.ID, Channel: channel.Type, Status: "delivered"}
	}
	detail := "web inbox"
	if target.Preferred != "" && target.Preferred != "web" {
		detail = fmt.Sprintf("preferred channel %s unavailable, fell back to web inbox", target.Preferred)
	}
	return NotificationDelivery{Recipient: target.ID, Channel: "web", Status: "delivered", Detail: detail}
}

// recordHumanRequest upserts the human_request row with the delivery
// outcome. Best-effort: storage problems surface in logs but never break the
// paused run — the entity tracks the question, not the delivery logistics.
func (a *Activities) recordHumanRequest(ctx context.Context, request NotifyChannelRequest, delivery NotificationDelivery) {
	var expiresAt any
	if request.TimeoutSeconds > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(request.TimeoutSeconds) * time.Second)
	}
	if request.Options == nil {
		request.Options = []string{}
	}
	body := map[string]any{
		"id":                    request.OperationID,
		"run_id":                request.RunID,
		"project_id":            request.Project,
		"agent_id":              request.AgentID,
		"recipient":             request.Recipient,
		"resolved_user":         delivery.Recipient,
		"question":              request.Question,
		"context":               request.Context,
		"options":               request.Options,
		"status":                delivery.Status,
		"channel":               delivery.Channel,
		"timeout_seconds":       request.TimeoutSeconds,
		"timeout_policy":        request.TimeoutPolicy,
		"execution_identity_id": request.ExecutionIdentityID,
		"expires_at":            expiresAt,
	}
	endpoint := fmt.Sprintf("%s/v1/workspace/human-requests", a.WorkspaceURL)
	if _, _, err := a.workspaceDo(ctx, http.MethodPost, endpoint, body); err != nil {
		slog.Error("record human request", "operation", request.OperationID, "error", err)
	}
}

// WorkspaceUser is the resolution input: one user's identity, position and
// communication channels.
type WorkspaceUser struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Role      string             `json:"role"`
	Active    bool               `json:"active"`
	OrgUnitID string             `json:"org_unit_id"`
	Channels  []recipientChannel `json:"channels"`
	Preferred string             `json:"preferred_channel"`
}

// fetchWorkspaceUsers pulls the resolution input once per delivery.
func (a *Activities) fetchWorkspaceUsers(ctx context.Context) ([]WorkspaceUser, bool) {
	endpoint := fmt.Sprintf("%s/v1/workspace/users", a.WorkspaceURL)
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil || status != http.StatusOK {
		return nil, false
	}
	var list struct {
		Users []WorkspaceUser `json:"users"`
	}
	if json.Unmarshal([]byte(body), &list) != nil {
		return nil, false
	}
	return list.Users, true
}

// identityAllowsHuman checks the identity's human_targets (§30): "*" or an
// empty list allows anyone; otherwise the resolved user must be listed. Fails
// closed when the policy source is unavailable.
func (a *Activities) identityAllowsHuman(ctx context.Context, identityID, userID string) (bool, string) {
	endpoint := fmt.Sprintf("%s/v1/workspace/execution-identities", a.WorkspaceURL)
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil || status != http.StatusOK {
		return false, fmt.Sprintf("execution identity %q could not be loaded", identityID)
	}
	var list struct {
		Identities []struct {
			ID           string   `json:"id"`
			HumanTargets []string `json:"human_targets"`
		} `json:"identities"`
	}
	if json.Unmarshal([]byte(body), &list) != nil {
		return false, fmt.Sprintf("execution identity %q could not be parsed", identityID)
	}
	for _, identity := range list.Identities {
		if identity.ID != identityID {
			continue
		}
		if len(identity.HumanTargets) == 0 {
			return true, ""
		}
		for _, allowed := range identity.HumanTargets {
			if allowed == "*" || allowed == userID {
				return true, ""
			}
		}
		return false, fmt.Sprintf("execution identity %q may not address user %q", identityID, userID)
	}
	return false, fmt.Sprintf("execution identity %q no longer exists", identityID)
}

// ResolveRecipient maps a logical recipient (§26) to a concrete workspace
// user: explicit user id/name, role:, org:, project_owner, or the run actor
// as the default. Tie-breaking picks the first active match — deterministic
// for a given users list.
func ResolveRecipient(recipient, actor string, users []WorkspaceUser) (WorkspaceUser, bool) {
	normalize := func(raw string) string {
		value := strings.TrimSpace(raw)
		value = strings.TrimPrefix(value, "user:")
		return strings.TrimSpace(value)
	}
	byName := func(id string) (WorkspaceUser, bool) {
		for _, u := range users {
			if u.Active && (u.ID == id || u.Name == id) {
				return u, true
			}
		}
		return WorkspaceUser{}, false
	}
	switch {
	case strings.HasPrefix(recipient, "role:"):
		role := strings.TrimSpace(strings.TrimPrefix(recipient, "role:"))
		for _, u := range users {
			if u.Active && u.Role == role {
				return u, true
			}
		}
		return WorkspaceUser{}, false
	case strings.HasPrefix(recipient, "org:"):
		unit := strings.TrimSpace(strings.TrimPrefix(recipient, "org:"))
		for _, u := range users {
			if u.Active && u.OrgUnitID == unit {
				return u, true
			}
		}
		return WorkspaceUser{}, false
	case recipient == "project_owner":
		// Projects carry no owner field yet (docs/org-structure.md §14); the
		// installation admin is the steward until explicit ownership lands.
		for _, u := range users {
			if u.Active && u.Role == "admin" {
				return u, true
			}
		}
		return WorkspaceUser{}, false
	case normalize(recipient) != "":
		return byName(normalize(recipient))
	default:
		if id := normalize(actor); id != "" {
			if u, ok := byName(id); ok {
				return u, true
			}
		}
		return WorkspaceUser{}, false
	}
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
