package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Supervisor attention queue (docs/supervisor-model.md §3): supervisor
// verdicts fold into CASES — one row per subject, not one row per re-published
// verdict. The journal is append-only and several families (knowledge
// hygiene above all) republish by design, so event-per-row queues drowned in
// duplicates the moment the journal outgrew one page. The case identity is
// the verdict's event id WITHOUT its nano suffix
// (supervisor-case/<family>/<subject>/<nano>) — exactly how acknowledgements
// already name them.
//
// Lifecycle: open → decided (a human acted or acknowledged). A re-published
// verdict bumps the occurrence counter and refreshes the payload but never
// silently reopens a decided case — «вернулось» is visible through
// last_seen > decided_at. Digest families (the noise budget) land directly
// in the digest lane instead of the attention queue.

// Case lifecycle states (the queue filter and the UI badge key off these).
const (
	SupervisorCaseOpen    = "open"
	SupervisorCaseDecided = "decided"
	SupervisorCaseDigest  = "digest"
)

// supervisorDigestFamilies are the republishing-by-design families: their
// verdicts are a weekly reading, not an interruption.
var supervisorDigestFamilies = map[string]bool{
	"knowledge_hygiene":     true,
	"development_narrative": true,
}

// ErrSupervisorCaseDecided is the API-layer 409: a decided case never takes
// a second verdict.
var ErrSupervisorCaseDecided = errors.New("supervisor case already decided")

// SupervisorCase is one attention item: a supervisor verdict family about one
// subject, folded across re-publishes (docs/supervisor-model.md §3).
type SupervisorCase struct {
	ID             int64           `json:"id"`
	CaseKey        string          `json:"case_key"`
	Family         string          `json:"family"`
	Cause          string          `json:"cause"`
	Subject        string          `json:"subject"`
	Project        string          `json:"project"`
	Summary        string          `json:"summary"`
	Recommendation string          `json:"recommendation"`
	Payload        json.RawMessage `json:"payload"`
	Status         string          `json:"status"`
	Occurrences    int             `json:"occurrences"`
	FirstSeen      time.Time       `json:"first_seen"`
	LastSeen       time.Time       `json:"last_seen"`
	LastEventID    string          `json:"last_event_id"`
	DecidedBy      string          `json:"decided_by,omitempty"`
	DecidedAt      *time.Time      `json:"decided_at,omitempty"`
	Decision       string          `json:"decision,omitempty"`
}

const supervisorCaseColumns = `id, case_key, family, cause, subject, project, summary, recommendation,
payload, status, occurrences, first_seen, last_seen, last_event_id, decided_by, decided_at, decision`

func scanSupervisorCase(scanner interface{ Scan(dest ...any) error }) (SupervisorCase, error) {
	var c SupervisorCase
	var decidedAt *time.Time
	err := scanner.Scan(&c.ID, &c.CaseKey, &c.Family, &c.Cause, &c.Subject, &c.Project, &c.Summary,
		&c.Recommendation, &c.Payload, &c.Status, &c.Occurrences, &c.FirstSeen, &c.LastSeen,
		&c.LastEventID, &c.DecidedBy, &decidedAt, &c.Decision)
	c.DecidedAt = decidedAt
	return c, err
}

// SupervisorCaseKey derives the case identity from a verdict's journal event
// id: supervisor-case/<family>/<subject>/<nano> minus the nano suffix. The
// subject itself may contain slashes — only the LAST segment is the suffix.
func SupervisorCaseKey(eventID string) string {
	if i := strings.LastIndex(eventID, "/"); i > 0 {
		return eventID[:i]
	}
	return eventID
}

// UpsertSupervisorCase folds one supervisor.verdict event into its case row
// (§3 queue rewrite). Idempotent per event id: the case_event anchor decides
// whether this event is new — only then does the occurrence counter move, so
// watermark replays never inflate counts. The first verdict decides the
// lane: an already-acknowledged event (legacy acks) lands as decided, a
// digest family lands in the digest lane, everything else opens.
// supervisorCaseUpsertSQL folds one verdict event into its case row. The
// timestamp value rides THREE DISTINCT parameters ($11 first_seen, $12
// last_seen, $13 decided_at): one parameter through several inference
// contexts (plain column vs CASE…ELSE NULL) once broke the parse with
// 42P08 «inconsistent types deduced», sticking the whole sync pass.
const supervisorCaseUpsertSQL = `INSERT INTO workspace_supervisor_case
	 (case_key, family, cause, subject, project, summary, recommendation, payload, status,
	  occurrences, first_seen, last_seen, last_event_id, decided_by, decided_at, decision)
	 VALUES ($1, $2, $3, $4, $5, $6, $7, $8,
	         CASE WHEN EXISTS (SELECT 1 FROM supervisor_ack WHERE verdict_event_id = $9) THEN 'decided'
	              WHEN $10 THEN 'digest'
	              ELSE 'open' END,
	         1, $11, $12, $9,
	         CASE WHEN EXISTS (SELECT 1 FROM supervisor_ack WHERE verdict_event_id = $9) THEN 'legacy-ack' ELSE '' END,
	         CASE WHEN EXISTS (SELECT 1 FROM supervisor_ack WHERE verdict_event_id = $9) THEN $13 ELSE NULL END,
	         CASE WHEN EXISTS (SELECT 1 FROM supervisor_ack WHERE verdict_event_id = $9) THEN 'acknowledge' ELSE '' END)
	 ON CONFLICT (case_key) DO UPDATE SET
	   occurrences = workspace_supervisor_case.occurrences + 1,
	   last_seen = GREATEST(workspace_supervisor_case.last_seen, EXCLUDED.last_seen),
	   last_event_id = EXCLUDED.last_event_id,
	   cause = COALESCE(NULLIF(EXCLUDED.cause, ''), workspace_supervisor_case.cause),
	   summary = EXCLUDED.summary,
	   recommendation = EXCLUDED.recommendation,
	   payload = EXCLUDED.payload`

func (s *Store) UpsertSupervisorCase(ctx context.Context, eventID string, family, cause, subject, project, summary, recommendation string, payload json.RawMessage, occurredAt time.Time) (bool, error) {
	caseKey := SupervisorCaseKey(eventID)
	if caseKey == "" || eventID == "" {
		return false, errors.New("event id is required")
	}
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var inserted bool
	err := s.pool.QueryRow(ctx,
		`INSERT INTO workspace_supervisor_case_event (event_id, case_key, occurred_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (event_id) DO NOTHING
		 RETURNING true`, eventID, caseKey, occurredAt).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // replay — the event already folded into its case
	}
	if err != nil {
		return false, err
	}
	_, err = s.pool.Exec(ctx, supervisorCaseUpsertSQL,
		caseKey, family, cause, subject, project, summary, recommendation, payload,
		eventID, supervisorDigestFamilies[family], occurredAt, occurredAt, occurredAt)
	return inserted, err
}

// SupervisorQueueFilter narrows the queue listing (§7): family, project and
// the resolved visibility. An empty status lists open + digest rows — the
// digest lane stays visible on demand but never interrupts.
type SupervisorQueueFilter struct {
	Family       string
	Project      string
	Status       string
	ShowResolved bool
	Limit        int
	Offset       int
}

// SupervisorQueueFacets are the queue's navigational truth: family and
// project histograms plus how long each family has been silent («семейство
// молчит с …» — a generator that stopped publishing is itself a signal).
type SupervisorQueueFacets struct {
	Families       map[string]int       `json:"families"`
	Projects       map[string]int       `json:"projects"`
	FamilyLastSeen map[string]time.Time `json:"family_last_seen"`
}

// SupervisorQueue is the queue page the endpoint serves.
type SupervisorQueue struct {
	Cases   []SupervisorCase      `json:"cases"`
	Count   int                   `json:"count"`
	Pending int                   `json:"pending"`
	Facets  SupervisorQueueFacets `json:"facets"`
}

// ListSupervisorQueue serves the attention queue server-side (§7): stable
// pagination over cases — unresolved first, freshest first inside each lane.
// Pending is the badge truth: open cases, computed with the same filters so
// the badge and the list never disagree.
func (s *Store) ListSupervisorQueue(ctx context.Context, filter SupervisorQueueFilter) (SupervisorQueue, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	args := []any{}
	where := []string{}
	if filter.Family != "" {
		args = append(args, filter.Family)
		where = append(where, fmt.Sprintf("family = $%d", len(args)))
	}
	if filter.Project != "" {
		args = append(args, filter.Project)
		where = append(where, fmt.Sprintf("project = $%d", len(args)))
	}
	switch {
	case filter.Status != "":
		args = append(args, filter.Status)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	case filter.ShowResolved:
		// everything
	default:
		where = append(where, "status <> 'decided'")
	}
	condition := ""
	if len(where) > 0 {
		condition = " WHERE " + strings.Join(where, " AND ")
	}

	queue := SupervisorQueue{
		Cases: []SupervisorCase{},
		Facets: SupervisorQueueFacets{
			Families: map[string]int{}, Projects: map[string]int{}, FamilyLastSeen: map[string]time.Time{},
		},
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE status = 'open') FROM workspace_supervisor_case`+condition,
		args...).Scan(&queue.Count, &queue.Pending); err != nil {
		return queue, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+supervisorCaseColumns+` FROM workspace_supervisor_case`+condition+
			` ORDER BY (status = 'open') DESC, last_seen DESC LIMIT `+fmt.Sprint(filter.Limit)+
			` OFFSET `+fmt.Sprint(filter.Offset), args...)
	if err != nil {
		return queue, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanSupervisorCase(rows)
		if err != nil {
			return queue, err
		}
		queue.Cases = append(queue.Cases, c)
		queue.Facets.Families[c.Family]++
		if c.Project != "" {
			queue.Facets.Projects[c.Project]++
		}
	}
	if err := rows.Err(); err != nil {
		return queue, err
	}
	// Facets span the whole queue (not the page) and answer «is this
	// generator still alive?».
	facetRows, err := s.pool.Query(ctx,
		`SELECT family, COUNT(*), MAX(last_seen) FROM workspace_supervisor_case GROUP BY family`)
	if err != nil {
		return queue, err
	}
	defer facetRows.Close()
	for facetRows.Next() {
		var family string
		var count int
		var lastSeen time.Time
		if err := facetRows.Scan(&family, &count, &lastSeen); err != nil {
			return queue, err
		}
		queue.Facets.Families[family] = count
		queue.Facets.FamilyLastSeen[family] = lastSeen
	}
	return queue, facetRows.Err()
}

// DecideSupervisorCase closes an open case with a human decision (an action
// like «invalidate the losing knowledge» or a plain acknowledge). Decided
// cases never take a second verdict — the API layer maps that to 409.
func (s *Store) DecideSupervisorCase(ctx context.Context, caseKey, decision, reviewer string) (SupervisorCase, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE workspace_supervisor_case
		 SET status = 'decided', decided_by = $2, decided_at = now(), decision = $3
		 WHERE case_key = $1 AND status <> 'decided'
		 RETURNING `+supervisorCaseColumns, caseKey, reviewer, decision)
	c, err := scanSupervisorCase(row)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if e := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM workspace_supervisor_case WHERE case_key = $1)`, caseKey).Scan(&exists); e == nil && exists {
			return SupervisorCase{}, ErrSupervisorCaseDecided
		}
		return SupervisorCase{}, ErrNotFound
	}
	return c, err
}

// SupervisorSyncWatermark returns when the verdict→case sync last advanced
// (zero time: never — the first pass folds the retention window).
func (s *Store) SupervisorSyncWatermark(ctx context.Context) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx, `SELECT watermark FROM workspace_supervisor_sync WHERE id`).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return at, err
}

// AdvanceSupervisorSync moves the verdict→case sync watermark forward.
func (s *Store) AdvanceSupervisorSync(ctx context.Context, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_supervisor_sync (id, watermark) VALUES (TRUE, $1)
		 ON CONFLICT (id) DO UPDATE SET watermark = GREATEST(workspace_supervisor_sync.watermark, EXCLUDED.watermark)`, at)
	return err
}

// PruneSupervisorCaseEvents keeps the idempotency anchors bounded: an event
// older than retention can no longer arrive (the journal reaps in the same
// window), so its anchor is dead weight.
func (s *Store) PruneSupervisorCaseEvents(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_supervisor_case_event WHERE occurred_at < $1`, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
