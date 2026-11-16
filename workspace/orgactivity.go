package workspace

// Organizational Activity projection (docs/temporality-organizational-
// operations-spec.md §4): derived rows folded from the journal — one row per
// source event keyed by the journal's own (source_id, event_id) identity so a
// replayed page can never create a duplicate (spec §21: «Дубликаты не
// создаются»). The org attribution is a launch-time SNAPSHOT resolved once
// per run (spec §3: «Перевод сотрудника не перемещает исторические события»).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// OrgActivityEvent is one folded projection row (the §4.3 logical contract,
// fields reconciled with the actual observation.Event envelope — see
// docs/organizational-operations-survey.md).
type OrgActivityEvent struct {
	SourceID     string          `json:"source_id"`
	EventID      string          `json:"event_id"`
	EventType    string          `json:"event_type"`
	Category     string          `json:"category"`
	OccurredAt   time.Time       `json:"occurred_at"`
	ReceivedAt   *time.Time      `json:"received_at,omitempty"`
	ProjectID    string          `json:"project_id,omitempty"`
	RunID        string          `json:"run_id,omitempty"`
	TaskID       string          `json:"task_id,omitempty"`
	PlanID       string          `json:"plan_id,omitempty"`
	ActorID      string          `json:"actor_id,omitempty"`
	OrgNodeID    string          `json:"org_node_id,omitempty"`
	OrgNodePath  string          `json:"org_node_path,omitempty"`
	Summary      string          `json:"summary"`
	Visibility   string          `json:"visibility_policy"`
	Refs         json.RawMessage `json:"refs,omitempty"`
	SourceRef    json.RawMessage `json:"source_ref,omitempty"`
	SchemaVersion int            `json:"schema_version"`
}

// OrgActiveRun is the active-run snapshot row (§4: «получение активных Run»).
type OrgActiveRun struct {
	ProjectID    string     `json:"project_id"`
	RunID        string     `json:"run_id"`
	SourceID     string     `json:"source_id,omitempty"`
	OrgNodeID    string     `json:"org_node_id,omitempty"`
	OrgNodePath  string     `json:"org_node_path,omitempty"`
	ActorID      string     `json:"actor_id,omitempty"`
	AgentID      string     `json:"agent_id,omitempty"`
	TaskID       string     `json:"task_id,omitempty"`
	Title        string     `json:"title,omitempty"`
	Status       string     `json:"status"`
	Urgent       bool       `json:"urgent"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	LastEventAt  *time.Time `json:"last_event_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

const orgActivityColumns = `source_id, event_id, event_type, category, occurred_at, received_at,
project_id, run_id, task_id, plan_id, actor_id, org_node_id, org_node_path,
summary, visibility_policy, refs, source_ref, schema_version`

func scanOrgActivityEvent(scanner interface{ Scan(dest ...any) error }) (OrgActivityEvent, error) {
	var e OrgActivityEvent
	var refs, sourceRef []byte
	err := scanner.Scan(&e.SourceID, &e.EventID, &e.EventType, &e.Category, &e.OccurredAt, &e.ReceivedAt,
		&e.ProjectID, &e.RunID, &e.TaskID, &e.PlanID, &e.ActorID, &e.OrgNodeID, &e.OrgNodePath,
		&e.Summary, &e.Visibility, &refs, &sourceRef, &e.SchemaVersion)
	if err != nil {
		return e, err
	}
	if len(refs) > 0 {
		e.Refs = json.RawMessage(refs)
	}
	if len(sourceRef) > 0 {
		e.SourceRef = json.RawMessage(sourceRef)
	}
	return e, nil
}

// UpsertOrgActivity folds one projection row. Returns true when the row is
// new (the anchor semantics: a replayed event returns false and folds
// nothing).
func (s *Store) UpsertOrgActivity(ctx context.Context, e OrgActivityEvent) (bool, error) {
	if e.SourceID == "" || e.EventID == "" || e.EventType == "" {
		return false, errors.New("source_id, event_id and event_type are required")
	}
	if e.Refs == nil {
		e.Refs = json.RawMessage("{}")
	}
	if e.SourceRef == nil {
		e.SourceRef = json.RawMessage("{}")
	}
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO org_activity (source_id, event_id, event_type, category, occurred_at, received_at,
		    project_id, run_id, task_id, plan_id, actor_id, org_node_id, org_node_path,
		    summary, visibility_policy, refs, source_ref, schema_version)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		 ON CONFLICT (source_id, event_id) DO NOTHING`,
		e.SourceID, e.EventID, e.EventType, e.Category, e.OccurredAt, e.ReceivedAt,
		e.ProjectID, e.RunID, e.TaskID, e.PlanID, e.ActorID, e.OrgNodeID, e.OrgNodePath,
		e.Summary, e.Visibility, []byte(e.Refs), []byte(e.SourceRef), e.SchemaVersion)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// OrgActivityFilter narrows list_activity (spec §15) and the Activity Board
// queries (§9): node scope, period, event types, run/task/actor/project.
type OrgActivityFilter struct {
	OrgNodeIDs []string // resolved scope (unit/subtree/custom) — empty means all
	EventTypes []string
	Category   string
	RunID      string
	TaskID     string
	ActorID    string
	ProjectID  string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// ListOrgActivity serves the board/feed queries, newest first.
func (s *Store) ListOrgActivity(ctx context.Context, filter OrgActivityFilter) ([]OrgActivityEvent, int, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 50
	}
	args := []any{}
	where := []string{}
	add := func(value any, condition string) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	if len(filter.OrgNodeIDs) > 0 {
		add(textListArg(filter.OrgNodeIDs), "org_node_id = ANY($%d)")
	}
	if len(filter.EventTypes) > 0 {
		add(textListArg(filter.EventTypes), "event_type = ANY($%d)")
	}
	if filter.Category != "" {
		add(filter.Category, "category = $%d")
	}
	if filter.RunID != "" {
		add(filter.RunID, "run_id = $%d")
	}
	if filter.TaskID != "" {
		add(filter.TaskID, "task_id = $%d")
	}
	if filter.ActorID != "" {
		add(filter.ActorID, "actor_id = $%d")
	}
	if filter.ProjectID != "" {
		add(filter.ProjectID, "project_id = $%d")
	}
	if filter.From != nil {
		add(*filter.From, "occurred_at >= $%d")
	}
	if filter.To != nil {
		add(*filter.To, "occurred_at <= $%d")
	}
	condition := ""
	if len(where) > 0 {
		condition = " WHERE " + strings.Join(where, " AND ")
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM org_activity`+condition, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+orgActivityColumns+` FROM org_activity`+condition+
			` ORDER BY occurred_at DESC, source_id, event_id LIMIT `+fmt.Sprint(filter.Limit)+
			` OFFSET `+fmt.Sprint(filter.Offset), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	events := []OrgActivityEvent{}
	for rows.Next() {
		e, err := scanOrgActivityEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		events = append(events, e)
	}
	return events, count, rows.Err()
}

// GetOrgActivityEvent fetches one projection row by its journal identity.
func (s *Store) GetOrgActivityEvent(ctx context.Context, sourceID, eventID string) (OrgActivityEvent, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+orgActivityColumns+` FROM org_activity WHERE source_id = $1 AND event_id = $2`,
		sourceID, eventID)
	e, err := scanOrgActivityEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// UpsertOrgActiveRun maintains the active-run snapshot. Attribution
// (org_node_id) is written only when the row is new or was empty — later
// events of the same run NEVER move it to another node (spec §3: historical
// attribution is fixed at launch).
func (s *Store) UpsertOrgActiveRun(ctx context.Context, r OrgActiveRun) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_active_run (project_id, run_id, source_id, org_node_id, org_node_path,
		    actor_id, agent_id, task_id, title, status, urgent, started_at, last_event_at, finished_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now())
		 ON CONFLICT (project_id, run_id) DO UPDATE SET
		   source_id = COALESCE(NULLIF(EXCLUDED.source_id, ''), org_active_run.source_id),
		   org_node_id = CASE WHEN org_active_run.org_node_id = '' THEN EXCLUDED.org_node_id ELSE org_active_run.org_node_id END,
		   org_node_path = CASE WHEN org_active_run.org_node_path = '' THEN EXCLUDED.org_node_path ELSE org_active_run.org_node_path END,
		   actor_id = COALESCE(NULLIF(EXCLUDED.actor_id, ''), org_active_run.actor_id),
		   agent_id = COALESCE(NULLIF(EXCLUDED.agent_id, ''), org_active_run.agent_id),
		   task_id = COALESCE(NULLIF(EXCLUDED.task_id, ''), org_active_run.task_id),
		   title = COALESCE(NULLIF(EXCLUDED.title, ''), org_active_run.title),
		   status = EXCLUDED.status,
		   urgent = org_active_run.urgent OR EXCLUDED.urgent,
		   started_at = COALESCE(org_active_run.started_at, EXCLUDED.started_at),
		   last_event_at = GREATEST(COALESCE(org_active_run.last_event_at, EXCLUDED.last_event_at), COALESCE(EXCLUDED.last_event_at, to_timestamp(0))),
		   finished_at = EXCLUDED.finished_at,
		   updated_at = now()`,
		r.ProjectID, r.RunID, r.SourceID, r.OrgNodeID, r.OrgNodePath,
		r.ActorID, r.AgentID, r.TaskID, r.Title, r.Status, r.Urgent,
		r.StartedAt, r.LastEventAt, r.FinishedAt)
	return err
}

// OrgActiveRunFilter narrows get_active_runs (spec §15).
type OrgActiveRunFilter struct {
	OrgNodeIDs []string
	Status     string // running|completed|failed|cancelled; '' = all
	TaskID     string
	ActorID    string
	ProjectID  string
	Limit      int
	Offset     int
}

// ListOrgActiveRuns returns the snapshot rows, most recently active first.
func (s *Store) ListOrgActiveRuns(ctx context.Context, filter OrgActiveRunFilter) ([]OrgActiveRun, int, error) {
	if filter.Limit <= 0 || filter.Limit > 1000 {
		filter.Limit = 200
	}
	args := []any{}
	where := []string{}
	add := func(value any, condition string) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	if len(filter.OrgNodeIDs) > 0 {
		add(textListArg(filter.OrgNodeIDs), "org_node_id = ANY($%d)")
	}
	if filter.Status != "" {
		add(filter.Status, "status = $%d")
	}
	if filter.TaskID != "" {
		add(filter.TaskID, "task_id = $%d")
	}
	if filter.ActorID != "" {
		add(filter.ActorID, "actor_id = $%d")
	}
	if filter.ProjectID != "" {
		add(filter.ProjectID, "project_id = $%d")
	}
	condition := ""
	if len(where) > 0 {
		condition = " WHERE " + strings.Join(where, " AND ")
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM org_active_run`+condition, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT project_id, run_id, source_id, org_node_id, org_node_path, actor_id, agent_id, task_id,
		        title, status, urgent, started_at, last_event_at, finished_at
		 FROM org_active_run`+condition+
			` ORDER BY COALESCE(last_event_at, started_at, updated_at) DESC NULLS LAST LIMIT `+fmt.Sprint(filter.Limit)+
			` OFFSET `+fmt.Sprint(filter.Offset), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	runs := []OrgActiveRun{}
	for rows.Next() {
		var r OrgActiveRun
		if err := rows.Scan(&r.ProjectID, &r.RunID, &r.SourceID, &r.OrgNodeID, &r.OrgNodePath,
			&r.ActorID, &r.AgentID, &r.TaskID, &r.Title, &r.Status, &r.Urgent,
			&r.StartedAt, &r.LastEventAt, &r.FinishedAt); err != nil {
			return nil, 0, err
		}
		runs = append(runs, r)
	}
	return runs, count, rows.Err()
}

// OrgActivityWatermark returns the projection sync watermark (zero: never).
func (s *Store) OrgActivityWatermark(ctx context.Context) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx, `SELECT watermark FROM org_activity_sync WHERE id`).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return at, err
}

// AdvanceOrgActivitySync moves the projection watermark forward (never back).
func (s *Store) AdvanceOrgActivitySync(ctx context.Context, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_activity_sync (id, watermark) VALUES (TRUE, $1)
		 ON CONFLICT (id) DO UPDATE SET watermark = GREATEST(org_activity_sync.watermark, EXCLUDED.watermark)`, at)
	return err
}

// PruneOrgActivity enforces the node activity_retention on the projection.
func (s *Store) PruneOrgActivity(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM org_activity WHERE occurred_at < $1`, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PruneOrgActiveRunsFinished drops terminal snapshot rows older than the
// given age — the snapshot stays small while terminal history lives in
// org_activity.
func (s *Store) PruneOrgActiveRunsFinished(ctx context.Context, finishedBefore time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM org_active_run WHERE status <> 'running' AND COALESCE(finished_at, updated_at) < $1`, finishedBefore)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GetOrgActiveRun fetches one snapshot row (attribution reuse + delivery
// checks).
func (s *Store) GetOrgActiveRun(ctx context.Context, projectID, runID string) (OrgActiveRun, error) {
	var r OrgActiveRun
	err := s.pool.QueryRow(ctx,
		`SELECT project_id, run_id, source_id, org_node_id, org_node_path, actor_id, agent_id, task_id,
		        title, status, urgent, started_at, last_event_at, finished_at
		 FROM org_active_run WHERE project_id = $1 AND run_id = $2`, projectID, runID).
		Scan(&r.ProjectID, &r.RunID, &r.SourceID, &r.OrgNodeID, &r.OrgNodePath,
			&r.ActorID, &r.AgentID, &r.TaskID, &r.Title, &r.Status, &r.Urgent,
			&r.StartedAt, &r.LastEventAt, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// SetOrgActiveRunUrgent latches or clears the urgent flag: critical events
// (run failure, waiting-for-human) raise it, their resolution clears it —
// the supervisor's out-of-band processing keys off this flag.
func (s *Store) SetOrgActiveRunUrgent(ctx context.Context, projectID, runID string, urgent bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE org_active_run SET urgent = $3, updated_at = now() WHERE project_id = $1 AND run_id = $2`,
		projectID, runID, urgent)
	return err
}

// SetOrgActiveRunSource records the journal source id a run was observed
// under — the steer worker resolves the Temporal workflow identity from it
// (agent.WorkflowID). COALESCE keeps the first observation: a run's source
// never changes mid-flight.
func (s *Store) SetOrgActiveRunSource(ctx context.Context, projectID, runID, sourceID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE org_active_run SET source_id = $3, updated_at = now()
		 WHERE project_id = $1 AND run_id = $2 AND (source_id = '' OR source_id IS NULL)`,
		projectID, runID, sourceID)
	return err
}

// OrgArtifactContext resolves an artifact's attribution inputs: artifacts
// carry their own org binding and origin run — lifecycle events journal
// without project/run context, so the fold joins the workspace row.
func (s *Store) OrgArtifactContext(ctx context.Context, artifactID string) (orgUnitID, originRun, originUser, sagaID, title string, err error) {
	var unit, run, user, saga *string
	err = s.pool.QueryRow(ctx,
		`SELECT org_unit_id, origin_run, origin_user, saga_id, title FROM workspace_artifact WHERE id = $1`, artifactID).
		Scan(&unit, &run, &user, &saga, &title)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", "", "", ErrNotFound
	}
	if err != nil {
		return
	}
	return derefPtr(unit), derefPtr(run), derefPtr(user), derefPtr(saga), title, nil
}

// RunExecContextByRunID resolves the launch-time authorization snapshot for a
// journal run id. Only root runs carry a workspace_run row (delegation/plan
// children live in the journal only) — ErrNotFound means "resolve via parent
// or actor".
func (s *Store) RunExecContextByRunID(ctx context.Context, runID string) (map[string]any, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT exec_context FROM workspace_run WHERE run_id = $1 LIMIT 1`, runID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ctx2 := decodeExecContext(raw)
	return ctx2, nil
}
