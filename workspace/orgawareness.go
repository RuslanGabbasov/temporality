package workspace

// Organizational Supervisor batches + awareness delivery ledger
// (docs/temporality-organizational-operations-spec.md §6, §8). Decisions are
// the supervisor's output; deliveries are the addressable lifecycle rows the
// steer worker and the Awareness Flow UI read. The (deduplication_key,
// target_run_id) unique index is the repetition guard: retries and re-folded
// windows can never enqueue a second copy for the same run.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Delivery lifecycle states (spec §8).
const (
	OrgDeliverySelected           = "selected"
	OrgDeliveryQueued             = "queued"
	OrgDeliveryDelivered          = "delivered"
	OrgDeliveryConsumed           = "consumed"
	OrgDeliveryFollowUpRequested  = "follow_up_requested"
	OrgDeliveryDetailsDelivered   = "details_delivered"
	OrgDeliveryActedUpon          = "acted_upon"
	OrgDeliveryDismissed          = "dismissed"
	OrgDeliveryExpired            = "expired"
	OrgDeliveryFailed             = "failed"
)

// Supervisor decision kinds (spec §6.3).
const (
	OrgDecisionIgnore           = "ignore"
	OrgDecisionNotify           = "notify"
	OrgDecisionRecommendLookup  = "recommend_lookup"
	OrgDecisionUrgentSteer      = "urgent_steer"
	OrgDecisionUpdateAwareness  = "update_awareness"
)

// ValidOrgDecisions is the closed set the supervisor model may emit.
var ValidOrgDecisions = map[string]bool{
	OrgDecisionIgnore:          true,
	OrgDecisionNotify:          true,
	OrgDecisionRecommendLookup: true,
	OrgDecisionUrgentSteer:     true,
	OrgDecisionUpdateAwareness: true,
}

// Batch statuses.
const (
	OrgBatchPending   = "pending"
	OrgBatchProcessing = "processing"
	OrgBatchCompleted = "completed"
	OrgBatchFailed    = "failed"
)

var orgActionableDecisions = map[string]bool{
	OrgDecisionNotify:          true,
	OrgDecisionRecommendLookup: true,
	OrgDecisionUrgentSteer:     true,
	OrgDecisionUpdateAwareness: true,
}

// OrgAwarenessDecision is one §6.4 output row.
type OrgAwarenessDecision struct {
	ID              string          `json:"id"`
	BatchID         string          `json:"batch_id"`
	OrgNodeID       string          `json:"org_node_id,omitempty"`
	SourceEventID   string          `json:"source_event_id,omitempty"`
	SourceRunID     string          `json:"source_run_id,omitempty"`
	TargetRunID     string          `json:"target_run_id,omitempty"`
	Decision        string          `json:"decision"`
	Relevance       string          `json:"relevance"`
	Reason          string          `json:"reason,omitempty"`
	Message         string          `json:"message,omitempty"`
	SuggestedLookup json.RawMessage `json:"suggested_lookup,omitempty"`
	Urgency         string          `json:"urgency"`
	DeduplicationKey string         `json:"deduplication_key,omitempty"`
	Model           string          `json:"model,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

// OrgAwarenessDelivery is one §8 lifecycle row.
type OrgAwarenessDelivery struct {
	ID               string          `json:"id"`
	DecisionID       string          `json:"decision_id,omitempty"`
	BatchID          string          `json:"batch_id,omitempty"`
	OrgNodeID        string          `json:"org_node_id,omitempty"`
	ProjectID        string          `json:"project_id,omitempty"`
	SourceEventID    string          `json:"source_event_id,omitempty"`
	SourceRunID      string          `json:"source_run_id,omitempty"`
	SourceSummary    string          `json:"source_summary,omitempty"`
	TargetRunID      string          `json:"target_run_id"`
	TargetActorID    string          `json:"target_actor_id,omitempty"`
	Decision         string          `json:"decision"`
	Relevance        string          `json:"relevance"`
	Urgency          string          `json:"urgency"`
	Reason           string          `json:"reason,omitempty"`
	Message          string          `json:"message,omitempty"`
	DetailLevel      string          `json:"detail_level,omitempty"`
	Status           string          `json:"status"`
	MessageRef       string          `json:"message_ref,omitempty"`
	Attempts         int             `json:"attempts"`
	LastError        string          `json:"last_error,omitempty"`
	DeduplicationKey string          `json:"deduplication_key,omitempty"`
	FollowUpRefs     json.RawMessage `json:"follow_up_refs,omitempty"`
	RelatedActionRefs json.RawMessage `json:"related_action_refs,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	QueuedAt         *time.Time      `json:"queued_at,omitempty"`
	DeliveredAt      *time.Time      `json:"delivered_at,omitempty"`
	ConsumedAt       *time.Time      `json:"consumed_at,omitempty"`
	ClosedAt         *time.Time      `json:"closed_at,omitempty"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// OrgActivityBatch is the §6.2 batch record.
type OrgActivityBatch struct {
	ID            string     `json:"id"`
	OrgNodeID     string     `json:"org_node_id"`
	Status        string     `json:"status"`
	Trigger       string     `json:"trigger"`
	WindowFrom    *time.Time `json:"window_from,omitempty"`
	WindowTo      *time.Time `json:"window_to,omitempty"`
	EventCount    int        `json:"event_count"`
	DecisionCount int        `json:"decision_count"`
	DeliveryCount int        `json:"delivery_count"`
	Empty         bool       `json:"empty"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// CreateOrgActivityBatch opens the batch record.
func (s *Store) CreateOrgActivityBatch(ctx context.Context, b OrgActivityBatch) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_activity_batch (id, org_node_id, status, trigger, window_from, window_to, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,now())`,
		b.ID, b.OrgNodeID, b.Status, b.Trigger, b.WindowFrom, b.WindowTo)
	return err
}

// StartOrgActivityBatch marks the pass as running.
func (s *Store) StartOrgActivityBatch(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE org_activity_batch SET status = $2, started_at = now() WHERE id = $1`, id, OrgBatchProcessing)
	return err
}

// CompleteOrgActivityBatch closes a successful pass and advances the node's
// schedule window. One transaction: the window never moves past a failed
// batch (spec §6: «watermark не продвигается до успешного завершения»).
func (s *Store) CompleteOrgActivityBatch(ctx context.Context, b OrgActivityBatch) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE org_activity_batch SET status = $2, event_count = $3, decision_count = $4, delivery_count = $5,
		    empty = $6, error = '', window_from = COALESCE($7, window_from), window_to = COALESCE($8, window_to), completed_at = now()
		 WHERE id = $1`,
		b.ID, OrgBatchCompleted, b.EventCount, b.DecisionCount, b.DeliveryCount, b.Empty, b.WindowFrom, b.WindowTo); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO org_activity_batch_state (org_node_id, last_batch_id, window_to, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (org_node_id) DO UPDATE SET
		   last_batch_id = EXCLUDED.last_batch_id,
		   window_to = EXCLUDED.window_to,
		   updated_at = now()`,
		b.OrgNodeID, b.ID, b.WindowTo); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FailOrgActivityBatch records the failure WITHOUT advancing the node
// window — the next tick reprocesses the same events (decisions dedupe).
func (s *Store) FailOrgActivityBatch(ctx context.Context, id, reason string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE org_activity_batch SET status = $2, error = $3, completed_at = now() WHERE id = $1`,
		id, OrgBatchFailed, reason)
	return err
}

// GetOrgActivityBatch returns one batch record.
func (s *Store) GetOrgActivityBatch(ctx context.Context, id string) (OrgActivityBatch, error) {
	var b OrgActivityBatch
	err := s.pool.QueryRow(ctx,
		`SELECT id, org_node_id, status, trigger, window_from, window_to, event_count, decision_count,
		        delivery_count, empty, error, created_at, started_at, completed_at
		 FROM org_activity_batch WHERE id = $1`, id).
		Scan(&b.ID, &b.OrgNodeID, &b.Status, &b.Trigger, &b.WindowFrom, &b.WindowTo,
			&b.EventCount, &b.DecisionCount, &b.DeliveryCount, &b.Empty, &b.Error,
			&b.CreatedAt, &b.StartedAt, &b.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// OrgNodeBatchWindow returns the node's last completed window end (zero:
// never batched — the first pass covers the lookback window).
func (s *Store) OrgNodeBatchWindow(ctx context.Context, orgNodeID string) (time.Time, string, error) {
	var at time.Time
	var batchID string
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(window_to, 'epoch'::timestamptz), last_batch_id FROM org_activity_batch_state WHERE org_node_id = $1`,
		orgNodeID).Scan(&at, &batchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, "", nil
	}
	if err != nil {
		// epoch sentinel: return zero time for a never-batched node
		if at.Equal(time.Unix(0, 0)) {
			return time.Time{}, batchID, nil
		}
		return at, batchID, err
	}
	if at.Equal(time.Unix(0, 0)) {
		return time.Time{}, batchID, nil
	}
	return at, batchID, nil
}

// OrgAwarenessRunCounts aggregates the delivery lifecycle states per run —
// the Work Board card badges and the Awareness Flow counters (§12).
type OrgAwarenessRunCounts struct {
	RunID            string `json:"run_id"`
	Selected         int    `json:"selected"`
	Delivered        int    `json:"delivered"`
	Consumed         int    `json:"consumed"`
	DetailsRequested int    `json:"details_requested"`
}

// OrgAwarenessCountsForRuns computes the counts for many runs in one query.
func (s *Store) OrgAwarenessCountsForRuns(ctx context.Context, runIDs []string) (map[string]OrgAwarenessRunCounts, error) {
	out := map[string]OrgAwarenessRunCounts{}
	if len(runIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT target_run_id,
		        COUNT(*) FILTER (WHERE status IN ($2, $3)),
		        COUNT(*) FILTER (WHERE delivered_at IS NOT NULL),
		        COUNT(*) FILTER (WHERE consumed_at IS NOT NULL),
		        COUNT(*) FILTER (WHERE status IN ($4, $5, $6))
		 FROM org_awareness_delivery WHERE target_run_id = ANY($1)
		 GROUP BY target_run_id`,
		textListArg(runIDs), OrgDeliverySelected, OrgDeliveryQueued,
		OrgDeliveryFollowUpRequested, OrgDeliveryDetailsDelivered, OrgDeliveryActedUpon)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c OrgAwarenessRunCounts
		if err := rows.Scan(&c.RunID, &c.Selected, &c.Delivered, &c.Consumed, &c.DetailsRequested); err != nil {
			return nil, err
		}
		out[c.RunID] = c
	}
	return out, rows.Err()
}

// ListOrgActivityBatches serves the batch listing (get_activity_batch
// collection, §15).
func (s *Store) ListOrgActivityBatches(ctx context.Context, orgNodeIDs []string, limit, offset int) ([]OrgActivityBatch, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args := []any{}
	where := []string{}
	if len(orgNodeIDs) > 0 {
		args = append(args, textListArg(orgNodeIDs))
		where = append(where, fmt.Sprintf("org_node_id = ANY($%d)", len(args)))
	}
	condition := ""
	if len(where) > 0 {
		condition = " WHERE " + strings.Join(where, " AND ")
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM org_activity_batch`+condition, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, org_node_id, status, trigger, window_from, window_to, event_count, decision_count,
		        delivery_count, empty, error, created_at, started_at, completed_at
		 FROM org_activity_batch`+condition+
			` ORDER BY created_at DESC LIMIT `+fmt.Sprint(limit)+` OFFSET `+fmt.Sprint(offset), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	batches := []OrgActivityBatch{}
	for rows.Next() {
		var b OrgActivityBatch
		if err := rows.Scan(&b.ID, &b.OrgNodeID, &b.Status, &b.Trigger, &b.WindowFrom, &b.WindowTo,
			&b.EventCount, &b.DecisionCount, &b.DeliveryCount, &b.Empty, &b.Error,
			&b.CreatedAt, &b.StartedAt, &b.CompletedAt); err != nil {
			return nil, 0, err
		}
		batches = append(batches, b)
	}
	return batches, count, rows.Err()
}

// OrgNodeBatchCompletedAt returns when the node's last batch COMPLETED (the
// state row's updated_at) — the scheduler's due check. Zero: never batched.
func (s *Store) OrgNodeBatchCompletedAt(ctx context.Context, orgNodeID string) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT updated_at FROM org_activity_batch_state WHERE org_node_id = $1`, orgNodeID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return at, err
}

// DueOrgActivityBatchNode resolves the earliest pending manual batch, if any
// (the process_activity_batch API marks a batch due; the loop honors it).
func (s *Store) DueOrgActivityBatchNode(ctx context.Context) (string, string, error) {
	var id, node string
	err := s.pool.QueryRow(ctx,
		`SELECT id, org_node_id FROM org_activity_batch
		 WHERE trigger = 'manual' AND status = 'pending' AND created_at < now() - interval '2 seconds'
		 ORDER BY created_at LIMIT 1`).Scan(&id, &node)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return id, node, err
}

const orgDeliveryColumns = `id, decision_id, batch_id, org_node_id, project_id, source_event_id,
source_run_id, source_summary, target_run_id, target_actor_id, decision, relevance, urgency,
reason, message, detail_level, status, message_ref, attempts, last_error, deduplication_key,
follow_up_refs, related_action_refs, created_at, queued_at, delivered_at, consumed_at, closed_at, updated_at`

func scanOrgDelivery(scanner interface{ Scan(dest ...any) error }) (OrgAwarenessDelivery, error) {
	var d OrgAwarenessDelivery
	var followUp, related []byte
	err := scanner.Scan(&d.ID, &d.DecisionID, &d.BatchID, &d.OrgNodeID, &d.ProjectID, &d.SourceEventID,
		&d.SourceRunID, &d.SourceSummary, &d.TargetRunID, &d.TargetActorID, &d.Decision, &d.Relevance,
		&d.Urgency, &d.Reason, &d.Message, &d.DetailLevel, &d.Status, &d.MessageRef, &d.Attempts,
		&d.LastError, &d.DeduplicationKey, &followUp, &related, &d.CreatedAt, &d.QueuedAt,
		&d.DeliveredAt, &d.ConsumedAt, &d.ClosedAt, &d.UpdatedAt)
	if err != nil {
		return d, err
	}
	if len(followUp) > 0 {
		d.FollowUpRefs = json.RawMessage(followUp)
	}
	if len(related) > 0 {
		d.RelatedActionRefs = json.RawMessage(related)
	}
	return d, nil
}

const orgDecisionColumns = `id, batch_id, org_node_id, source_event_id, source_run_id, target_run_id,
decision, relevance, reason, message, suggested_lookup, urgency, deduplication_key, model, created_at`

func scanOrgDecision(scanner interface{ Scan(dest ...any) error }) (OrgAwarenessDecision, error) {
	var d OrgAwarenessDecision
	var lookup []byte
	err := scanner.Scan(&d.ID, &d.BatchID, &d.OrgNodeID, &d.SourceEventID, &d.SourceRunID, &d.TargetRunID,
		&d.Decision, &d.Relevance, &d.Reason, &d.Message, &lookup, &d.Urgency, &d.DeduplicationKey,
		&d.Model, &d.CreatedAt)
	if err != nil {
		return d, err
	}
	if len(lookup) > 0 {
		d.SuggestedLookup = json.RawMessage(lookup)
	}
	return d, nil
}

// CreateOrgAwarenessDecision persists one decision. ErrConflict means the
// deduplication key already produced an actionable decision (the supervisor's
// repeat suppression, spec §6: «подавлять повторы»).
func (s *Store) CreateOrgAwarenessDecision(ctx context.Context, d OrgAwarenessDecision) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_awareness_decision (id, batch_id, org_node_id, source_event_id, source_run_id,
		    target_run_id, decision, relevance, reason, message, suggested_lookup, urgency, deduplication_key, model)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		d.ID, d.BatchID, d.OrgNodeID, d.SourceEventID, d.SourceRunID, d.TargetRunID,
		d.Decision, d.Relevance, d.Reason, d.Message, []byte(d.SuggestedLookup), d.Urgency,
		d.DeduplicationKey, d.Model)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// ListOrgAwarenessDecisions serves get_awareness_decisions (spec §15).
func (s *Store) ListOrgAwarenessDecisions(ctx context.Context, batchID, orgNodeID, decision string, limit, offset int) ([]OrgAwarenessDecision, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args := []any{}
	where := []string{}
	add := func(value any, condition string) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	if batchID != "" {
		add(batchID, "batch_id = $%d")
	}
	if orgNodeID != "" {
		add(orgNodeID, "org_node_id = $%d")
	}
	if decision != "" {
		add(decision, "decision = $%d")
	}
	condition := ""
	if len(where) > 0 {
		condition = " WHERE " + strings.Join(where, " AND ")
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM org_awareness_decision`+condition, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+orgDecisionColumns+` FROM org_awareness_decision`+condition+
			` ORDER BY created_at DESC LIMIT `+fmt.Sprint(limit)+` OFFSET `+fmt.Sprint(offset), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	decisions := []OrgAwarenessDecision{}
	for rows.Next() {
		d, err := scanOrgDecision(rows)
		if err != nil {
			return nil, 0, err
		}
		decisions = append(decisions, d)
	}
	return decisions, count, rows.Err()
}

// OrgDeliveryExists reports whether an actionable delivery with this dedup
// key already reached this target run — the in-memory check before insert
// (the unique index is the last resort).
func (s *Store) OrgDeliveryExists(ctx context.Context, deduplicationKey, targetRunID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM org_awareness_delivery WHERE deduplication_key = $1 AND target_run_id = $2)`,
		deduplicationKey, targetRunID).Scan(&exists)
	return exists, err
}

// CreateOrgAwarenessDelivery inserts the lifecycle row (status selected).
// ErrConflict = the dedup key already delivered to this run.
func (s *Store) CreateOrgAwarenessDelivery(ctx context.Context, d OrgAwarenessDelivery) error {
	if d.TargetRunID == "" {
		return errors.New("target_run_id is required")
	}
	if d.Status == "" {
		d.Status = OrgDeliverySelected
	}
	if d.FollowUpRefs == nil {
		d.FollowUpRefs = json.RawMessage("[]")
	}
	if d.RelatedActionRefs == nil {
		d.RelatedActionRefs = json.RawMessage("[]")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_awareness_delivery (id, decision_id, batch_id, org_node_id, project_id,
		    source_event_id, source_run_id, source_summary, target_run_id, target_actor_id,
		    decision, relevance, urgency, reason, message, detail_level, status, deduplication_key,
		    follow_up_refs, related_action_refs)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		d.ID, d.DecisionID, d.BatchID, d.OrgNodeID, d.ProjectID,
		d.SourceEventID, d.SourceRunID, d.SourceSummary, d.TargetRunID, d.TargetActorID,
		d.Decision, d.Relevance, d.Urgency, d.Reason, d.Message, d.DetailLevel, d.Status,
		d.DeduplicationKey, []byte(d.FollowUpRefs), []byte(d.RelatedActionRefs))
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// QueueOrgAwarenessDeliveries moves selected rows to queued (the delivery
// worker's claim) and returns them. Urgent rows go first.
func (s *Store) QueueOrgAwarenessDeliveries(ctx context.Context, limit int) ([]OrgAwarenessDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`UPDATE org_awareness_delivery
		 SET status = $2, queued_at = now(), updated_at = now()
		 WHERE id IN (
		     SELECT id FROM org_awareness_delivery
		     WHERE status = $1
		     ORDER BY (urgency = 'high') DESC, created_at
		     LIMIT $3
		 )
		 RETURNING `+orgDeliveryColumns,
		OrgDeliverySelected, OrgDeliveryQueued, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := []OrgAwarenessDelivery{}
	for rows.Next() {
		d, err := scanOrgDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

// PendingOrgAwarenessDeliveries returns queued rows due for a delivery
// attempt (next_attempt semantics via attempts and backoff computed by the
// caller).
func (s *Store) PendingOrgAwarenessDeliveries(ctx context.Context, limit int) ([]OrgAwarenessDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+orgDeliveryColumns+` FROM org_awareness_delivery
		 WHERE status = $1
		 ORDER BY (urgency = 'high') DESC, queued_at, created_at
		 LIMIT $2`, OrgDeliveryQueued, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := []OrgAwarenessDelivery{}
	for rows.Next() {
		d, err := scanOrgDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

// MarkOrgAwarenessDeliveryDelivered records a successful steer signal.
func (s *Store) MarkOrgAwarenessDeliveryDelivered(ctx context.Context, id, messageRef string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE org_awareness_delivery
		 SET status = $2, message_ref = $3, delivered_at = now(), last_error = '', attempts = attempts + 1, updated_at = now()
		 WHERE id = $1 AND status IN ($4, $5)`,
		id, OrgDeliveryDelivered, messageRef, OrgDeliveryQueued, OrgDeliverySelected)
	return err
}

// RetryOrgAwarenessDelivery records a failed attempt; the row stays queued
// until the attempt budget runs out, then it fails terminally.
func (s *Store) RetryOrgAwarenessDelivery(ctx context.Context, id string, maxAttempts int, attemptErr error) (retry bool, err error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE org_awareness_delivery
		 SET status = CASE WHEN attempts + 1 >= $2 THEN $3 ELSE status END,
		     closed_at = CASE WHEN attempts + 1 >= $2 THEN now() ELSE closed_at END,
		     last_error = $4, attempts = attempts + 1, updated_at = now()
		 WHERE id = $1 AND status = $5`,
		id, maxAttempts, OrgDeliveryFailed, attemptErr.Error(), OrgDeliveryQueued)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	var attempts int
	var status string
	if err := s.pool.QueryRow(ctx,
		`SELECT attempts, status FROM org_awareness_delivery WHERE id = $1`, id).Scan(&attempts, &status); err != nil {
		return false, err
	}
	return status == OrgDeliveryQueued && attempts < maxAttempts, nil
}

// ExpireOrgAwarenessDeliveriesForRun closes queued deliveries for a run that
// finished before delivery (spec §22 scenario 5: «уведомление истекает»).
func (s *Store) ExpireOrgAwarenessDeliveriesForRun(ctx context.Context, targetRunID, reason string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE org_awareness_delivery
		 SET status = $2, last_error = $3, closed_at = now(), updated_at = now()
		 WHERE target_run_id = $1 AND status IN ($4, $5)`,
		targetRunID, OrgDeliveryExpired, reason, OrgDeliveryQueued, OrgDeliverySelected)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MarkOrgAwarenessDeliveryConsumed correlates the workflow's own run.user_note
// journal event (the marker carries the delivery id) with the delivery:
// steer reached the model context (spec §8 — consumed proves placement, not
// semantic use).
func (s *Store) MarkOrgAwarenessDeliveryConsumed(ctx context.Context, id string, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE org_awareness_delivery
		 SET status = $2, consumed_at = $3, updated_at = now()
		 WHERE id = $1 AND status = $4 AND consumed_at IS NULL`,
		id, OrgDeliveryConsumed, at, OrgDeliveryDelivered)
	return err
}

// AppendOrgAwarenessDeliveryFollowUp records an explicit follow-up reference
// (request_related_run_details / record_awareness_follow_up) and advances the
// lifecycle. acted_upon requires the explicit action_ref — temporal
// proximity is never enough (spec §8).
func (s *Store) AppendOrgAwarenessDeliveryFollowUp(ctx context.Context, id, ref string, status string) (OrgAwarenessDelivery, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE org_awareness_delivery
		 SET follow_up_refs = follow_up_refs || $2::jsonb,
		     status = CASE WHEN $3 <> '' THEN $3 ELSE status END,
		     closed_at = CASE WHEN $3 = $4 THEN now() ELSE closed_at END,
		     updated_at = now()
		 WHERE id = $1
		 RETURNING `+orgDeliveryColumns,
		id, mustJSONString(ref), status, OrgDeliveryActedUpon)
	d, err := scanOrgDelivery(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// AppendOrgAwarenessDeliveryAction registers an explicitly correlated action
// ref and marks the delivery acted_upon.
func (s *Store) AppendOrgAwarenessDeliveryAction(ctx context.Context, id, actionRef string) (OrgAwarenessDelivery, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE org_awareness_delivery
		 SET related_action_refs = related_action_refs || $2::jsonb,
		     status = $3, closed_at = now(), updated_at = now()
		 WHERE id = $1
		 RETURNING `+orgDeliveryColumns,
		id, mustJSONString(actionRef), OrgDeliveryActedUpon)
	d, err := scanOrgDelivery(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DismissOrgAwarenessDelivery closes a delivery as dismissed by the target.
func (s *Store) DismissOrgAwarenessDelivery(ctx context.Context, id string) (OrgAwarenessDelivery, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE org_awareness_delivery
		 SET status = $2, closed_at = now(), updated_at = now()
		 WHERE id = $1 AND status NOT IN ($3, $4, $5)
		 RETURNING `+orgDeliveryColumns,
		id, OrgDeliveryDismissed, OrgDeliveryDismissed, OrgDeliveryExpired, OrgDeliveryFailed)
	d, err := scanOrgDelivery(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// distinguish "not found" from "already closed"
		var exists bool
		if e := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM org_awareness_delivery WHERE id = $1)`, id).Scan(&exists); e == nil && exists {
			return d, ErrConflict
		}
		return d, ErrNotFound
	}
	return d, err
}

// OrgDeliveryFilter narrows list_run_awareness / get_awareness_flow.
type OrgDeliveryFilter struct {
	BatchID       string
	OrgNodeIDs    []string
	SourceRunID   string
	TargetRunID   string
	SourceEventID string
	Decision      string
	Status        string
	ActorID       string
	From          *time.Time
	To            *time.Time
	Limit         int
	Offset        int
}

// ListOrgAwarenessDeliveries serves the run history and the flow views.
func (s *Store) ListOrgAwarenessDeliveries(ctx context.Context, filter OrgDeliveryFilter) ([]OrgAwarenessDelivery, int, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 50
	}
	args := []any{}
	where := []string{}
	add := func(value any, condition string) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	if filter.BatchID != "" {
		add(filter.BatchID, "batch_id = $%d")
	}
	if len(filter.OrgNodeIDs) > 0 {
		add(textListArg(filter.OrgNodeIDs), "org_node_id = ANY($%d)")
	}
	if filter.SourceRunID != "" {
		add(filter.SourceRunID, "source_run_id = $%d")
	}
	if filter.TargetRunID != "" {
		add(filter.TargetRunID, "target_run_id = $%d")
	}
	if filter.SourceEventID != "" {
		add(filter.SourceEventID, "source_event_id = $%d")
	}
	if filter.Decision != "" {
		add(filter.Decision, "decision = $%d")
	}
	if filter.Status != "" {
		add(filter.Status, "status = $%d")
	}
	if filter.ActorID != "" {
		add(filter.ActorID, "target_actor_id = $%d")
	}
	if filter.From != nil {
		add(*filter.From, "created_at >= $%d")
	}
	if filter.To != nil {
		add(*filter.To, "created_at <= $%d")
	}
	condition := ""
	if len(where) > 0 {
		condition = " WHERE " + strings.Join(where, " AND ")
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM org_awareness_delivery`+condition, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+orgDeliveryColumns+` FROM org_awareness_delivery`+condition+
			` ORDER BY created_at DESC LIMIT `+fmt.Sprint(filter.Limit)+` OFFSET `+fmt.Sprint(filter.Offset), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	deliveries := []OrgAwarenessDelivery{}
	for rows.Next() {
		d, err := scanOrgDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, count, rows.Err()
}

// GetOrgAwarenessDelivery fetches one lifecycle row.
func (s *Store) GetOrgAwarenessDelivery(ctx context.Context, id string) (OrgAwarenessDelivery, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+orgDeliveryColumns+` FROM org_awareness_delivery WHERE id = $1`, id)
	d, err := scanOrgDelivery(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// CountOrgDeliveriesForRunSince enforces max_deliveries_per_run_per_window
// (spec §5, §7.2).
func (s *Store) CountOrgDeliveriesForRunSince(ctx context.Context, targetRunID string, since time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM org_awareness_delivery
		 WHERE target_run_id = $1 AND created_at >= $2 AND status NOT IN ($3, $4)`,
		targetRunID, since, OrgDeliveryExpired, OrgDeliveryFailed).Scan(&count)
	return count, err
}

// CountOrgDeliveriesForEventSince enforces max_recipients_per_event.
func (s *Store) CountOrgDeliveriesForEventSince(ctx context.Context, sourceEventID string, since time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM org_awareness_delivery
		 WHERE source_event_id = $1 AND created_at >= $2 AND status NOT IN ($3, $4)`,
		sourceEventID, since, OrgDeliveryExpired, OrgDeliveryFailed).Scan(&count)
	return count, err
}

// ListOrgDeliveriesForConsumption returns delivered deliveries whose
// marker may have been consumed by the target run recently — the fold loop
// correlates run.user_note markers against these ids.
func (s *Store) ListOrgDeliveriesForConsumption(ctx context.Context, targetRunID string, limit int) ([]OrgAwarenessDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+orgDeliveryColumns+` FROM org_awareness_delivery
		 WHERE target_run_id = $1 AND status = $2
		 ORDER BY delivered_at DESC LIMIT $3`, targetRunID, OrgDeliveryDelivered, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := []OrgAwarenessDelivery{}
	for rows.Next() {
		d, err := scanOrgDelivery(rows)
		if err != nil {
			return nil, err
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

// PruneOrgAwarenessDeliveries enforces delivery_retention (spec §5).
func (s *Store) PruneOrgAwarenessDeliveries(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM org_awareness_delivery WHERE created_at < $1 AND status IN ($2, $3, $4, $5)`,
		olderThan, OrgDeliveryDismissed, OrgDeliveryExpired, OrgDeliveryFailed, OrgDeliveryActedUpon)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// mustJSONString wraps a plain string ref into a JSON string literal for the
// || jsonb append.
func mustJSONString(value string) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`""`)
	}
	return encoded
}

// isUniqueViolation reports a SQLSTATE 23505 — the dedup indexes' verdict.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
