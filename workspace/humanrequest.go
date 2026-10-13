package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Human request storage (docs/org-structure.md §28). Rows are created by the
// notification activity at ask_human time, closed by the approval endpoint
// (answered/cancelled) or by the expiry sweep/activity (expired). Team
// requests (recipient org:<unit>, docs/inbound-messages.md §8) carry one
// delivery row per member; the first answer wins and supersedes the rest.

const humanRequestColumns = `id, run_id, project_id, agent_id, agent_version, recipient, resolved_user, question, context,
options, status, channel, response, answered_by, timeout_seconds, timeout_policy,
execution_identity_id, created_at, delivered_at, answered_at, expires_at`

func scanHumanRequest(scanner interface{ Scan(dest ...any) error }) (HumanRequest, error) {
	var r HumanRequest
	var options []byte
	var deliveredAt, answeredAt, expiresAt *time.Time
	err := scanner.Scan(&r.ID, &r.RunID, &r.ProjectID, &r.AgentID, &r.AgentVersion, &r.Recipient, &r.ResolvedUser,
		&r.Question, &r.Context, &options, &r.Status, &r.Channel, &r.Response, &r.AnsweredBy,
		&r.TimeoutSeconds, &r.TimeoutPolicy, &r.ExecutionIdentityID,
		&r.CreatedAt, &deliveredAt, &answeredAt, &expiresAt)
	if err != nil {
		return r, err
	}
	if len(options) > 0 {
		_ = json.Unmarshal(options, &r.Options)
	}
	r.DeliveredAt = deliveredAt
	r.AnsweredAt = answeredAt
	r.ExpiresAt = expiresAt
	return r, nil
}

// CreateHumanRequest inserts a new pending request. Id collisions (activity
// retry after a partial failure) update nothing — the row already exists.
func (s *Store) CreateHumanRequest(ctx context.Context, r *HumanRequest) error {
	if r.ID == "" || r.RunID == "" || r.ProjectID == "" || r.Question == "" {
		return errors.New("id, run_id, project_id and question are required")
	}
	if r.Status == "" {
		r.Status = HumanStatusPending
	}
	if r.Options == nil {
		r.Options = []string{}
	}
	var expiresAt any
	if r.ExpiresAt != nil {
		expiresAt = *r.ExpiresAt
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO human_request
		 (id, run_id, project_id, agent_id, agent_version, recipient, resolved_user, question, context, options,
		  status, channel, timeout_seconds, timeout_policy, execution_identity_id, created_at, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now(),$16)
		 ON CONFLICT (id) DO NOTHING`,
		r.ID, r.RunID, r.ProjectID, r.AgentID, r.AgentVersion, r.Recipient, r.ResolvedUser, r.Question, r.Context,
		marshalStrings(r.Options), r.Status, r.Channel, r.TimeoutSeconds, r.TimeoutPolicy,
		r.ExecutionIdentityID, expiresAt)
	return err
}

// DeliverHumanRequest records the resolution and delivery outcome: the
// concrete user the logical recipient resolved to and the channel used.
func (s *Store) DeliverHumanRequest(ctx context.Context, id, resolvedUser, status, channel, detail string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE human_request
		 SET resolved_user = $2, status = $3, channel = $4,
		     delivered_at = CASE WHEN $3 = 'delivered' THEN now() ELSE delivered_at END
		 WHERE id = $1 AND status = 'pending'`,
		id, resolvedUser, status, channel)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_ = detail // reserved for future delivery diagnostics on the row
	return nil
}

// Delivery row statuses: delivered while answerable, failed when the
// transport rejected it, superseded once another member answered first.
const (
	HumanDeliveryDelivered  = "delivered"
	HumanDeliveryFailed     = "failed"
	HumanDeliverySuperseded = "superseded"
)

// AddHumanRequestDeliveries records the team fan-out (one row per member,
// upsert so an activity retry refreshes rather than duplicates).
func (s *Store) AddHumanRequestDeliveries(ctx context.Context, requestID string, deliveries []HumanRequestDelivery) error {
	if len(deliveries) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, d := range deliveries {
		if d.Status == "" {
			d.Status = HumanDeliveryDelivered
		}
		batch.Queue(`INSERT INTO human_request_delivery (request_id, user_id, channel, status)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (request_id, user_id) DO UPDATE SET channel = EXCLUDED.channel, status = EXCLUDED.status`,
			requestID, d.UserID, d.Channel, d.Status)
	}
	return s.pool.SendBatch(ctx, batch).Close()
}

// ListOpenHumanRequestDeliveries returns the delivery rows of still-open
// requests: the channel inbound loop maps each row to a member whose chat may
// answer the request.
func (s *Store) ListOpenHumanRequestDeliveries(ctx context.Context) ([]HumanRequestDelivery, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT d.request_id, d.user_id, d.channel, d.status, d.created_at
		 FROM human_request_delivery d
		 JOIN human_request h ON h.id = d.request_id
		 WHERE h.status IN ('pending','delivered')
		 ORDER BY d.created_at, d.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []HumanRequestDelivery{}
	for rows.Next() {
		var d HumanRequestDelivery
		if err := rows.Scan(&d.RequestID, &d.UserID, &d.Channel, &d.Status, &d.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// supersedeDeliveries is the fan-out counterpart of closing a request: the
// copies that nobody answered stop being answerable. Observability only —
// open-request matching already excludes closed requests.
func (s *Store) supersedeDeliveries(ctx context.Context, requestIDs ...string) {
	if len(requestIDs) == 0 {
		return
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE human_request_delivery SET status = $2 WHERE request_id = ANY($1) AND status = 'delivered'`,
		requestIDs, HumanDeliverySuperseded)
}

// AnswerHumanRequest closes a request with the human's response. Only
// pending/delivered rows can be answered — a late answer after expiry is
// recorded but does not resurrect the wait. For team requests the first
// answer wins: later answers find the row closed and change nothing.
func (s *Store) AnswerHumanRequest(ctx context.Context, id, response, answeredBy string) (HumanRequest, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE human_request
		 SET status = 'answered', response = $2, answered_by = $3, answered_at = now()
		 WHERE id = $1 AND status IN ('pending','delivered')`,
		id, response, answeredBy)
	if err != nil {
		return HumanRequest{}, err
	}
	if tag.RowsAffected() > 0 {
		// Team fan-out: the first answer wins, the other members' copies close.
		s.supersedeDeliveries(ctx, id)
	}
	if tag.RowsAffected() == 0 {
		// Either unknown id or already closed; distinguish for the caller.
		if _, err := s.GetHumanRequest(ctx, id); err != nil {
			return HumanRequest{}, err
		}
	}
	return s.GetHumanRequest(ctx, id)
}

// CancelHumanRequest marks an open request cancelled (the human declined to
// answer or the run was cancelled outright).
func (s *Store) CancelHumanRequest(ctx context.Context, id, actor string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE human_request
		 SET status = 'cancelled', answered_by = $2, answered_at = now()
		 WHERE id = $1 AND status IN ('pending','delivered')`,
		id, actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		s.supersedeDeliveries(ctx, id)
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.GetHumanRequest(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// ExpireHumanRequests sweeps overdue requests (docs/org-structure.md §33):
// lazy expiry for rows the workflow's close-activity could not reach. Returns
// the expired ids for audit.
func (s *Store) ExpireHumanRequests(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE human_request
		 SET status = 'expired'
		 WHERE status IN ('pending','delivered') AND expires_at IS NOT NULL AND expires_at < now()
		 RETURNING id`)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.supersedeDeliveries(ctx, ids...)
	return ids, nil
}

// ExpireHumanRequestByID expires one request if it is still open. Used by
// the workflow's close activity; ErrNotFound when the row does not exist.
func (s *Store) ExpireHumanRequestByID(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE human_request
		 SET status = 'expired'
		 WHERE id = $1 AND status IN ('pending','delivered')`, id)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		s.supersedeDeliveries(ctx, id)
		return true, nil
	}
	if _, err := s.GetHumanRequest(ctx, id); err != nil {
		return false, err
	}
	return false, nil // already closed by someone else
}

// GetHumanRequest fetches one row by id (= ask operation id).
func (s *Store) GetHumanRequest(ctx context.Context, id string) (HumanRequest, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+humanRequestColumns+` FROM human_request WHERE id = $1`, id)
	r, err := scanHumanRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// ListHumanRequests returns requests filtered by project and/or status and/or
// the resolved recipient, newest first. nil filter values are skipped.
func (s *Store) ListHumanRequests(ctx context.Context, project, status, resolvedUser string, onlyOpen bool) ([]HumanRequest, error) {
	query := `SELECT ` + humanRequestColumns + ` FROM human_request WHERE 1=1`
	args := []any{}
	if project != "" {
		args = append(args, project)
		query += ` AND project_id = $` + strconv.Itoa(len(args))
	}
	if status != "" {
		args = append(args, status)
		query += ` AND status = $` + strconv.Itoa(len(args))
	}
	if resolvedUser != "" {
		args = append(args, resolvedUser)
		query += ` AND resolved_user = $` + strconv.Itoa(len(args))
	}
	if onlyOpen {
		query += ` AND status IN ('pending','delivered')`
	}
	query += ` ORDER BY created_at DESC LIMIT 200`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []HumanRequest{}
	for rows.Next() {
		r, err := scanHumanRequest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ListHumanRequestsByRun returns every request of one run, oldest first —
// the verdict projection reads the whole ask history of the run
// (specs/quality-loop.md §3; served by idx_human_request_run).
func (s *Store) ListHumanRequestsByRun(ctx context.Context, runID string) ([]HumanRequest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+humanRequestColumns+` FROM human_request WHERE run_id = $1 ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []HumanRequest{}
	for rows.Next() {
		r, err := scanHumanRequest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
