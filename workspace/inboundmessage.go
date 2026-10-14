package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Inbound message store (docs/inbound-messages.md §4.1, §10). The unique
// (channel, message_id) key is the idempotency boundary of the intake
// pipeline: the loop inserts before processing and skips rows that already
// exist, so at-least-once channel delivery (matrix /sync replay, kernel
// restart) never creates a second intake run.

// InboundMessageID builds the deterministic row id from the delivery parts.
func InboundMessageID(channel, messageID string) string {
	return "inbound/" + channel + "/" + messageID
}

// InsertInboundMessage records a new message and reports whether this call
// was the first one (false = duplicate delivery, already processed or being
// processed). SenderUserID/status may be refined by later updates.
func (s *Store) InsertInboundMessage(ctx context.Context, m *InboundMessage) (bool, error) {
	if m.ID == "" {
		m.ID = InboundMessageID(m.Channel, m.MessageID)
	}
	if m.Status == "" {
		m.Status = InboundStatusReceived
	}
	attachments, _ := json.Marshal(m.Attachments)
	if m.Attachments == nil {
		attachments = []byte("[]")
	}
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_inbound_message
		   (id, channel, message_id, sender_raw, sender_user_id, room, body, attachments, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9, now(), now())
		 ON CONFLICT (channel, message_id) DO NOTHING`,
		m.ID, m.Channel, m.MessageID, m.SenderRaw, m.SenderUserID, m.Room, m.Body, attachments, m.Status)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// inboundMessageColumns is the shared projection for scan/scan helpers.
const inboundMessageColumns = `id, channel, message_id, sender_raw, COALESCE(sender_user_id, ''), room, body, attachments, status, COALESCE(classification, ''), COALESCE(intake_run_id, ''), COALESCE(target_run_id, ''), COALESCE(project_id, ''), retired_at, created_at, updated_at`

func scanInboundMessage(scan func(dest ...any) error) (InboundMessage, error) {
	var m InboundMessage
	var attachments []byte
	if err := scan(&m.ID, &m.Channel, &m.MessageID, &m.SenderRaw, &m.SenderUserID, &m.Room, &m.Body, &attachments, &m.Status, &m.Classification, &m.IntakeRunID, &m.TargetRunID, &m.ProjectID, &m.RetiredAt, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return m, err
	}
	if len(attachments) > 0 {
		_ = json.Unmarshal(attachments, &m.Attachments)
	}
	return m, nil
}

// GetInboundMessage loads one row by its deterministic id.
func (s *Store) GetInboundMessage(ctx context.Context, id string) (InboundMessage, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+inboundMessageColumns+` FROM workspace_inbound_message WHERE id = $1`, id)
	m, err := scanInboundMessage(row.Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return m, ErrNotFound
		}
		return m, err
	}
	return m, nil
}

// GetInboundMessageByIntakeRun resolves the intake row behind a running
// intake run: the tool surface resolves the message from the run id, never
// from model arguments (docs/inbound-messages.md §7).
func (s *Store) GetInboundMessageByIntakeRun(ctx context.Context, runID string) (InboundMessage, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+inboundMessageColumns+` FROM workspace_inbound_message WHERE intake_run_id = $1`, runID)
	m, err := scanInboundMessage(row.Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return m, ErrNotFound
		}
		return m, err
	}
	return m, nil
}

// InboundMessageUpdate is a partial update of the intake state machine. Zero
// fields are left untouched; empty-string status/classification mean "not
// changed" — use the typed setters below for lifecycle transitions.
type InboundMessageUpdate struct {
	SenderUserID   string
	Status         string
	Classification string
	IntakeRunID    string
	TargetRunID    string
	ProjectID      string
	// Attachments records the saved staging paths of the message's files;
	// nil leaves the column untouched.
	Attachments []string
	// ClearProject removes a resolved project (not used today, but the
	// clarifying state may reset a wrong guess).
	ClearProject bool
}

// UpdateInboundMessage applies the non-zero fields of u.
func (s *Store) UpdateInboundMessage(ctx context.Context, id string, u InboundMessageUpdate) error {
	set := []string{"updated_at = now()"}
	args := []any{id}
	add := func(column, value string) {
		args = append(args, value)
		set = append(set, column+" = $"+itoa(len(args)))
	}
	if u.SenderUserID != "" {
		add("sender_user_id", u.SenderUserID)
	}
	if u.Status != "" {
		add("status", u.Status)
	}
	if u.Classification != "" {
		add("classification", u.Classification)
	}
	if u.IntakeRunID != "" {
		add("intake_run_id", u.IntakeRunID)
	}
	if u.TargetRunID != "" {
		add("target_run_id", u.TargetRunID)
	}
	if u.Attachments != nil {
		attachments, _ := json.Marshal(u.Attachments)
		args = append(args, string(attachments))
		set = append(set, "attachments = $"+itoa(len(args)))
	}
	if u.ClearProject {
		set = append(set, "project_id = NULL")
	} else if u.ProjectID != "" {
		add("project_id", u.ProjectID)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_inbound_message SET `+strings.Join(set, ", ")+` WHERE id = $1`, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListInboundMessages returns the newest messages first, bounded by limit
// (default 100, max 500). Filters are optional.
func (s *Store) ListInboundMessages(ctx context.Context, status, channel string, limit int) ([]InboundMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := `SELECT ` + inboundMessageColumns + ` FROM workspace_inbound_message WHERE 1=1`
	args := []any{}
	if status != "" {
		args = append(args, status)
		query += ` AND status = $` + itoa(len(args))
	}
	if channel != "" {
		args = append(args, channel)
		query += ` AND channel = $` + itoa(len(args))
	}
	args = append(args, limit)
	query += ` ORDER BY created_at DESC LIMIT $` + itoa(len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []InboundMessage{}
	for rows.Next() {
		m, err := scanInboundMessage(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// itoa avoids importing strconv for the two callers in this file.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// ListRoutedInboundMessages returns the messages of one sender that produced
// runs, newest first (docs/inbound-messages.md §4.2: run_status answers "what
// happened to my tasks" — only routed rows have a target run to report).
func (s *Store) ListRoutedInboundMessages(ctx context.Context, senderUserID string, limit int) ([]InboundMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+inboundMessageColumns+` FROM workspace_inbound_message
		 WHERE sender_user_id = $1 AND target_run_id <> '' AND status = 'routed'
		 ORDER BY created_at DESC LIMIT $2`, senderUserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []InboundMessage{}
	for rows.Next() {
		m, err := scanInboundMessage(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// ExpireStaleInboundMessages marks still-unrouted messages older than ttl as
// expired (docs/inbound-messages.md §6.1: the clarification timeout ends the
// intake politely). Called lazily from the list endpoint.
func (s *Store) ExpireStaleInboundMessages(ctx context.Context, ttl time.Duration) error {
	// make_interval(secs => $4), not $4::interval: pgx has no encode plan for
	// an int arg against the interval OID, so the cast made every sweep fail
	// client-side ("unable to encode 172800 ... for interval") and inbox rows
	// never expired. Same pattern as human_request stats.
	_, err := s.pool.Exec(ctx,
		`UPDATE workspace_inbound_message
		   SET status = $1, updated_at = now()
		 WHERE status IN ($2, $3) AND created_at < now() - make_interval(secs => $4)`,
		InboundStatusExpired, InboundStatusReceived, InboundStatusClarifying, ttl.Seconds())
	return err
}

// CountInboundMessagesSince counts a sender's messages inside a window — the
// intake rate limit input (docs/inbound-messages.md §12 phase 4). Every status
// counts: rejected and expired traffic still proves the channel is being
// hammered.
func (s *Store) CountInboundMessagesSince(ctx context.Context, senderUserID string, since time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM workspace_inbound_message
		  WHERE sender_user_id = $1 AND created_at >= $2`, senderUserID, since).Scan(&count)
	return count, err
}

// RetireInboundMessages scrubs bodies and attachment lists of messages older
// than the retention cutoff (docs/inbound-messages.md §12 phase 4, PII): the
// row survives as the audit chain link (ids, statuses, run pointers), the
// payload does not. Returns the retired row ids so the caller can drop the
// staged attachment files on disk.
func (s *Store) RetireInboundMessages(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE workspace_inbound_message
		   SET body = '', attachments = '[]'::jsonb, retired_at = now(), updated_at = now()
		 WHERE retired_at IS NULL AND created_at < $1
		 RETURNING id`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
