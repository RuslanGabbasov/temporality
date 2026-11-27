package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ConversationOutboxEvent is one outbound delivery unit
// (docs/conversational-runtime.md §13): the durable record of an answer that
// must reach a transport. Producers enqueue it in the same logical moment the
// answer becomes authoritative; the delivery loop drains it independently, so
// a kernel restart or a transport outage delays delivery instead of losing it.
type ConversationOutboxEvent struct {
	ID             string
	OrganizationID string
	ConversationID string
	EventType      string
	Payload        json.RawMessage
	Status         string // pending | processing | delivered | failed
	Attempts       int
	LastError      string
	CreatedAt      time.Time
	DeliveredAt    *time.Time
	NextAttemptAt  time.Time
}

// Outbox event kinds. Each kind names the delivery step that consumes it.
const (
	// OutboxRunAnswer carries an agent run's final answer to the chat the
	// work was requested from (intake verdicts and routed target runs).
	OutboxRunAnswer = "run_answer"
	// OutboxConversationMessage carries a conversational answer (the lead
	// agent's direct turn) to the transport chat of its conversation.
	OutboxConversationMessage = "conversation_message"
)

const outboxColumns = `id, organization_id, conversation_id, event_type, payload, status, attempts,
last_error, created_at, delivered_at, next_attempt_at`

func scanOutboxEvent(scanner interface{ Scan(...any) error }) (ConversationOutboxEvent, error) {
	var e ConversationOutboxEvent
	var delivered *time.Time
	if err := scanner.Scan(&e.ID, &e.OrganizationID, &e.ConversationID, &e.EventType, &e.Payload,
		&e.Status, &e.Attempts, &e.LastError, &e.CreatedAt, &delivered, &e.NextAttemptAt); err != nil {
		return ConversationOutboxEvent{}, err
	}
	e.DeliveredAt = delivered
	return e, nil
}

// EnqueueConversationOutboxEvent persists one delivery unit. Idempotent by
// event id: a producer that retried after an unclear failure does not double
// the delivery.
func (s *Store) EnqueueConversationOutboxEvent(ctx context.Context, e *ConversationOutboxEvent) error {
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.EventType) == "" {
		return errors.New("outbox event id and event_type are required")
	}
	if e.Status == "" {
		e.Status = "pending"
	}
	payload := e.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO conversation_outbox (id, organization_id, conversation_id, event_type, payload, status)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (id) DO NOTHING`,
		e.ID, e.OrganizationID, e.ConversationID, e.EventType, payload, e.Status)
	return err
}

// ClaimDueConversationOutboxEvents atomically leases due events to one
// caller: rows flip to 'processing' under a SKIP LOCKED take, so concurrent
// kernels (or loop iterations) never double-deliver. Events stuck in
// 'processing' past the lease are re-claimed — the crash that orphaned them
// is exactly the failure the outbox exists to survive.
// The lease rides as $2::interval: pgx v5 encodes time.Duration as interval,
// but the bare `now()-$2` shape lets Postgres resolve the parameter to the
// datetime category's PREFERRED type (timestamptz), turning `now()-$2` into
// an interval and the comparison into `timestamptz <= interval` — 42883 on
// EVERY claim, the whole delivery pipeline silently dead (only warn logs).
// The explicit cast pins interval (same pattern as observation retention
// sweep); a rehearsal against production caught it before any row ever
// claimed.
func (s *Store) ClaimDueConversationOutboxEvents(ctx context.Context, limit int, lease time.Duration) ([]ConversationOutboxEvent, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE conversation_outbox SET status='processing', next_attempt_at=now()+$2::interval
		WHERE id IN (
			SELECT id FROM conversation_outbox
			WHERE (status='pending' AND next_attempt_at <= now())
			   OR (status='processing' AND next_attempt_at <= now()-$2::interval)
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+outboxColumns, limit, lease)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ConversationOutboxEvent, 0)
	for rows.Next() {
		event, scanErr := scanOutboxEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

// CompleteConversationOutboxEvent marks a delivered event terminal.
func (s *Store) CompleteConversationOutboxEvent(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE conversation_outbox SET status='delivered', delivered_at=now(), last_error='' WHERE id=$1`, id)
	return err
}

// FailConversationOutboxEvent records a failed attempt and schedules the
// retry with exponential backoff. Past the ceiling the event parks in
// 'failed' — visible, auditable, never silently retried forever.
func (s *Store) FailConversationOutboxEvent(ctx context.Context, id string, attempt int, reason error) error {
	message := ""
	if reason != nil {
		message = reason.Error()
	}
	if len(message) > 500 {
		message = message[:500]
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE conversation_outbox SET status=CASE WHEN $2 >= $3 THEN 'failed' ELSE 'pending' END,
		 attempts=$2, last_error=$4, next_attempt_at=now()+$5::interval
		 WHERE id=$1`, id, attempt, MaxOutboxAttempts, message, outboxBackoff(attempt))
	return err
}

// RetryConversationOutboxEvent reschedules a claimed event without charging
// the attempt ladder: "the run is still live" is progress, not failure.
func (s *Store) RetryConversationOutboxEvent(ctx context.Context, id string, delay time.Duration) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE conversation_outbox SET status='pending', next_attempt_at=now()+$2::interval WHERE id=$1 AND status='processing'`, id, delay)
	return err
}

// MaxOutboxAttempts caps the delivery retry ladder. After it, the event stays
// 'failed' for the audit trail; a human (or a future requeue tool) decides.
const MaxOutboxAttempts = 8

// outboxBackoff spaces retries exponentially: 30s, 1m, 2m, 4m … capped at an
// hour so a long transport outage still gets periodic probes.
func outboxBackoff(attempt int) time.Duration {
	backoff := 30 * time.Second
	for i := 1; i < attempt && backoff < time.Hour; i++ {
		backoff *= 2
	}
	if backoff > time.Hour {
		backoff = time.Hour
	}
	return backoff
}
