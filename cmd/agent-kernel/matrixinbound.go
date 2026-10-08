package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// matrixInboundLoop turns the matrix transport two-way: it long-polls the
// homeserver /sync endpoint and routes room replies back into paused runs via
// the same Approval signal the web inbox uses. Transport settings are reloaded
// every cycle, so admin changes take effect without a restart; when matrix is
// not configured the loop idles and re-checks.
//
// Security note (MVP): matching is per room — anyone in the recipient's room
// answers on the room owner's behalf. Finer sender verification (mxid mapping
// to workspace users) is future work.
type matrixInboundLoop struct {
	httpClient *http.Client
	settings   func(context.Context) (agent.TransportSettings, error)
	store      matrixInboundStore
	sourceID   string
	log        *slog.Logger
	// signal routes one approval into a waiting workflow; production wraps
	// temporal SignalWorkflow, tests record calls.
	signal func(ctx context.Context, workflowID string, approval agent.Approval) error

	since     string // sync cursor; empty → initial sync (bounded history replay)
	botKey    string // settings fingerprint the cached botUserID belongs to
	botUserID string
}

// matrixInboundStore is the workspace surface the loop needs; *workspace.Store
// satisfies it, tests provide fakes.
type matrixInboundStore interface {
	ListHumanRequests(ctx context.Context, project, status, resolvedUser string, onlyOpen bool) ([]workspace.HumanRequest, error)
	AnswerHumanRequest(ctx context.Context, id, response, answeredBy string) (workspace.HumanRequest, error)
	CancelHumanRequest(ctx context.Context, id, actor string) error
	ListUsers(ctx context.Context) ([]workspace.User, error)
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
}

// run cycles until the context is cancelled. A cycle error is logged and the
// loop backs off briefly; the returned delay also paces idle re-checks.
func (m *matrixInboundLoop) run(ctx context.Context) {
	for ctx.Err() == nil {
		delay, err := m.cycle(ctx)
		if err != nil {
			m.log.Warn("matrix inbound", "error", err)
			if delay < 5*time.Second {
				delay = 5 * time.Second
			}
		}
		if !sleepContext(ctx, delay) {
			return
		}
	}
}

// cycle performs one configuration check + sync pass and returns how long to
// wait before the next one. Zero delay means: loop straight into the next
// long-poll.
func (m *matrixInboundLoop) cycle(ctx context.Context) (time.Duration, error) {
	// Same precedence as delivery (Activities.transports): database overlay
	// over env; a loader error degrades to env rather than silencing replies.
	settings := agent.TransportSettingsFromEnv()
	if m.settings != nil {
		if stored, err := m.settings(ctx); err == nil {
			settings = stored.OverlayEnv()
		}
	}
	homeserver := strings.TrimRight(strings.TrimSpace(settings.MatrixHomeserver), "/")
	token := agent.TrimBearerPrefix(settings.MatrixAccessToken)
	if homeserver == "" || token == "" {
		return 30 * time.Second, nil // matrix not configured; the admin may enable it live
	}
	if key := homeserver + "\x00" + token; key != m.botKey {
		m.botKey = key
		m.botUserID = ""
		m.since = "" // different account → different sync stream, drop the cursor
	}
	// The bot's own sent questions come back through /sync; without the bot's
	// user id they would match themselves. Fail safe: no whoami, no processing.
	if m.botUserID == "" {
		userID, err := m.matrixWhoami(ctx, homeserver, token)
		if err != nil {
			return 30 * time.Second, fmt.Errorf("matrix whoami: %w", err)
		}
		m.botUserID = userID
		m.log.Info("matrix inbound listening", "bot", userID, "homeserver", homeserver)
	}
	asks, users, err := m.openMatrixAsks(ctx)
	if err != nil {
		return 15 * time.Second, fmt.Errorf("load open asks: %w", err)
	}
	since, messages, err := m.matrixSync(ctx, homeserver, token)
	if err != nil {
		return 5 * time.Second, fmt.Errorf("matrix sync: %w", err)
	}
	m.since = since
	for _, reply := range agent.MatchMatrixReplies(messages, asks, users, m.botUserID) {
		m.routeReply(ctx, reply)
	}
	return 0, nil
}

// openMatrixAsks loads open human requests delivered to a matrix room and the
// room sets of their recipients. Only matrix-delivered asks match: a reply in a
// room answers the question that was asked in that room.
func (m *matrixInboundLoop) openMatrixAsks(ctx context.Context) ([]agent.MatrixPendingAsk, []agent.MatrixUserRooms, error) {
	rows, err := m.store.ListHumanRequests(ctx, "", "", "", true)
	if err != nil {
		return nil, nil, err
	}
	users, err := m.store.ListUsers(ctx)
	if err != nil {
		return nil, nil, err
	}
	roomsByUser := make(map[string]agent.MatrixUserRooms, len(users))
	for _, user := range users {
		entry := agent.MatrixUserRooms{UserID: user.ID}
		for _, channel := range user.Channels {
			if channel.Type == "matrix" && channel.Enabled && strings.TrimSpace(channel.Address) != "" {
				entry.Rooms = append(entry.Rooms, channel.Address)
			}
		}
		roomsByUser[user.ID] = entry
	}
	asks := make([]agent.MatrixPendingAsk, 0, len(rows))
	rooms := make([]agent.MatrixUserRooms, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Channel != "matrix" || row.ResolvedUser == "" {
			continue
		}
		entry, ok := roomsByUser[row.ResolvedUser]
		if !ok || len(entry.Rooms) == 0 {
			continue
		}
		asks = append(asks, agent.MatrixPendingAsk{
			RequestID: row.ID,
			RunID:     row.RunID,
			ProjectID: row.ProjectID,
			UserID:    row.ResolvedUser,
			CreatedAt: row.CreatedAt,
		})
		if !seen[row.ResolvedUser] {
			seen[row.ResolvedUser] = true
			rooms = append(rooms, entry)
		}
	}
	return asks, rooms, nil
}

// matrixSync long-polls /sync. The initial sync (no cursor) bounds the room
// history it asks for; replayed events are still constrained by the
// ask-created-at rule, so replies that arrived while the kernel was down are
// picked up without older chatter matching.
func (m *matrixInboundLoop) matrixSync(ctx context.Context, homeserver, token string) (string, []agent.MatrixInboundMessage, error) {
	endpoint := homeserver + "/_matrix/client/v3/sync?timeout=" + strconv.Itoa(int((25 * time.Second).Seconds()))
	if m.since == "" {
		endpoint += "&filter=" + url.QueryEscape(`{"room":{"timeline":{"limit":50}}}`)
	} else {
		endpoint += "&since=" + url.QueryEscape(m.since)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("returned %d: %s", resp.StatusCode, truncateForLog(string(body), 200))
	}
	return agent.ParseMatrixSync(body)
}

// matrixWhoami resolves the bot's own user id so its messages can be skipped.
func (m *matrixInboundLoop) matrixWhoami(ctx context.Context, homeserver, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, homeserver+"/_matrix/client/v3/account/whoami", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("returned %d: %s", resp.StatusCode, truncateForLog(string(body), 200))
	}
	var decoded struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", err
	}
	if decoded.UserID == "" {
		return "", errors.New("no user_id in response")
	}
	return decoded.UserID, nil
}

// routeReply signals the waiting workflow and closes the human request row.
// The signal goes first: resuming the run is the point. When it fails the row
// stays open — the workflow is still waiting and a later retry (e.g. after a
// restart's history replay) may yet deliver the answer.
func (m *matrixInboundLoop) routeReply(ctx context.Context, reply agent.MatrixReply) {
	response := truncateForLog(reply.Message.Body, 4000)
	actor := reply.Ask.UserID // anyone in the room answers as the room owner (MVP)
	approval := agent.Approval{
		OperationID: reply.Ask.RequestID,
		Approved:    !reply.Cancelled,
		ActorID:     actor,
		Response:    response,
	}
	workflowID := agent.WorkflowID(m.sourceID, reply.Ask.ProjectID, reply.Ask.RunID)
	if err := m.signal(ctx, workflowID, approval); err != nil {
		// A finished workflow (timeout race) is expected: the row is already
		// expired and the run moved on — nothing to route.
		detail := strings.ToLower(err.Error())
		if strings.Contains(detail, "not found") || strings.Contains(detail, "already completed") {
			m.log.Info("matrix reply arrived after run ended", "operation", reply.Ask.RequestID, "run", reply.Ask.RunID)
			return
		}
		m.log.Warn("matrix reply signal", "operation", reply.Ask.RequestID, "run", reply.Ask.RunID, "error", err)
		return
	}
	if reply.Cancelled {
		if err := m.store.CancelHumanRequest(ctx, reply.Ask.RequestID, actor); err != nil && !errors.Is(err, workspace.ErrNotFound) {
			m.log.Warn("matrix reply cancel request", "operation", reply.Ask.RequestID, "error", err)
		}
	} else if _, err := m.store.AnswerHumanRequest(ctx, reply.Ask.RequestID, response, actor); err != nil && !errors.Is(err, workspace.ErrNotFound) {
		m.log.Warn("matrix reply answer request", "operation", reply.Ask.RequestID, "error", err)
	}
	action := workspace.AuditHumanAnswered
	if reply.Cancelled {
		action = workspace.AuditHumanCancelled
	}
	if err := m.store.RecordAccessAudit(ctx, actor, action, "human_request", reply.Ask.RequestID, map[string]any{
		"run_id":  reply.Ask.RunID,
		"project": reply.Ask.ProjectID,
		"channel": "matrix",
		"sender":  reply.Message.Sender,
		"room":    reply.Message.RoomID,
	}); err != nil {
		m.log.Warn("matrix reply audit", "operation", reply.Ask.RequestID, "error", err)
	}
	m.log.Info("matrix reply routed", "operation", reply.Ask.RequestID, "run", reply.Ask.RunID, "sender", reply.Message.Sender, "cancelled", reply.Cancelled)
}

// truncateForLog bounds a string by runes for logs, errors and signal payloads.
func truncateForLog(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
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
