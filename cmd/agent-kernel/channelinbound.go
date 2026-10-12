package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// channelInboundLoop turns a two-way channel driver into inbound routing: it
// long-polls the driver's poller and maps chat replies back into paused runs
// via the same Approval signal the web inbox uses — one loop per driver with
// a poller, no channel-specific code here. Transport settings are reloaded
// every cycle, so admin changes take effect without a restart; when the
// channel is not configured the loop idles and re-checks. The cursor and the
// account identity survive restarts in workspace_channel_cursor.
//
// Security note (MVP): matching is per chat — anyone in the recipient's chat
// answers on the owner's behalf. Finer sender verification (channel identity
// mapping to workspace users) arrives with the intake pipeline
// (docs/inbound-messages.md).
type channelInboundLoop struct {
	driver     agent.ChannelDriver
	poller     agent.ChannelPoller
	httpClient *http.Client
	settings   func(context.Context) (agent.ChannelSettings, error)
	store      channelInboundStore
	sourceID   string
	log        *slog.Logger
	// signal routes one approval into a waiting workflow; production wraps
	// temporal SignalWorkflow, tests record calls.
	signal func(ctx context.Context, workflowID string, approval agent.Approval) error

	cursor      string // opaque driver cursor; "" → fresh start (bounded history)
	identityKey string // settings fingerprint the cursor and selfID belong to
	selfID      string // the channel account's own identity; never matched
}

// channelInboundStore is the workspace surface the loop needs; *workspace.Store
// satisfies it, tests provide fakes.
type channelInboundStore interface {
	ListHumanRequests(ctx context.Context, project, status, resolvedUser string, onlyOpen bool) ([]workspace.HumanRequest, error)
	AnswerHumanRequest(ctx context.Context, id, response, answeredBy string) (workspace.HumanRequest, error)
	CancelHumanRequest(ctx context.Context, id, actor string) error
	ListUsers(ctx context.Context) ([]workspace.User, error)
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
	ChannelCursor(ctx context.Context, channel string) (workspace.ChannelCursor, error)
	SaveChannelCursor(ctx context.Context, channel string, cursor workspace.ChannelCursor) error
}

// run cycles until the context is cancelled. A cycle error is logged and the
// loop backs off briefly; the returned delay also paces idle re-checks. The
// cursor is loaded once so restarts continue where the loop left off.
func (l *channelInboundLoop) run(ctx context.Context) {
	if stored, err := l.store.ChannelCursor(ctx, l.driver.Type()); err != nil {
		l.log.Warn("channel cursor load", "channel", l.driver.Type(), "error", err)
	} else {
		l.cursor, l.identityKey = stored.Cursor, stored.Identity
	}
	for ctx.Err() == nil {
		delay, err := l.cycle(ctx)
		if err != nil {
			l.log.Warn("channel inbound", "channel", l.driver.Type(), "error", err)
			if delay < 5*time.Second {
				delay = 5 * time.Second
			}
		}
		if !sleepContext(ctx, delay) {
			return
		}
	}
}

// cycle performs one configuration check + poll pass and returns how long to
// wait before the next one. Zero delay means: loop straight into the next
// long-poll.
func (l *channelInboundLoop) cycle(ctx context.Context) (time.Duration, error) {
	// Same precedence as delivery (Activities.transports): database overlay
	// over env; a loader error degrades to env rather than silencing replies.
	settings := agent.ChannelSettingsFromEnv()
	if l.settings != nil {
		if stored, err := l.settings(ctx); err == nil {
			settings = stored.OverlayEnv()
		}
	}
	channelSettings := settings.Channels[l.driver.Type()]
	if !l.driver.IsConfigured(channelSettings) {
		return 30 * time.Second, nil // channel not configured; the admin may enable it live
	}
	if key := agent.ChannelFingerprint(channelSettings); key != l.identityKey {
		// Different account (credentials changed): the cursor belongs to
		// another stream and the cached self identity is stale.
		l.identityKey = key
		l.selfID = ""
		if l.cursor != "" {
			l.cursor = ""
			l.saveCursor(ctx)
		}
	}
	if l.selfID == "" {
		// The account's own sent questions come back through the poll; without
		// its identity they would match themselves. Fail safe: no self, no
		// processing.
		selfID, err := l.poller.Self(ctx, l.httpClient, channelSettings)
		if err != nil {
			return 30 * time.Second, fmt.Errorf("%s self: %w", l.driver.Type(), err)
		}
		l.selfID = selfID
		l.log.Info("channel inbound listening", "channel", l.driver.Type(), "self", selfID)
	}
	asks, users, err := l.openAsks(ctx)
	if err != nil {
		return 15 * time.Second, fmt.Errorf("load open asks: %w", err)
	}
	messages, next, err := l.poller.Poll(ctx, l.httpClient, channelSettings, l.cursor)
	if err != nil {
		return 5 * time.Second, fmt.Errorf("%s poll: %w", l.driver.Type(), err)
	}
	// Route first, persist after: a crash between the two re-delivers the
	// batch (at-least-once) — the workflow side already tolerates duplicate
	// answers.
	for _, reply := range agent.MatchChannelReplies(messages, asks, users, l.selfID) {
		l.routeReply(ctx, reply)
	}
	if next != l.cursor {
		l.cursor = next
		l.saveCursor(ctx)
	}
	return 0, nil
}

// saveCursor persists the loop state best-effort: a failed save costs a
// redelivery after restart, never a lost message.
func (l *channelInboundLoop) saveCursor(ctx context.Context) {
	if err := l.store.SaveChannelCursor(ctx, l.driver.Type(), workspace.ChannelCursor{Cursor: l.cursor, Identity: l.identityKey}); err != nil {
		l.log.Warn("channel cursor save", "channel", l.driver.Type(), "error", err)
	}
}

// openAsks loads open human requests delivered through this channel and the
// chat sets of their recipients. Only asks delivered through the channel
// match: a reply in a chat answers the question that was asked there.
func (l *channelInboundLoop) openAsks(ctx context.Context) ([]agent.PendingAsk, []agent.UserChats, error) {
	rows, err := l.store.ListHumanRequests(ctx, "", "", "", true)
	if err != nil {
		return nil, nil, err
	}
	users, err := l.store.ListUsers(ctx)
	if err != nil {
		return nil, nil, err
	}
	chatsByUser := make(map[string]agent.UserChats, len(users))
	for _, user := range users {
		entry := agent.UserChats{UserID: user.ID}
		for _, channel := range user.Channels {
			if channel.Type == l.driver.Type() && channel.Enabled && strings.TrimSpace(channel.Address) != "" {
				entry.Chats = append(entry.Chats, channel.Address)
			}
		}
		chatsByUser[user.ID] = entry
	}
	asks := make([]agent.PendingAsk, 0, len(rows))
	chats := make([]agent.UserChats, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Channel != l.driver.Type() || row.ResolvedUser == "" {
			continue
		}
		entry, ok := chatsByUser[row.ResolvedUser]
		if !ok || len(entry.Chats) == 0 {
			continue
		}
		asks = append(asks, agent.PendingAsk{
			RequestID: row.ID,
			RunID:     row.RunID,
			ProjectID: row.ProjectID,
			UserID:    row.ResolvedUser,
			CreatedAt: row.CreatedAt,
		})
		if !seen[row.ResolvedUser] {
			seen[row.ResolvedUser] = true
			chats = append(chats, entry)
		}
	}
	return asks, chats, nil
}

// routeReply signals the waiting workflow and closes the human request row.
// The signal goes first: resuming the run is the point. When it fails the row
// stays open — the workflow is still waiting and a later retry (e.g. after a
// restart's history replay) may yet deliver the answer. File replies are
// downloaded into the project workspace first, so the resumed run — and every
// agent in the project — can read the attachment by path.
func (l *channelInboundLoop) routeReply(ctx context.Context, reply agent.ChannelReply) {
	response := truncateForLog(reply.Message.Text, 4000)
	if len(reply.Message.Files) > 0 {
		saved, err := l.saveInboundFiles(ctx, reply)
		if err != nil {
			l.log.Warn("channel inbound file", "channel", l.driver.Type(), "operation", reply.Ask.RequestID, "error", err)
			response = "[File could not be received: " + err.Error() + "] " + response
		} else {
			response = strings.TrimSpace("[Attached file: " + saved + "] " + response)
		}
	}
	actor := reply.Ask.UserID // anyone in the chat answers as the chat owner (MVP)
	approval := agent.Approval{
		OperationID: reply.Ask.RequestID,
		Approved:    !reply.Cancelled,
		ActorID:     actor,
		Response:    response,
	}
	workflowID := agent.WorkflowID(l.sourceID, reply.Ask.ProjectID, reply.Ask.RunID)
	if err := l.signal(ctx, workflowID, approval); err != nil {
		// A finished workflow (timeout race) is expected: the row is already
		// expired and the run moved on — nothing to route.
		detail := strings.ToLower(err.Error())
		if strings.Contains(detail, "not found") || strings.Contains(detail, "already completed") {
			l.log.Info("channel reply arrived after run ended", "channel", l.driver.Type(), "operation", reply.Ask.RequestID, "run", reply.Ask.RunID)
			return
		}
		l.log.Warn("channel reply signal", "channel", l.driver.Type(), "operation", reply.Ask.RequestID, "run", reply.Ask.RunID, "error", err)
		return
	}
	if reply.Cancelled {
		if err := l.store.CancelHumanRequest(ctx, reply.Ask.RequestID, actor); err != nil && !errors.Is(err, workspace.ErrNotFound) {
			l.log.Warn("channel reply cancel request", "operation", reply.Ask.RequestID, "error", err)
		}
	} else if _, err := l.store.AnswerHumanRequest(ctx, reply.Ask.RequestID, response, actor); err != nil && !errors.Is(err, workspace.ErrNotFound) {
		l.log.Warn("channel reply answer request", "operation", reply.Ask.RequestID, "error", err)
	}
	action := workspace.AuditHumanAnswered
	if reply.Cancelled {
		action = workspace.AuditHumanCancelled
	}
	if err := l.store.RecordAccessAudit(ctx, actor, action, "human_request", reply.Ask.RequestID, map[string]any{
		"run_id":  reply.Ask.RunID,
		"project": reply.Ask.ProjectID,
		"channel": l.driver.Type(),
		"sender":  reply.Message.Sender,
		"chat":    reply.Message.ChatID,
	}); err != nil {
		l.log.Warn("channel reply audit", "operation", reply.Ask.RequestID, "error", err)
	}
	l.log.Info("channel reply routed", "channel", l.driver.Type(), "operation", reply.Ask.RequestID, "run", reply.Ask.RunID, "sender", reply.Message.Sender, "cancelled", reply.Cancelled)
}

// truncateForLog bounds a string by runes for logs, errors and signal payloads.
func truncateForLog(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// saveInboundFiles downloads the attachments of a file reply through the
// driver (only it can resolve the opaque references) into the project's
// persistent workspace ({sandbox}/projects/{project}/inbox/) and returns the
// container-side paths the run will see (/workspace/inbox/…). Multiple
// attachments land as comma-joined paths; names are transliterated like chat
// uploads, collisions get a timestamp prefix.
func (l *channelInboundLoop) saveInboundFiles(ctx context.Context, reply agent.ChannelReply) (string, error) {
	fetcher, ok := l.poller.(agent.ChannelFileFetcher)
	if !ok {
		return "", fmt.Errorf("channel %s cannot receive files", l.driver.Type())
	}
	root := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_ROOT"))
	if root == "" {
		return "", errors.New("KERNEL_SANDBOX_ROOT is not configured")
	}
	settings := agent.ChannelSettingsFromEnv()
	if l.settings != nil {
		if stored, err := l.settings(ctx); err == nil {
			settings = stored.OverlayEnv()
		}
	}
	channelSettings := settings.Channels[l.driver.Type()]
	dir := filepath.Join(root, "projects", reply.Ask.ProjectID, "inbox")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	saved := make([]string, 0, len(reply.Message.Files))
	for _, file := range reply.Message.Files {
		payload, err := fetcher.FetchFile(ctx, l.httpClient, channelSettings, file)
		if err != nil {
			return "", err
		}
		name := safeFileName(file.Name)
		target := filepath.Join(dir, name)
		if _, err := os.Stat(target); err == nil {
			name = time.Now().UTC().Format("20060102-150405") + "-" + name
			target = filepath.Join(dir, name)
		}
		if err := os.WriteFile(target, payload, 0o666); err != nil {
			return "", err
		}
		saved = append(saved, "/workspace/inbox/"+name)
	}
	return strings.Join(saved, ", "), nil
}

// safeFileName normalizes an inbound attachment name the same way chat
// uploads are: base name, transliterated, slugified, extension preserved.
func safeFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "." || strings.ContainsAny(ext, " /") {
		// "..." and similar junk yield a lone-dot extension; drop it.
		ext = ""
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	slug := slugify(translit(base))
	if slug == "" || slug == "untitled" {
		slug = "attachment"
	}
	return slug + ext
}

// sleepContext waits for d or ctx cancellation; false means the ctx is done.
func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
