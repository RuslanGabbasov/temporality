package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
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
	// Schema is the optional form the human fills instead of free text
	// (docs/interactive-HITL.md §7): a bounded JSON Schema object delivered
	// with the question and enforced server-side on submission. nil = plain
	// question (options + free text).
	Schema json.RawMessage
	// FormURL is the minted hosted-form link for this ask (docs/interactive-
	// HITL.md §6.2) — filled by the notify activity before the channel pick;
	// outboundText forwards it so channels render the fill-the-form button.
	FormURL string
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
	// ThreadRef is the conversation thread the question landed in on
	// thread-capable transports (slack ts; docs/channels.md §9): the posted
	// message id, or the thread a follow-up joined. Empty otherwise.
	ThreadRef string `json:"thread_ref,omitempty"`
	// DeliveredTo is the team fan-out breakdown (docs/inbound-messages.md §8):
	// one entry per unit member. Empty for single-recipient requests.
	DeliveredTo []DeliveryTarget `json:"delivered_to,omitempty"`
}

// DeliveryTarget is one member's outcome inside an org: fan-out.
type DeliveryTarget struct {
	UserID  string `json:"user_id"`
	Channel string `json:"channel"`
	Status  string `json:"status"` // delivered | skipped | failed
	Detail  string `json:"detail,omitempty"`
	// ThreadRef is this member's conversation thread on thread-capable
	// transports — slack-style threads are per posted copy.
	ThreadRef string `json:"thread_ref,omitempty"`
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
	// SenderID is the owner's own identity in this channel (mxid for matrix):
	// the input for DM resolution when the address is left empty.
	SenderID string `json:"sender_id"`
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
// Team recipients (org:<unit>, docs/inbound-messages.md §8) take the fan-out
// path: every active member gets their own copy, any member may answer.
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
	if strings.HasPrefix(request.Recipient, "org:") {
		return a.deliverToUnit(ctx, request, users, strings.TrimSpace(strings.TrimPrefix(request.Recipient, "org:")))
	}
	// Job-title fan-out (docs/org-structure.md §3.3): `title:qa` reaches every
	// active user whose job title matches the substring (case-insensitive) —
	// the natural address for «позови всех тестировщиков» without a unit that
	// mirrors the function.
	if strings.HasPrefix(request.Recipient, "title:") {
		return a.deliverToJobTitle(ctx, request, users, strings.TrimSpace(strings.TrimPrefix(request.Recipient, "title:")))
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
	settings := a.transports(ctx)
	channel, mode := pickChannel(recipientChannels{Channels: target.Channels, Preferred: target.Preferred}, settings.Configured)
	// Hosted form link (docs/interactive-HITL.md §6.2): a schema ask mints a
	// scoped-secret page URL — one per ask, before the channel pick, so every
	// delivery (channel retries included) carries the same link. The base is
	// the same public UI URL the notification links already use; the page and
	// the answer API live behind /kernel-api on the kernel (the form routes
	// are token-authed, no harness login).
	if len(request.Schema) > 0 && strings.TrimSpace(settings.UIURL) != "" {
		if link, err := a.mintFormLink(ctx, request); err == nil {
			request.FormURL = strings.TrimRight(settings.UIURL, "/") + "/kernel-api/forms/" + link
		} else {
			slog.Warn("notify: mint form link", "operation", request.OperationID, "error", err.Error())
		}
	}
	if mode != "web" {
		// An empty address means the transport resolves the conversation itself
		// (matrix: the user's DM room with the bot, from their registered mxid).
		deliveryAddress := channel.Address
		if strings.TrimSpace(deliveryAddress) == "" {
			resolved, rErr := a.resolveChannelAddress(ctx, channel.Type, "", channel.SenderID)
			if rErr != nil {
				return NotificationDelivery{Recipient: target.ID, Channel: channel.Type, Status: "failed", Detail: truncate(rErr.Error(), 300)}
			}
			deliveryAddress = resolved
		}
		// Follow-ups of the same run join the previous ask's conversation
		// thread (docs/channels.md §9) — new threads only for new runs.
		thread := a.channelThread(ctx, channel.Type, request, target.ID)
		postedID, err := a.sendToChannel(ctx, channel.Type, deliveryAddress, request, settings, thread)
		if err != nil {
			return NotificationDelivery{Recipient: target.ID, Channel: channel.Type, Status: "failed", Detail: truncate(err.Error(), 300)}
		}
		return NotificationDelivery{Recipient: target.ID, Channel: channel.Type, Status: "delivered", ThreadRef: conversationThreadRef(thread, postedID)}
	}
	detail := "web inbox"
	if target.Preferred != "" && target.Preferred != "web" {
		detail = fmt.Sprintf("preferred channel %s unavailable, fell back to web inbox", target.Preferred)
	}
	return NotificationDelivery{Recipient: target.ID, Channel: "web", Status: "delivered", Detail: detail}
}

// deliverToJobTitle fans one ask out to every active user whose job title
// matches the substring (case-insensitive) — the group address for functions
// that cross-cut org units («позови всех тестировщиков»). Deterministic order:
// role seniority, then name.
func (a *Activities) deliverToJobTitle(ctx context.Context, request NotifyChannelRequest, users []WorkspaceUser, title string) NotificationDelivery {
	if title == "" {
		return NotificationDelivery{Recipient: "title:", Channel: "web", Status: "rejected", Detail: "empty job title in recipient"}
	}
	settings := a.transports(ctx)
	needle := strings.ToLower(title)
	members := make([]WorkspaceUser, 0, 4)
	for _, u := range users {
		if u.Active && strings.Contains(strings.ToLower(u.JobTitle), needle) {
			members = append(members, u)
		}
	}
	if len(members) == 0 {
		return NotificationDelivery{Recipient: "title:" + title, Channel: "web", Status: "rejected", Detail: fmt.Sprintf("no active user carries job title matching %q", title)}
	}
	sort.SliceStable(members, func(i, j int) bool {
		pi, pj := rolePriority(members[i].Role), rolePriority(members[j].Role)
		if pi != pj {
			return pi < pj
		}
		return members[i].Name < members[j].Name
	})
	targets := make([]DeliveryTarget, 0, len(members))
	delivered := 0
	for _, member := range members {
		if request.ExecutionIdentityID != "" {
			allowed, detail := a.identityAllowsHuman(ctx, request.ExecutionIdentityID, member.ID)
			if !allowed {
				targets = append(targets, DeliveryTarget{UserID: member.ID, Channel: "web", Status: "skipped", Detail: detail})
				continue
			}
		}
		channel, mode := pickChannel(recipientChannels{Channels: member.Channels, Preferred: member.Preferred}, settings.Configured)
		if mode == "web" {
			targets = append(targets, DeliveryTarget{UserID: member.ID, Channel: "web", Status: "delivered", Detail: "web inbox"})
			delivered++
			continue
		}
		deliveryAddress := channel.Address
		if strings.TrimSpace(deliveryAddress) == "" {
			resolved, rErr := a.resolveChannelAddress(ctx, channel.Type, "", channel.SenderID)
			if rErr != nil {
				targets = append(targets, DeliveryTarget{UserID: member.ID, Channel: channel.Type, Status: "failed", Detail: truncate(rErr.Error(), 300)})
				continue
			}
			deliveryAddress = resolved
		}
		thread := a.channelThread(ctx, channel.Type, request, member.ID)
		postedID, sendErr := a.sendToChannel(ctx, channel.Type, deliveryAddress, request, settings, thread)
		if sendErr != nil {
			targets = append(targets, DeliveryTarget{UserID: member.ID, Channel: channel.Type, Status: "failed", Detail: truncate(sendErr.Error(), 300)})
			continue
		}
		targets = append(targets, DeliveryTarget{UserID: member.ID, Channel: channel.Type, Status: "delivered", ThreadRef: conversationThreadRef(thread, postedID)})
		delivered++
	}
	status := "failed"
	if delivered > 0 {
		status = "delivered"
	}
	return NotificationDelivery{
		Recipient:   "title:" + title,
		Channel:     "multi",
		Status:      status,
		Detail:      fmt.Sprintf("%d/%d users with job title ~%q reached", delivered, len(members), title),
		DeliveredTo: targets,
	}
}

// deliverToUnit fans one ask_human out to every active member of the unit
// (docs/inbound-messages.md §8). Direct members only — the subtree inherits
// visibility, not team membership. The unit's own head leads the delivery
// order (nearest explicit manager first), then role seniority, then name —
// deterministic for a given directory snapshot. "Head first" is priority in
// the list, not authority over the answer: whoever replies first closes the
// request.
func (a *Activities) deliverToUnit(ctx context.Context, request NotifyChannelRequest, users []WorkspaceUser, unitID string) NotificationDelivery {
	if unitID == "" {
		return NotificationDelivery{Recipient: unitID, Channel: "web", Status: "rejected", Detail: "empty org unit in recipient"}
	}
	settings := a.transports(ctx)
	// Members: active users placed directly in the unit.
	members := make([]WorkspaceUser, 0, len(users))
	unitKnown := false
	for _, u := range users {
		if u.OrgUnitID == unitID {
			unitKnown = true
			if u.Active {
				members = append(members, u)
			}
		}
	}
	if !unitKnown {
		return NotificationDelivery{Recipient: unitID, Channel: "web", Status: "rejected", Detail: fmt.Sprintf("org unit %q not found", unitID)}
	}
	if len(members) == 0 {
		return NotificationDelivery{Recipient: unitID, Channel: "web", Status: "rejected", Detail: fmt.Sprintf("org unit %q has no active members", unitID)}
	}
	// Head first: the unit's own head when they are a member, else nothing —
	// an ancestor's head is not part of this team's delivery list.
	headFirst := ""
	if units, ok := a.fetchOrgUnits(ctx); ok {
		for _, unit := range units {
			if unit.ID == unitID {
				headFirst = unit.HeadUserID
				break
			}
		}
	}
	sort.SliceStable(members, func(i, j int) bool {
		if (members[i].ID == headFirst) != (members[j].ID == headFirst) {
			return members[i].ID == headFirst
		}
		pi, pj := rolePriority(members[i].Role), rolePriority(members[j].Role)
		if pi != pj {
			return pi < pj
		}
		return members[i].Name < members[j].Name
	})

	targets := make([]DeliveryTarget, 0, len(members))
	delivered := 0
	for _, member := range members {
		if request.ExecutionIdentityID != "" {
			// §30 applies per member: an identity may not address someone → their
			// copy is skipped, the rest of the team still gets asked.
			allowed, detail := a.identityAllowsHuman(ctx, request.ExecutionIdentityID, member.ID)
			if !allowed {
				targets = append(targets, DeliveryTarget{UserID: member.ID, Channel: "web", Status: "skipped", Detail: detail})
				continue
			}
		}
		channel, mode := pickChannel(recipientChannels{Channels: member.Channels, Preferred: member.Preferred}, settings.Configured)
		if mode != "web" {
			// Same empty-address DM resolution as the direct path above.
			deliveryAddress := channel.Address
			if strings.TrimSpace(deliveryAddress) == "" {
				resolved, rErr := a.resolveChannelAddress(ctx, channel.Type, "", channel.SenderID)
				if rErr != nil {
					targets = append(targets, DeliveryTarget{UserID: member.ID, Channel: channel.Type, Status: "failed", Detail: truncate(rErr.Error(), 200)})
					continue
				}
				deliveryAddress = resolved
			}
			// Per-member thread: every copy opens (or joins) its own
			// conversation thread in the member's chat.
			thread := a.channelThread(ctx, channel.Type, request, member.ID)
			postedID, err := a.sendToChannel(ctx, channel.Type, deliveryAddress, request, settings, thread)
			if err != nil {
				targets = append(targets, DeliveryTarget{UserID: member.ID, Channel: channel.Type, Status: "failed", Detail: truncate(err.Error(), 200)})
				continue
			}
			targets = append(targets, DeliveryTarget{UserID: member.ID, Channel: channel.Type, Status: "delivered", ThreadRef: conversationThreadRef(thread, postedID)})
			delivered++
			continue
		}
		targets = append(targets, DeliveryTarget{UserID: member.ID, Channel: "web", Status: "delivered", Detail: "web inbox"})
		delivered++
	}
	status := "failed"
	if delivered > 0 {
		status = "delivered"
	}
	return NotificationDelivery{
		Recipient:   unitID,
		Channel:     "multi",
		Status:      status,
		Detail:      fmt.Sprintf("%d/%d unit members reached", delivered, len(members)),
		DeliveredTo: targets,
	}
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
	// Team fan-out: resolved_user/channel mirror the first reached member for
	// backward-compatible display, while the delivery rows carry every member.
	// thread_ref follows the same mirror — one conversation anchor per request.
	resolvedUser, channel := delivery.Recipient, delivery.Channel
	threadRef := delivery.ThreadRef
	var deliveries []map[string]any
	firstPicked := false
	for _, target := range delivery.DeliveredTo {
		if target.Status == "delivered" && !firstPicked {
			resolvedUser, channel = target.UserID, target.Channel
			threadRef = target.ThreadRef
			firstPicked = true
		}
		status := target.Status
		if status == "skipped" {
			status = "failed" // no copy was sent — nothing to answer
		}
		entry := map[string]any{"user_id": target.UserID, "channel": target.Channel, "status": status}
		if target.Detail != "" && status == "failed" {
			entry["detail"] = target.Detail
		}
		deliveries = append(deliveries, entry)
	}
	body := map[string]any{
		"id":                    request.OperationID,
		"run_id":                request.RunID,
		"project_id":            request.Project,
		"agent_id":              request.AgentID,
		"recipient":             request.Recipient,
		"resolved_user":         resolvedUser,
		"question":              request.Question,
		"context":               request.Context,
		"options":               request.Options,
		"status":                delivery.Status,
		"channel":               channel,
		"thread_ref":            threadRef,
		"timeout_seconds":       request.TimeoutSeconds,
		"timeout_policy":        request.TimeoutPolicy,
		"execution_identity_id": request.ExecutionIdentityID,
		"expires_at":            expiresAt,
	}
	if len(request.Schema) > 0 {
		body["schema"] = request.Schema
	}
	if deliveries != nil {
		body["deliveries"] = deliveries
	}
	a.postHumanRequest(ctx, body)
}

func (a *Activities) postHumanRequest(ctx context.Context, body map[string]any) {
	endpoint := fmt.Sprintf("%s/v1/workspace/human-requests", a.WorkspaceURL)
	if _, _, err := a.workspaceDo(ctx, http.MethodPost, endpoint, body); err != nil {
		slog.Error("record human request", "operation", body["id"], "error", err)
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
	JobTitle  string             `json:"job_title"`
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

// sendToChannel dispatches one delivery to its channel driver
// (channeldriver.go). Unknown types fall back to the web inbox via the
// caller's failed status. thread asks the driver to post into an existing
// conversation thread; the return is the posted message's channel id when the
// driver can report it (slack ts — ThreadedTextPoster), "" otherwise.

// resolveChannelAddress returns the delivery address for one user channel:
// the registered address, or — when the row leaves the address empty — the
// transport's own DM resolution from the user's sender identity (matrix:
// the bot's shared DM room with the user's mxid).
func (a *Activities) resolveChannelAddress(ctx context.Context, channelType, address, senderID string) (string, error) {
	if strings.TrimSpace(address) != "" {
		return address, nil
	}
	driver := channelDriverByType(channelType)
	if driver == nil {
		return "", fmt.Errorf("no transport for channel %q", channelType)
	}
	resolver, ok := driver.(DMResolver)
	if !ok {
		return "", fmt.Errorf("channel %q has no address and cannot resolve direct chats", channelType)
	}
	return resolver.ResolveDM(ctx, a.HTTP, a.transports(ctx).Channels[channelType], senderID)
}

func (a *Activities) sendToChannel(ctx context.Context, channelType, address string, request NotifyChannelRequest, settings ChannelSettings, thread string) (postedID string, err error) {
	driver := channelDriverByType(channelType)
	if driver == nil {
		return "", fmt.Errorf("no transport for channel %q", channelType)
	}
	msg := outboundText(request, settings.UIURL)
	// Only thread-capable transports understand the pointer; everyone else
	// would ignore it at best — drop it and post top-level.
	if driver.Spec().Capabilities.Threads {
		msg.Thread = thread
	}
	if poster, ok := driver.(ThreadedTextPoster); ok {
		return poster.SendTextThreaded(ctx, a.HTTP, settings.Channels[channelType], address, msg)
	}
	return "", driver.SendText(ctx, a.HTTP, settings.Channels[channelType], address, msg)
}

// conversationThreadRef picks the conversation anchor for thread_ref: a
// follow-up that joined an existing thread keeps it (the posted message's ts
// is only a leaf inside it); a top-level post becomes the thread root itself,
// so its ts is the ref the user's threaded replies will carry as thread_ts.
func conversationThreadRef(thread, postedID string) string {
	if strings.TrimSpace(thread) != "" {
		return strings.TrimSpace(thread)
	}
	return strings.TrimSpace(postedID)
}

// channelThread finds the conversation thread of this run's previous ask to
// the user on the same channel (human_request.thread_ref, docs/channels.md
// §9): a follow-up question joins that thread instead of scattering across
// the channel. Best-effort — lookup problems degrade to a fresh top-level
// question, never to a failed delivery.
func (a *Activities) channelThread(ctx context.Context, channelType string, request NotifyChannelRequest, userID string) string {
	driver := channelDriverByType(channelType)
	if driver == nil || !driver.Spec().Capabilities.Threads {
		return ""
	}
	endpoint := fmt.Sprintf("%s/v1/workspace/human-requests?project=%s&user=%s",
		a.WorkspaceURL, url.QueryEscape(request.Project), url.QueryEscape(userID))
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil || status != http.StatusOK {
		return ""
	}
	var list struct {
		Requests []struct {
			RunID     string `json:"run_id"`
			Channel   string `json:"channel"`
			ThreadRef string `json:"thread_ref"`
		} `json:"requests"`
	}
	if json.Unmarshal([]byte(body), &list) != nil {
		return ""
	}
	// Newest first: the latest ask of this run carries the live conversation.
	for _, row := range list.Requests {
		if row.RunID == request.RunID && row.Channel == channelType && row.ThreadRef != "" {
			return row.ThreadRef
		}
	}
	return ""
}

// outboundText renders the channel-neutral payload: Body is what text
// transports deliver, the envelope fields feed structured ones (webhook).
func outboundText(request NotifyChannelRequest, uiURL string) OutboundText {
	msg := OutboundText{
		Body:           notificationText(request, uiURL),
		Question:       request.Question,
		Context:        request.Context,
		Options:        request.Options,
		Project:        request.Project,
		RunID:          request.RunID,
		OperationID:    request.OperationID,
		AgentID:        request.AgentID,
		TimeoutSeconds: request.TimeoutSeconds,
		TimeoutPolicy:  request.TimeoutPolicy,
		Schema:         request.Schema,
		FormURL:        request.FormURL,
	}
	if trimmed := strings.TrimSpace(uiURL); trimmed != "" {
		msg.AnswerURL = strings.TrimRight(trimmed, "/") + "/agents?project=" + url.QueryEscape(request.Project)
	}
	return msg
}

// formFieldNames lists a form schema's property names for the text-channel
// hint — best-effort: a malformed schema degrades to no hint, never to a
// failed delivery.
func formFieldNames(schema json.RawMessage) []string {
	var parsed struct {
		Properties map[string]any `json:"properties"`
	}
	if json.Unmarshal(schema, &parsed) != nil {
		return nil
	}
	names := make([]string, 0, len(parsed.Properties))
	for name := range parsed.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// notificationText renders the human-facing message for external transports.
// The answer itself always happens in the product UI; external channels
// carry the question and a link. The envelope follows the question's
// language — the model writes it in the conversation's language.
func notificationText(request NotifyChannelRequest, uiURL string) string {
	lang := DetectLang(request.Question)
	var builder strings.Builder
	builder.WriteString(ChannelText(lang, "ask_header"))
	builder.WriteString("\n\n")
	builder.WriteString(request.Question)
	if request.Context != "" {
		builder.WriteString("\n\n")
		builder.WriteString(ChannelText(lang, "ask_context"))
		builder.WriteString(" ")
		builder.WriteString(request.Context)
	}
	if len(request.Options) > 0 {
		builder.WriteString("\n")
		builder.WriteString(ChannelText(lang, "ask_options"))
		builder.WriteString(" ")
		builder.WriteString(strings.Join(request.Options, " / "))
	}
	if fields := formFieldNames(request.Schema); len(fields) > 0 {
		builder.WriteString("\n")
		builder.WriteString(ChannelText(lang, "ask_form", strings.Join(fields, ", ")))
	}
	// The hosted one-click form leads when present (docs/interactive-HITL.md
	// §6.2): answerable from any browser without a harness account.
	if request.FormURL != "" {
		builder.WriteString("\n")
		builder.WriteString(ChannelText(lang, "ask_form_url"))
		builder.WriteString(" ")
		builder.WriteString(request.FormURL)
	}
	builder.WriteString("\n")
	builder.WriteString(ChannelText(lang, "ask_project_run", request.Project, request.RunID))
	if request.TimeoutSeconds > 0 {
		builder.WriteString(" · ")
		builder.WriteString(ChannelText(lang, "ask_waiting", request.TimeoutSeconds))
	}
	if trimmed := strings.TrimSpace(uiURL); trimmed != "" {
		builder.WriteString("\n")
		builder.WriteString(ChannelText(lang, "ask_answer_here"))
		builder.WriteString(" ")
		builder.WriteString(strings.TrimRight(trimmed, "/"))
		builder.WriteString("/agents?project=")
		builder.WriteString(url.QueryEscape(request.Project))
	}
	return builder.String()
}
