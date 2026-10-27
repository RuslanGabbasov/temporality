// Package postgres implements the observation journal store on PostgreSQL.
// Extracted verbatim from the frozen FRP substrate (era 1) when the journal
// became its own service — see docs/product-architecture.md.
package postgres

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/temporality-project/temporality/observation"
)

//go:embed migrations/observation_events.up.sql
var observationEventsSchema string

//go:embed migrations/observation_events_indexes.up.sql
var observationEventsIndexes string

//go:embed migrations/observation_events_retention.up.sql
var observationEventsRetention string

// journalMigrations lists the boot-time migrations in application order. The
// retention file must come after the baseline: it CREATE OR REPLACEs the
// trigger function the baseline creates. All are idempotent and re-run on
// every boot (plain multi-statement Exec — PostgreSQL wraps each file in an
// implicit transaction).
var journalMigrations = []string{
	observationEventsSchema,
	observationEventsIndexes,
	observationEventsRetention,
}

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate applies the journal schema. Idempotent: CREATE ... IF NOT EXISTS
// everywhere, so it is safe on databases that already ran the historical
// FRP migration chain (which created the same table as 000018).
func (s *Store) Migrate(ctx context.Context) error {
	for i, migration := range journalMigrations {
		if _, err := s.pool.Exec(ctx, migration); err != nil {
			return fmt.Errorf("journal migration %d: %w", i, err)
		}
	}
	return nil
}

// retentionSweepGUC is the session-scoped custom GUC that marks one connection
// as the retention sweeper: the append-only trigger
// (migrations/observation_events_retention.up.sql) permits DELETE only while
// it reads 'on' on that session. Custom GUCs are settable by any role.
const retentionSweepGUC = "temporality.retention_sweep"

const (
	retentionSweepSetSQL   = `SET ` + retentionSweepGUC + ` = 'on'`
	retentionSweepResetSQL = `RESET ` + retentionSweepGUC
)

// retentionSweepSQL deletes one bounded batch of expired events. PostgreSQL
// has no DELETE ... LIMIT, so the batch is picked by ctid first; the outer
// delete then hits exactly those rows. now() - $1::interval keeps the cutoff
// on the database clock, so sweepers on skewed app servers cannot over-retain.
const retentionSweepSQL = `DELETE FROM observation_events
	WHERE ctid IN (
		SELECT ctid FROM observation_events
		WHERE occurred_at <= now() - $1::interval
		LIMIT $2
	)`

const retentionSweepDefaultBatchSize = 5000

// RetentionSweep deletes observation events older than olderThan in
// batchSize chunks and returns the total rows removed. batchSize <= 0 falls
// back to retentionSweepDefaultBatchSize. While it runs, the GUC
// temporality.retention_sweep lifts the append-only trigger for this one
// connection — DELETE only, UPDATE stays rejected. The GUC is RESET before
// the connection returns to the pool: pgxpool hands the same connection to
// the next borrower with its session state intact, so a leaked 'on' would
// poke a permanent hole in the append-only guarantee for ordinary clients.
func (s *Store) RetentionSweep(ctx context.Context, olderThan time.Duration, batchSize int) (int64, error) {
	if olderThan <= 0 {
		return 0, fmt.Errorf("retention sweep: olderThan must be positive, got %s", olderThan)
	}
	if batchSize <= 0 {
		batchSize = retentionSweepDefaultBatchSize
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	// The RESET deliberately runs on a context that survives caller
	// cancellation: a cancelled sweep must still clear the session before the
	// connection re-enters the pool.
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), retentionSweepResetSQL)
		conn.Release()
	}()
	if _, err := conn.Exec(ctx, retentionSweepSetSQL); err != nil {
		return 0, fmt.Errorf("enable %s: %w", retentionSweepGUC, err)
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		tag, err := conn.Exec(ctx, retentionSweepSQL, olderThan, batchSize)
		if err != nil {
			return total, fmt.Errorf("retention sweep batch: %w", err)
		}
		deleted := tag.RowsAffected()
		total += deleted
		if deleted == 0 {
			return total, nil
		}
	}
}

// appendObservationSQL is the single-row insert shared by the one-event and
// batch append paths, so the two cannot drift apart.
const appendObservationSQL = `INSERT INTO observation_events
	(source_id,event_id,schema,occurred_at,received_at,integration,source_version,project_id,run_id,task_id,actor_id,actor_type,parent_event_id,type,data,evidence)
	VALUES ($1,$2,$3,$4,COALESCE($5::timestamptz,clock_timestamp()),$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
	ON CONFLICT (source_id,event_id) DO NOTHING`

// encodeObservationEvent marshals the JSONB columns and normalizes the
// received_at bind (NULL = let the database clock stamp it).
func encodeObservationEvent(event observation.Event) (data, evidence []byte, receivedAt any, err error) {
	data, err = json.Marshal(event.Data)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode event data: %w", err)
	}
	evidence, err = json.Marshal(event.Evidence)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode event evidence: %w", err)
	}
	if !event.ReceivedAt.IsZero() {
		receivedAt = event.ReceivedAt
	}
	return data, evidence, receivedAt, nil
}

const notifyEventsChangedSQL = `SELECT pg_notify('observation_events_changed', $1)`

// notifyEventsChanged wakes LISTEN/NOTIFY subscribers (the SSE wake path)
// after a successful append. Best effort by design: a dropped notification
// costs one 1.5s polling fallback, never an ingest failure.
func (s *Store) notifyEventsChanged(ctx context.Context, project string) {
	_, _ = s.pool.Exec(ctx, notifyEventsChangedSQL, project)
}

func (s *Store) AppendObservation(ctx context.Context, event observation.Event) (bool, error) {
	if err := event.Validate(); err != nil {
		return false, err
	}
	data, evidence, receivedAt, err := encodeObservationEvent(event)
	if err != nil {
		return false, err
	}
	result, err := s.pool.Exec(ctx, appendObservationSQL,
		event.Source.ID, event.EventID, event.Schema, event.OccurredAt, receivedAt,
		event.Source.Integration, event.Source.Version, event.Context.Project, event.Context.Run,
		event.Context.Task, event.Context.Actor.ID, event.Context.Actor.Type,
		event.Context.ParentEventID, event.Type, data, evidence)
	if err != nil {
		return false, err
	}
	if result.RowsAffected() == 1 {
		s.notifyEventsChanged(ctx, event.Context.Project)
		return true, nil
	}
	previous, err := s.GetObservation(ctx, event.Source.ID, event.EventID)
	if err != nil {
		return false, err
	}
	event.ReceivedAt = previous.ReceivedAt
	if sameObservation(previous, event) {
		return false, nil
	}
	return false, observation.ErrConflict
}

// distinctProjects returns the unique, order-stable project ids of a batch:
// one pg_notify per project wakes exactly the watchers that can see them.
func distinctProjects(events []observation.Event) []string {
	seen := make(map[string]struct{}, len(events))
	projects := make([]string, 0, 1)
	for _, event := range events {
		project := event.Context.Project
		if _, ok := seen[project]; ok {
			continue
		}
		seen[project] = struct{}{}
		projects = append(projects, project)
	}
	return projects
}

// AppendObservationBatch appends a group of events in one round trip (pgx
// Batch, same framing as UpsertKnowledgeEmbeddings). Hint activation telemetry
// (hint.query + one hint.offered per offer) is the caller that pays per-event
// inserts today. Every event is validated and encoded up front — a rejected
// event aborts the batch before anything is written. SQL failures do not stop
// the drain: every queued statement still executes, the first error is
// returned, so a partial batch is visible to the caller.
func (s *Store) AppendObservationBatch(ctx context.Context, events []observation.Event) error {
	batch := &pgx.Batch{}
	for _, event := range events {
		if err := event.Validate(); err != nil {
			return err
		}
		data, evidence, receivedAt, err := encodeObservationEvent(event)
		if err != nil {
			return err
		}
		batch.Queue(appendObservationSQL,
			event.Source.ID, event.EventID, event.Schema, event.OccurredAt, receivedAt,
			event.Source.Integration, event.Source.Version, event.Context.Project, event.Context.Run,
			event.Context.Task, event.Context.Actor.ID, event.Context.Actor.Type,
			event.Context.ParentEventID, event.Type, data, evidence)
	}
	if batch.Len() == 0 {
		return nil
	}
	// Wake subscribers after the inserts: one notification per project, even
	// when the batch itself failed partially — a spurious wake is a harmless
	// extra poll, and its own error must never mask the insert result.
	notifyProjects := distinctProjects(events)
	for _, project := range notifyProjects {
		batch.Queue(notifyEventsChangedSQL, project)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	var firstErr error
	for range events {
		if _, err := br.Exec(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for range notifyProjects {
		_, _ = br.Exec()
	}
	return firstErr
}

func sameObservation(a, b observation.Event) bool {
	a.ReceivedAt = b.ReceivedAt
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && bytes.Equal(left, right)
}

func (s *Store) GetObservation(ctx context.Context, sourceID, eventID string) (observation.Event, error) {
	event, err := scanObservation(s.pool.QueryRow(ctx, `SELECT source_id,event_id,schema,occurred_at,received_at,integration,source_version,project_id,run_id,task_id,actor_id,actor_type,parent_event_id,type,data,evidence
		FROM observation_events WHERE source_id=$1 AND event_id=$2`, sourceID, eventID))
	if errors.Is(err, pgx.ErrNoRows) {
		return observation.Event{}, observation.ErrNotFound
	}
	return event, err
}

func (s *Store) ListObservations(ctx context.Context, filter observation.Filter) ([]observation.Event, error) {
	page, err := s.ListObservationPage(ctx, filter, "")
	return page.Events, err
}

func (s *Store) ListObservationPage(ctx context.Context, filter observation.Filter, encodedCursor string) (observation.Page, error) {
	limit, err := observation.NormalizeLimit(filter.Limit)
	if err != nil {
		return observation.Page{}, err
	}
	cursor, err := observation.DecodeCursor(encodedCursor)
	if err != nil {
		return observation.Page{}, err
	}
	var cursorOccurred, cursorReceived, cursorSource, cursorEvent any
	if cursor != nil {
		cursorOccurred, cursorReceived = cursor.OccurredAt, cursor.ReceivedAt
		cursorSource, cursorEvent = cursor.SourceID, cursor.EventID
	}
	// Desc mode walks the window newest-first (the experience timeline loads
	// «от свежих к старым» so the newest activity paints first). Cursor
	// comparisons flip with the order; the cursor itself is the same tuple.
	desc := filter.Order == "desc"
	order := "ASC"
	cursorCmp := ">"
	if desc {
		order = "DESC"
		cursorCmp = "<"
	}
	rows, err := s.pool.Query(ctx, `SELECT source_id,event_id,schema,occurred_at,received_at,integration,source_version,project_id,run_id,task_id,actor_id,actor_type,parent_event_id,type,data,evidence
		FROM observation_events
		WHERE ($1='' OR project_id=$1) AND ($2='' OR source_id=$2) AND ($3='' OR event_id=$3)
		AND ($4='' OR run_id=$4) AND ($5='' OR task_id=$5) AND ($6='' OR actor_id=$6) AND ($7='' OR type=$7)
		AND ($8::timestamptz IS NULL OR occurred_at >= $8) AND ($9::timestamptz IS NULL OR occurred_at <= $9)
		AND ($10::timestamptz IS NULL OR received_at <= $10)
		AND ($11::timestamptz IS NULL OR (occurred_at,received_at,source_id,event_id) `+cursorCmp+` ($11,$12,$13,$14))
		AND ($16='' OR data->>'scope_kind'=$16)
		AND (cardinality($17::text[]) = 0 OR data->>'scope_id' = ANY($17))
		AND (cardinality($18::text[]) = 0 OR data->>'knowledge_id' = ANY($18))
		AND (cardinality($19::text[]) = 0 OR project_id = ANY($19))
		AND (cardinality($20::text[]) = 0 OR type = ANY($20))
		ORDER BY occurred_at `+order+`,received_at `+order+`,source_id `+order+`,event_id `+order+` LIMIT $15`,
		filter.Project, filter.SourceID, filter.EventID, filter.Run, filter.Task, filter.Actor, filter.Type,
		filter.Since, filter.Until, filter.KnownAt, cursorOccurred, cursorReceived, cursorSource, cursorEvent, limit+1,
		filter.ScopeKind, textList(filter.ScopeIDs), textList(filter.KnowledgeIDs), textList(filter.Projects), textList(filter.Types))
	if err != nil {
		return observation.Page{}, err
	}
	defer rows.Close()
	events := make([]observation.Event, 0, limit+1)
	for rows.Next() {
		event, scanErr := scanObservation(rows)
		if scanErr != nil {
			return observation.Page{}, scanErr
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return observation.Page{}, err
	}
	page := observation.Page{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		page.NextCursor = observation.EncodeCursor(page.Events[len(page.Events)-1])
	}
	return page, nil
}

type observationScanner interface{ Scan(...any) error }

// HealthEvents loads the dashboard aggregation window in a single pass
// (docs/system-dashboard.md §3.1): the same columns as ListObservationPage but
// no page limit — the aggregate must see the whole window, and paging 500-row
// chunks per dashboard poll would dwarf the query itself.
func (s *Store) HealthEvents(ctx context.Context, since time.Time, projects []string) ([]observation.Event, error) {
	types := append([]string(nil), observation.HealthEventTypes...)
	rows, err := s.pool.Query(ctx, `SELECT source_id,event_id,schema,occurred_at,received_at,integration,source_version,project_id,run_id,task_id,actor_id,actor_type,parent_event_id,type,data,evidence
		FROM observation_events
		WHERE occurred_at >= $1 AND type = ANY($2) AND (cardinality($3::text[]) = 0 OR project_id = ANY($3))`,
		since, textList(types), textList(projects))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]observation.Event, 0)
	for rows.Next() {
		event, scanErr := scanObservation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// textList normalizes a filter list so pgx always binds an array literal:
// cardinality of a bound empty array is 0, which makes the ANY/ID filters
// pass-through, while a SQL NULL would leave the condition unevaluated.
func textList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return values
}

func scanObservation(row observationScanner) (observation.Event, error) {
	var event observation.Event
	var data, evidence []byte
	err := row.Scan(&event.Source.ID, &event.EventID, &event.Schema, &event.OccurredAt,
		&event.ReceivedAt, &event.Source.Integration, &event.Source.Version,
		&event.Context.Project, &event.Context.Run, &event.Context.Task,
		&event.Context.Actor.ID, &event.Context.Actor.Type, &event.Context.ParentEventID,
		&event.Type, &data, &evidence)
	if err != nil {
		return event, err
	}
	event.Data = decodeJSONObject(event.Source.ID, event.EventID, data)
	event.Evidence = decodeEvidence(event.Source.ID, event.EventID, evidence)
	return event, nil
}

// decodeJSONObject parses the jsonb data column, which must hold a JSON
// object. The write path only ever produces objects, but jsonb itself allows
// scalars and arrays, and one out-of-band row (direct DB write, ancient
// writer) holding e.g. an array used to fail every listing that paged
// through it — /v1/agent/operations answered HTTP 500 forever. Degrade the
// row to a marker instead of failing the page; the journal stays append-only,
// so the row itself cannot be repaired in place.
func decodeJSONObject(sourceID, eventID string, data []byte) map[string]any {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		slog.Warn("observation event data is not a JSON object; degrading to marker", "source_id", sourceID, "event_id", eventID, "error", err)
		const keep = 512
		snippet := string(trimmed)
		if len(snippet) > keep {
			snippet = snippet[:keep] + "…"
		}
		return map[string]any{"_unparsed_data": snippet}
	}
	return parsed
}

// decodeEvidence parses the jsonb evidence column the same tolerant way: a
// malformed row loses its evidence but never fails the listing.
func decodeEvidence(sourceID, eventID string, evidence []byte) []observation.Evidence {
	trimmed := bytes.TrimSpace(evidence)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var parsed []observation.Evidence
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		slog.Warn("observation event evidence is not a JSON array; dropping", "source_id", sourceID, "event_id", eventID, "error", err)
		return nil
	}
	return parsed
}

// KnowledgeEmbedding is one stored semantic vector of a knowledge item.
type KnowledgeEmbedding struct {
	KnowledgeID string
	Model       string
	Dim         int
	Vector      []float32
}

// UpsertKnowledgeEmbeddings replaces vectors in one batch. All vectors in a
// batch must share the model and dimension (validated by the caller).
func (s *Store) UpsertKnowledgeEmbeddings(ctx context.Context, model string, items map[string][]float32) error {
	batch := &pgx.Batch{}
	for id, vector := range items {
		encoded, err := json.Marshal(vector)
		if err != nil {
			return fmt.Errorf("encode embedding for %q: %w", id, err)
		}
		batch.Queue(`INSERT INTO observation_knowledge_embedding (knowledge_id, model, dim, embedding, updated_at)
			VALUES ($1, $2, $3, $4::jsonb, clock_timestamp())
			ON CONFLICT (knowledge_id, model) DO UPDATE SET dim = EXCLUDED.dim, embedding = EXCLUDED.embedding, updated_at = clock_timestamp()`,
			id, model, len(vector), string(encoded))
	}
	if batch.Len() == 0 {
		return nil
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range items {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// ListKnowledgeEmbeddings returns vectors stored for the model, keyed by
// knowledge id. When knowledgeIDs is non-empty the listing is filtered to
// those ids (AND knowledge_id = ANY($N)) — the hints path passes its
// project's projected visible knowledge, so one hints call no longer loads
// every project's vectors. The id list is chunked in
// knowledgeEmbeddingIDChunk-sized ANY(...) arrays: chunking keeps the query
// plan and array bind small for very large corpora while preserving exact
// semantics (the documented alternative — dropping the filter above the cap —
// would reintroduce the cross-project load this filter exists to prevent).
// An empty model = the installation's single embedding space: accept any
// stored model so UI consumers (related-knowledge lookups) work without
// knowing the operator's embedding choice.
const knowledgeEmbeddingIDChunk = 5000

// knowledgeEmbeddingSelect builds the WHERE clause and arguments for one
// chunk of the vector listing. Pure so the SQL/argument framing is testable
// without a database.
func knowledgeEmbeddingSelect(model string, knowledgeIDs []string) (string, []any) {
	const base = `SELECT knowledge_id, dim, embedding FROM observation_knowledge_embedding WHERE `
	hasModel := strings.TrimSpace(model) != ""
	switch {
	case hasModel && len(knowledgeIDs) > 0:
		return base + `model = $1 AND knowledge_id = ANY($2)`, []any{model, knowledgeIDs}
	case hasModel:
		return base + `model = $1`, []any{model}
	case len(knowledgeIDs) > 0:
		return base + `model <> '' AND knowledge_id = ANY($1)`, []any{knowledgeIDs}
	default:
		return base + `model <> ''`, nil
	}
}

func (s *Store) ListKnowledgeEmbeddings(ctx context.Context, model string, knowledgeIDs []string) (map[string][]float32, error) {
	out := map[string][]float32{}
	chunks := [][]string{nil} // empty filter = one unfiltered query (legacy shape)
	if len(knowledgeIDs) > 0 {
		chunks = knowledgeEmbeddingChunks(knowledgeIDs)
	}
	for _, chunk := range chunks {
		query, args := knowledgeEmbeddingSelect(model, chunk)
		rows, err := s.pool.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		if err := scanKnowledgeEmbeddings(rows, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// knowledgeEmbeddingChunks splits an id filter into ANY(...) array binds of at
// most knowledgeEmbeddingIDChunk ids, so the cap degrades to chunked queries
// instead of dropping the filter. Pure; tested without a database.
func knowledgeEmbeddingChunks(knowledgeIDs []string) [][]string {
	chunks := make([][]string, 0, (len(knowledgeIDs)+knowledgeEmbeddingIDChunk-1)/knowledgeEmbeddingIDChunk)
	for start := 0; start < len(knowledgeIDs); start += knowledgeEmbeddingIDChunk {
		end := min(start+knowledgeEmbeddingIDChunk, len(knowledgeIDs))
		chunks = append(chunks, knowledgeIDs[start:end])
	}
	return chunks
}

func scanKnowledgeEmbeddings(rows pgx.Rows, out map[string][]float32) error {
	defer rows.Close()
	for rows.Next() {
		var id string
		var dim int
		var raw []byte
		if err := rows.Scan(&id, &dim, &raw); err != nil {
			return err
		}
		vector := make([]float32, 0, dim)
		if err := json.Unmarshal(raw, &vector); err != nil {
			continue // corrupt row: skip rather than break matching
		}
		out[id] = vector
	}
	return rows.Err()
}

// KnowledgeProjects enumerates the projects that carry knowledge events —
// the semantic map walks them to join vectors with the projections.
func (s *Store) ListKnowledgeProjects(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT project_id FROM observation_events
		 WHERE type LIKE 'knowledge.%' AND project_id <> ''
		 ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []string{}
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

// KnowledgeEmbeddingGap is the observation.KnowledgeEmbeddingGap row: id
// plus proposition text.
// ListKnowledgeEmbeddingGaps returns knowledge items without a vector for the
// model, oldest first, capped. The proposition text rides along so the caller
// (the kernel, which owns the provider keys) can embed it directly.
func (s *Store) ListKnowledgeEmbeddingGaps(ctx context.Context, model string, limit int) ([]observation.KnowledgeEmbeddingGap, error) {
	if limit <= 0 {
		limit = 256
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (e.data->>'knowledge_id')
			e.data->>'knowledge_id' AS knowledge_id,
			e.data->>'proposition'  AS text
		FROM observation_events e
		WHERE e.type IN ('knowledge.proposed', 'knowledge.corrected')
		  AND COALESCE(e.data->>'knowledge_id', '') <> ''
		  AND COALESCE(e.data->>'proposition', '') <> ''
		  AND NOT EXISTS (
			SELECT 1 FROM observation_knowledge_embedding emb
			WHERE emb.knowledge_id = e.data->>'knowledge_id' AND emb.model = $1)
		ORDER BY e.data->>'knowledge_id', e.occurred_at DESC
		LIMIT $2`, model, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gaps := []observation.KnowledgeEmbeddingGap{}
	for rows.Next() {
		var gap observation.KnowledgeEmbeddingGap
		if err := rows.Scan(&gap.KnowledgeID, &gap.Text); err != nil {
			return nil, err
		}
		gaps = append(gaps, gap)
	}
	return gaps, rows.Err()
}
