package memory

import (
	"context"
	_ "embed"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/0001_init.up.sql
var migrationSQL string

// ApplyMigrations creates the AML schema in an empty database.
func ApplyMigrations(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, migrationSQL)
	return err
}

// Store persists assets, feedback events and the raw call log. The in-memory
// implementation backs unit tests; postgres backs the benchmark.
type Store interface {
	UpsertAsset(ctx context.Context, asset *Asset, created bool) error
	ListAssets(ctx context.Context) ([]*Asset, error)
	AppendEvent(ctx context.Context, event Event) error
	AppendEvents(ctx context.Context, events []Event) error
	InsertCall(ctx context.Context, call RecordedCall) error
	Close()
}

// Event is one feedback-loop record; the payload carries why_selected and
// other forensic detail.
type Event struct {
	AssetID   string
	SessionID string
	TaskID    string
	Type      string
	Payload   map[string]any
	At        time.Time
}

// RecordedCall is one observed tool call with its outcome.
type RecordedCall struct {
	SessionID   string
	TaskID      string
	Step        int
	Seq         int
	Tool        string
	Service     string
	Resource    string
	Auth        string
	Environment string
	Params      map[string]any
	Status      int
	OK          bool
	Cause       string
	Summary     string
	At          time.Time
}

// ---------------------------------------------------------------- in-memory

type MemStore struct {
	mu     sync.Mutex
	assets map[string]*Asset
	events []Event
	calls  []RecordedCall
}

// NewMemStore builds an empty in-memory store.
func NewMemStore() *MemStore { return &MemStore{assets: map[string]*Asset{}} }

// UpsertAsset stores a clone of the asset.
func (s *MemStore) UpsertAsset(_ context.Context, asset *Asset, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assets[asset.DedupKey] = asset.Clone()
	return nil
}

// ListAssets returns all assets.
func (s *MemStore) ListAssets(_ context.Context) ([]*Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Asset, 0, len(s.assets))
	for _, a := range s.assets {
		out = append(out, a.Clone())
	}
	return out, nil
}

// AppendEvent records one event.
func (s *MemStore) AppendEvent(_ context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.At.IsZero() {
		event.At = time.Now()
	}
	s.events = append(s.events, event)
	return nil
}

// AppendEvents records many events.
func (s *MemStore) AppendEvents(ctx context.Context, events []Event) error {
	for _, e := range events {
		if err := s.AppendEvent(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// InsertCall records one observed call.
func (s *MemStore) InsertCall(_ context.Context, call RecordedCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
	return nil
}

// Close is a no-op.
func (s *MemStore) Close() {}

// Events exposes recorded events (tests only).
func (s *MemStore) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

// Calls exposes recorded calls (tests only).
func (s *MemStore) Calls() []RecordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RecordedCall(nil), s.calls...)
}

// ----------------------------------------------------------------- postgres

// PgStore is the postgres-backed store.
type PgStore struct {
	pool *pgxpool.Pool
}

// OpenPg connects the store to DATABASE_URL-style DSN.
func OpenPg(ctx context.Context, dsn string) (*PgStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &PgStore{pool: pool}, nil
}

// Close releases the pool.
func (s *PgStore) Close() { s.pool.Close() }

// Pool exposes the underlying pool (driver bootstrap only).
func (s *PgStore) Pool() *pgxpool.Pool { return s.pool }

// UpsertAsset inserts or updates the asset by dedup key.
func (s *PgStore) UpsertAsset(ctx context.Context, a *Asset, created bool) error {
	sessions := a.SourceSessions
	if sessions == nil {
		sessions = []string{}
	}
	evidence := a.Evidence
	if evidence == nil {
		evidence = []Evidence{}
	}
	_, err := s.pool.Exec(ctx, `
		insert into aml_assets (id, dedup_key, service, problem, kind, proposition, recommendation,
			evidence, confidence, status, created_at, last_confirmed_at, last_contradicted_at,
			confirmation_count, contradiction_count, consecutive_contradictions, environment, version_context, source_sessions, last_confirmed_session)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		on conflict (dedup_key) do update set
			proposition = excluded.proposition,
			recommendation = excluded.recommendation,
			evidence = excluded.evidence,
			confidence = excluded.confidence,
			status = excluded.status,
			last_confirmed_at = excluded.last_confirmed_at,
			last_contradicted_at = excluded.last_contradicted_at,
			confirmation_count = excluded.confirmation_count,
			contradiction_count = excluded.contradiction_count,
			consecutive_contradictions = excluded.consecutive_contradictions,
			environment = excluded.environment,
			version_context = excluded.version_context,
			source_sessions = excluded.source_sessions,
			last_confirmed_session = excluded.last_confirmed_session`,
		a.ID, a.DedupKey, a.Service, a.Problem, a.Kind, a.Proposition, a.Recommendation,
		evidence, a.Confidence, a.Status, a.CreatedAt, a.LastConfirmedAt, a.LastContradictedAt,
		a.ConfirmationCount, a.ContradictionCount, a.ConsecutiveContradictions, a.Environment, a.VersionContext, sessions, a.LastConfirmedSession)
	return err
}

// ListAssets loads all non-archived-history assets.
func (s *PgStore) ListAssets(ctx context.Context) ([]*Asset, error) {
	rows, err := s.pool.Query(ctx, `
		select id, dedup_key, service, problem, kind, proposition, recommendation, evidence,
			confidence, status, created_at, last_confirmed_at, last_contradicted_at,
			confirmation_count, contradiction_count, consecutive_contradictions, environment, version_context, source_sessions, last_confirmed_session
		from aml_assets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Asset
	for rows.Next() {
		a := &Asset{}
		var lastConfirmed, lastContradicted *time.Time
		if err := rows.Scan(&a.ID, &a.DedupKey, &a.Service, &a.Problem, &a.Kind, &a.Proposition,
			&a.Recommendation, &a.Evidence, &a.Confidence, &a.Status, &a.CreatedAt,
			&lastConfirmed, &lastContradicted,
			&a.ConfirmationCount, &a.ContradictionCount, &a.ConsecutiveContradictions,
			&a.Environment, &a.VersionContext, &a.SourceSessions, &a.LastConfirmedSession); err != nil {
			return nil, err
		}
		if lastConfirmed != nil {
			a.LastConfirmedAt = *lastConfirmed
		}
		if lastContradicted != nil {
			a.LastContradictedAt = *lastContradicted
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AppendEvent inserts one feedback event.
func (s *PgStore) AppendEvent(ctx context.Context, e Event) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	_, err := s.pool.Exec(ctx, `
		insert into aml_events (occurred_at, session_id, task_id, asset_id, event_type, payload)
		values ($1,$2,$3,nullif($4,'')::uuid,$5,$6)`,
		e.At, e.SessionID, e.TaskID, e.AssetID, e.Type, e.Payload)
	return err
}

// AppendEvents inserts many feedback events in one transaction.
func (s *PgStore) AppendEvents(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	return s.pool.AcquireFunc(ctx, func(conn *pgxpool.Conn) error {
		batch := &pgx.Batch{}
		for _, e := range events {
			if e.At.IsZero() {
				e.At = time.Now()
			}
			batch.Queue(`
				insert into aml_events (occurred_at, session_id, task_id, asset_id, event_type, payload)
				values ($1,$2,$3,nullif($4,'')::uuid,$5,$6)`,
				e.At, e.SessionID, e.TaskID, e.AssetID, e.Type, e.Payload)
		}
		return conn.SendBatch(ctx, batch).Close()
	})
}

// InsertCall records one observed tool call.
func (s *PgStore) InsertCall(ctx context.Context, c RecordedCall) error {
	params := c.Params
	if params == nil {
		params = map[string]any{}
	}
	_, err := s.pool.Exec(ctx, `
		insert into aml_tool_calls (session_id, task_id, step, seq, tool, service, resource, auth, environment, params, status, ok, cause, summary)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		c.SessionID, c.TaskID, c.Step, c.Seq, c.Tool, c.Service, c.Resource, c.Auth, c.Environment, params, c.Status, c.OK, c.Cause, c.Summary)
	return err
}

// DropDatabase drops a database (benchmark isolation).
func DropDatabase(ctx context.Context, adminDSN, name string) error {
	cfg, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		return err
	}
	cfg.Database = "postgres"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fmt.Sprintf("select pg_terminate_backend(pid) from pg_stat_activity where datname = '%s'", name)); err != nil {
		return err
	}
	_, err = conn.Exec(ctx, fmt.Sprintf("drop database if exists %s", name))
	return err
}

// CreateDatabase creates an empty database (benchmark isolation).
func CreateDatabase(ctx context.Context, adminDSN, name string) error {
	cfg, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		return err
	}
	cfg.Database = "postgres"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, "create database "+name)
	return err
}

// DatabaseURL rewrites the database name inside a DSN.
func DatabaseURL(baseDSN, name string) string {
	for _, sep := range []string{"postgres://", "postgresql://"} {
		if len(baseDSN) > len(sep) && baseDSN[:len(sep)] == sep {
			rest := baseDSN[len(sep):]
			slash := lastIndex(rest, '/')
			if slash < 0 {
				return fmt.Sprintf("%s%s/%s", sep, rest, name)
			}
			query := ""
			path := rest[slash+1:]
			if q := lastIndex(path, '?'); q >= 0 {
				query = path[q:]
				path = path[:q]
			}
			return fmt.Sprintf("%s%s/%s%s", sep, rest[:slash], name, query)
		}
	}
	// key=value DSN form
	return baseDSN + " dbname=" + name
}

func lastIndex(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}
