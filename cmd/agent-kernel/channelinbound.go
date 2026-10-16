package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// intake starts the intake run for a fresh initiative message from an
	// identified sender and returns its run id; nil disables the intake
	// pipeline (messages are still recorded and rejected, never lost).
	intake func(ctx context.Context, message agent.InboundMessage, senderUserID string) (string, error)
	// emit journals one intake.* event (docs/inbound-messages.md §11); nil
	// drops events (tests).
	emit func(ctx context.Context, eventType string, message agent.InboundMessage, senderUserID string, data map[string]any)
	// events receives hitl.verdict.recorded emissions when a channel reply
	// answers a human request (specs/quality-loop.md §9); nil disables them
	// (tests run without an outbox).
	events hitlOutbox
	// intakeEnabled gates the pipeline: messages are still deduplicated and
	// recorded when off, but no run starts and no rejection is delivered.
	intakeEnabled bool
	// ratePerHour caps how many initiative messages one sender may push per
	// hour (docs/inbound-messages.md §12 phase 4); 0 disables the check.
	ratePerHour int

	cursor      string // opaque driver cursor; "" → fresh start (bounded history)
	identityKey string // settings fingerprint the cursor and selfID belong to
	selfID      string // the channel account's own identity; never matched
}

// channelInboundStore is the workspace surface the loop needs; *workspace.Store
// satisfies it, tests provide fakes.
type channelInboundStore interface {
	ListHumanRequests(ctx context.Context, project, status, resolvedUser string, onlyOpen bool) ([]workspace.HumanRequest, error)
	ListOpenHumanRequestDeliveries(ctx context.Context) ([]workspace.HumanRequestDelivery, error)
	AnswerHumanRequest(ctx context.Context, id, response, answeredBy string) (workspace.HumanRequest, error)
	CancelHumanRequest(ctx context.Context, id, actor string) error
	SetHumanRequestThreadRef(ctx context.Context, id, threadRef string) error
	ListUsers(ctx context.Context) ([]workspace.User, error)
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
	ChannelCursor(ctx context.Context, channel string) (workspace.ChannelCursor, error)
	SaveChannelCursor(ctx context.Context, channel string, cursor workspace.ChannelCursor) error
	InsertInboundMessage(ctx context.Context, m *workspace.InboundMessage) (bool, error)
	UpdateInboundMessage(ctx context.Context, id string, u workspace.InboundMessageUpdate) error
	CountInboundMessagesSince(ctx context.Context, senderUserID string, since time.Time) (int, error)
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
	messages, next, err := l.poller.Poll(ctx, l.httpClient, channelSettings, l.cursor)
	if err != nil {
		return 5 * time.Second, fmt.Errorf("%s poll: %w", l.driver.Type(), err)
	}
	l.routeMessages(ctx, messages, channelSettings)
	if next != l.cursor {
		l.cursor = next
		l.saveCursor(ctx)
	}
	return 0, nil
}

// routeMessages sends one batch of normalized inbound messages through the
// shared pipeline: replies match open asks first, everything else enters the
// intake pipeline (docs/channels.md §5). The poll cycle and the webhook
// receiver both land here — one routing core, two feeds.
func (l *channelInboundLoop) routeMessages(ctx context.Context, messages []agent.InboundMessage, channelSettings map[string]string) {
	asks, users, senders, err := l.openAsks(ctx)
	if err != nil {
		l.log.Warn("load open asks", "channel", l.driver.Type(), "error", err)
		return
	}
	// Route first, persist after: a crash between the two re-delivers the
	// batch (at-least-once) — the workflow side already tolerates duplicate
	// answers.
	replies := agent.MatchChannelReplies(messages, asks, users, l.selfID)
	answered := make(map[string]bool, len(replies))
	for _, reply := range replies {
		answered[reply.Message.ID] = true
		l.routeReply(ctx, reply)
	}
	// Everything else is an initiative message: the intake pipeline records
	// it durably, identifies the sender (closed world) and either starts the
	// intake run or rejects politely (docs/inbound-messages.md §4–5).
	for _, message := range messages {
		if answered[message.ID] || message.Sender == "" || message.Sender == l.selfID {
			continue
		}
		l.handleIntake(ctx, message, channelSettings, senders)
	}
}

// saveCursor persists the loop state best-effort: a failed save costs a
// redelivery after restart, never a lost message.
func (l *channelInboundLoop) saveCursor(ctx context.Context) {
	if err := l.store.SaveChannelCursor(ctx, l.driver.Type(), workspace.ChannelCursor{Cursor: l.cursor, Identity: l.identityKey}); err != nil {
		l.log.Warn("channel cursor save", "channel", l.driver.Type(), "error", err)
	}
}

// openAsks loads open human requests delivered through this channel, the
// chat sets of their recipients and the sender-identity registry of every
// enabled channel of this type. Only asks delivered through the channel
// match: a reply in a chat answers the question that was asked there.
// Team requests (org:<unit>, docs/inbound-messages.md §8) match through their
// delivery rows: every member whose copy went out through this channel may
// answer from their own chat — the first reply wins.
func (l *channelInboundLoop) openAsks(ctx context.Context) ([]agent.PendingAsk, []agent.UserChats, []agent.UserSenderChannels, error) {
	rows, err := l.store.ListHumanRequests(ctx, "", "", "", true)
	if err != nil {
		return nil, nil, nil, err
	}
	deliveryRows, err := l.store.ListOpenHumanRequestDeliveries(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	users, err := l.store.ListUsers(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	chatsByUser := make(map[string]agent.UserChats, len(users))
	senders := make([]agent.UserSenderChannels, 0, len(users))
	for _, user := range users {
		entry := agent.UserChats{UserID: user.ID}
		senderEntry := agent.UserSenderChannels{UserID: user.ID}
		for _, channel := range user.Channels {
			if channel.Type == l.driver.Type() && channel.Enabled && strings.TrimSpace(channel.Address) != "" {
				entry.Chats = append(entry.Chats, channel.Address)
				senderEntry.Channels = append(senderEntry.Channels, agent.SenderChannel{Address: channel.Address, SenderID: channel.SenderID})
			}
		}
		chatsByUser[user.ID] = entry
		senders = append(senders, senderEntry)
	}
	// Team fan-out index: which requests have delivery rows at all, and which
	// members received a copy through THIS channel (only their chats answer).
	isTeamAsk := make(map[string]bool, len(deliveryRows))
	membersOnChannel := make(map[string][]string, len(deliveryRows))
	for _, delivery := range deliveryRows {
		isTeamAsk[delivery.RequestID] = true
		if delivery.Channel == l.driver.Type() && delivery.Status == workspace.HumanDeliveryDelivered {
			membersOnChannel[delivery.RequestID] = append(membersOnChannel[delivery.RequestID], delivery.UserID)
		}
	}
	asks := make([]agent.PendingAsk, 0, len(rows))
	chats := make([]agent.UserChats, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if isTeamAsk[row.ID] {
			// One ask per member copy: a reply from any member's chat answers the
			// shared RequestID; MatchChannelReplies pairs messages oldest-first and
			// stops at the first answer.
			for _, member := range membersOnChannel[row.ID] {
				entry, ok := chatsByUser[member]
				if !ok || len(entry.Chats) == 0 {
					continue
				}
				asks = append(asks, agent.PendingAsk{
					RequestID: row.ID,
					RunID:     row.RunID,
					ProjectID: row.ProjectID,
					UserID:    member,
					ThreadRef: row.ThreadRef,
					CreatedAt: row.CreatedAt,
				})
				if !seen[member] {
					seen[member] = true
					chats = append(chats, entry)
				}
			}
			continue
		}
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
			ThreadRef: row.ThreadRef,
			CreatedAt: row.CreatedAt,
		})
		if !seen[row.ResolvedUser] {
			seen[row.ResolvedUser] = true
			chats = append(chats, entry)
		}
	}
	return asks, chats, senders, nil
}

// routeReply signals the waiting workflow and closes the human request row.
// The signal goes first: resuming the run is the point. When it fails the row
// stays open — the workflow is still waiting and a later retry (e.g. after a
// restart's history replay) may yet deliver the answer. File replies are
// downloaded into the project workspace first, so the resumed run — and every
// agent in the project — can read the attachment by path.
func (l *channelInboundLoop) routeReply(ctx context.Context, reply agent.ChannelReply) {
	// A threaded reply pins the conversation thread on the ask when it has
	// none yet (docs/channels.md §9): the rest of the thread matches exactly,
	// and follow-up questions of the run join it. Best-effort — a failed pin
	// only costs the exact-match fast path, never the answer itself.
	if reply.Message.ReplyTo != "" {
		if err := l.store.SetHumanRequestThreadRef(ctx, reply.Ask.RequestID, reply.Message.ReplyTo); err != nil && !errors.Is(err, workspace.ErrNotFound) {
			l.log.Warn("channel reply pin thread", "channel", l.driver.Type(), "operation", reply.Ask.RequestID, "error", err)
		}
	}
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
	} else if row, err := l.store.AnswerHumanRequest(ctx, reply.Ask.RequestID, response, actor); err == nil {
		// Terminal transition with a verdict → hitl.verdict.recorded
		// (specs/quality-loop.md §9) through the durable outbox.
		emitHitlVerdict(ctx, l.events, row)
	} else if !errors.Is(err, workspace.ErrNotFound) {
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

// handleIntake is the intake pipeline entry (docs/inbound-messages.md §4–5):
// dedup → closed-world sender identification → intake run or polite
// rejection. The row is durable before anything side-effecting happens, so a
// crash between here and the workflow start leaves a recorded message the
// operator can replay instead of a silently lost task.
func (l *channelInboundLoop) handleIntake(ctx context.Context, message agent.InboundMessage, channelSettings map[string]string, senders []agent.UserSenderChannels) {
	row := &workspace.InboundMessage{
		ID:        workspace.InboundMessageID(message.Channel, message.ID),
		Channel:   message.Channel,
		MessageID: message.ID,
		SenderRaw: message.Sender,
		Room:      message.ChatID,
		Body:      truncateForLog(message.Text, 8000),
		Status:    workspace.InboundStatusReceived,
	}
	inserted, err := l.store.InsertInboundMessage(ctx, row)
	if err != nil {
		// A storage failure must not block the cursor: log and skip. The
		// message redelivers only if the channel replays it; losing it to a
		// DB outage is the same tradeoff replies already make.
		l.log.Warn("intake insert", "channel", l.driver.Type(), "message", message.ID, "error", err)
		return
	}
	if !inserted {
		return // duplicate delivery — already processed or being processed
	}
	senderUserID, roomRegistered := agent.IdentifyInboundSender(message, senders)
	if senderUserID == "" {
		// Closed world: an initiative message from anyone but the registered
		// identity of the room owner never creates work. A registered room
		// gets a polite pointer to the app; an unregistered room stays silent
		// (recording replies to every room the bot sits in would be spam).
		reason := "unknown_sender"
		if !roomRegistered {
			reason = "unregistered_room"
		}
		_ = l.store.UpdateInboundMessage(ctx, row.ID, workspace.InboundMessageUpdate{Status: workspace.InboundStatusRejected})
		if l.emit != nil {
			l.emit(ctx, "intake.rejected", message, "", map[string]any{"reason": reason})
		}
		if roomRegistered && l.intakeEnabled {
			l.sendRoomNotice(ctx, channelSettings, message.ChatID,
				"Temporality: this room accepts answers to agent questions, but new tasks are only accepted from the registered account of the room owner. Open the app to create a task, or register this channel identity in your profile.")
		}
		l.log.Info("intake rejected", "channel", l.driver.Type(), "message", message.ID, "reason", reason, "sender", message.Sender)
		return
	}
	_ = l.store.UpdateInboundMessage(ctx, row.ID, workspace.InboundMessageUpdate{SenderUserID: senderUserID})
	// Sender rate limit (docs/inbound-messages.md §12 phase 4): a flooding
	// sender gets a polite refusal instead of a queue of intake runs — every
	// message burns an agent turn and a model call. The count includes this
	// row, so the limit is "messages per hour", not "messages plus one".
	if l.ratePerHour > 0 {
		if sent, cErr := l.store.CountInboundMessagesSince(ctx, senderUserID, time.Now().UTC().Add(-time.Hour)); cErr == nil && sent > l.ratePerHour {
			_ = l.store.UpdateInboundMessage(ctx, row.ID, workspace.InboundMessageUpdate{Status: workspace.InboundStatusRejected})
			if l.emit != nil {
				l.emit(ctx, "intake.rejected", message, senderUserID, map[string]any{"reason": "rate_limited"})
			}
			_ = l.store.RecordAccessAudit(ctx, senderUserID, workspace.AuditIntakeRejected, "inbound_message", row.ID, map[string]any{
				"reason": "rate_limited", "messages_last_hour": sent, "limit": l.ratePerHour,
			})
			l.sendRoomNotice(ctx, channelSettings, message.ChatID,
				fmt.Sprintf("Temporality: rate limit reached (%d messages per hour). Please retry later; urgent tasks can be created in the app.", l.ratePerHour))
			l.log.Info("intake rate limited", "channel", l.driver.Type(), "message", message.ID, "sender", senderUserID, "sent", sent)
			return
		}
	}
	if l.emit != nil {
		l.emit(ctx, "intake.received", message, senderUserID, nil)
	}
	// Attachments of an initiative message are saved before the intake run
	// starts (docs/inbound-messages.md §12 phase 3): the staging paths land on
	// the row, start_task copies them into the target project's inbox. A fetch
	// failure degrades to a text-only intake — the files are named in the prompt
	// either way, so the dialog can tell the user what could not be received.
	if len(message.Files) > 0 {
		if paths, err := l.saveInitiativeFiles(ctx, row.ID, message.Files); err != nil {
			l.log.Warn("intake attachment", "channel", l.driver.Type(), "message", message.ID, "error", err)
		} else {
			row.Attachments = paths
			_ = l.store.UpdateInboundMessage(ctx, row.ID, workspace.InboundMessageUpdate{Attachments: paths})
		}
	}
	if l.intake == nil || !l.intakeEnabled {
		l.log.Info("intake recorded without processing (pipeline disabled)", "channel", l.driver.Type(), "message", message.ID)
		return
	}
	runID, err := l.intake(ctx, message, senderUserID)
	if err != nil {
		// The row stays received — visible in the Входящие list as stuck; the
		// deterministic run id makes a manual retry idempotent.
		l.log.Warn("intake run start", "channel", l.driver.Type(), "message", message.ID, "error", err)
		return
	}
	_ = l.store.UpdateInboundMessage(ctx, row.ID, workspace.InboundMessageUpdate{IntakeRunID: runID})
	l.log.Info("intake run started", "channel", l.driver.Type(), "message", message.ID, "run", runID, "sender", senderUserID)
}

// sendRoomNotice delivers a channel-neutral service notice to a chat.
// Best-effort by design: a failed notice costs nothing downstream.
func (l *channelInboundLoop) sendRoomNotice(ctx context.Context, channelSettings map[string]string, address, body string) {
	noticeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := l.driver.SendText(noticeCtx, l.httpClient, channelSettings, address, agent.OutboundText{Body: body}); err != nil {
		l.log.Warn("intake notice", "channel", l.driver.Type(), "chat", address, "error", err)
	}
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
	root, err := sandboxRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "projects", reply.Ask.ProjectID, "inbox")
	saved, err := l.saveChannelFiles(ctx, reply.Message.Files, dir)
	if err != nil {
		return "", err
	}
	for i, name := range saved {
		saved[i] = "/workspace/inbox/" + name
	}
	return strings.Join(saved, ", "), nil
}

// saveInitiativeFiles stores the attachments of an initiative message in a
// message-scoped staging dir ({root}/inbound/{key}/) and returns the absolute
// host paths for the row. The intake project is not used as the store: files
// move into the real project's inbox only when start_task resolves where the
// task lands — until then nothing clutters a project workspace.
func (l *channelInboundLoop) saveInitiativeFiles(ctx context.Context, rowID string, files []agent.InboundFile) ([]string, error) {
	root, err := sandboxRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "inbound", inboundStagingKey(rowID))
	names, err := l.saveChannelFiles(ctx, files, dir)
	if err != nil {
		return nil, err
	}
	// Absolute host paths: the row outlives the loop cycle, and start_task
	// resolves them into the target project's inbox later.
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths, nil
}

// sandboxRoot resolves the persistent workspace root inbound files live under.
func sandboxRoot() (string, error) {
	root := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_ROOT"))
	if root == "" {
		return "", errors.New("KERNEL_SANDBOX_ROOT is not configured")
	}
	return root, nil
}

// inboundStagingKey derives a filesystem-safe, deterministic dir name from the
// inbound row id (inbound/{channel}/{message_id}).
func inboundStagingKey(rowID string) string {
	digest := sha256.Sum256([]byte(rowID))
	return hex.EncodeToString(digest[:])[:16]
}

// saveChannelFiles downloads attachments through the driver (only it can
// resolve the opaque references) into dir and returns the stored file names.
func (l *channelInboundLoop) saveChannelFiles(ctx context.Context, files []agent.InboundFile, dir string) ([]string, error) {
	fetcher, ok := l.poller.(agent.ChannelFileFetcher)
	if !ok {
		return nil, fmt.Errorf("channel %s cannot receive files", l.driver.Type())
	}
	settings := agent.ChannelSettingsFromEnv()
	if l.settings != nil {
		if stored, err := l.settings(ctx); err == nil {
			settings = stored.OverlayEnv()
		}
	}
	channelSettings := settings.Channels[l.driver.Type()]
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, err
	}
	saved := make([]string, 0, len(files))
	for _, file := range files {
		payload, err := fetcher.FetchFile(ctx, l.httpClient, channelSettings, file)
		if err != nil {
			return nil, err
		}
		name := safeFileName(file.Name)
		target := filepath.Join(dir, name)
		if _, err := os.Stat(target); err == nil {
			name = time.Now().UTC().Format("20060102-150405") + "-" + name
			target = filepath.Join(dir, name)
		}
		if err := os.WriteFile(target, payload, 0o666); err != nil {
			return nil, err
		}
		saved = append(saved, name)
	}
	return saved, nil
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
