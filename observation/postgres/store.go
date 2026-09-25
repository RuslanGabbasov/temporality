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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/temporality-project/temporality/observation"
)

//go:embed migrations/observation_events.up.sql
var observationEventsSchema string

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
	_, err := s.pool.Exec(ctx, observationEventsSchema)
	return err
}

func (s *Store) AppendObservation(ctx context.Context, event observation.Event) (bool, error) {
	if err := event.Validate(); err != nil {
		return false, err
	}
	data, err := json.Marshal(event.Data)
	if err != nil {
		return false, err
	}
	evidence, err := json.Marshal(event.Evidence)
	if err != nil {
		return false, err
	}
	var receivedAt any
	if !event.ReceivedAt.IsZero() {
		receivedAt = event.ReceivedAt
	}
	result, err := s.pool.Exec(ctx, `INSERT INTO observation_events
		(source_id,event_id,schema,occurred_at,received_at,integration,source_version,project_id,run_id,task_id,actor_id,actor_type,parent_event_id,type,data,evidence)
		VALUES ($1,$2,$3,$4,COALESCE($5::timestamptz,clock_timestamp()),$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (source_id,event_id) DO NOTHING`,
		event.Source.ID, event.EventID, event.Schema, event.OccurredAt, receivedAt,
		event.Source.Integration, event.Source.Version, event.Context.Project, event.Context.Run,
		event.Context.Task, event.Context.Actor.ID, event.Context.Actor.Type,
		event.Context.ParentEventID, event.Type, data, evidence)
	if err != nil {
		return false, err
	}
	if result.RowsAffected() == 1 {
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
	rows, err := s.pool.Query(ctx, `SELECT source_id,event_id,schema,occurred_at,received_at,integration,source_version,project_id,run_id,task_id,actor_id,actor_type,parent_event_id,type,data,evidence
		FROM observation_events
		WHERE ($1='' OR project_id=$1) AND ($2='' OR source_id=$2) AND ($3='' OR event_id=$3)
		AND ($4='' OR run_id=$4) AND ($5='' OR task_id=$5) AND ($6='' OR actor_id=$6) AND ($7='' OR type=$7)
		AND ($8::timestamptz IS NULL OR occurred_at >= $8) AND ($9::timestamptz IS NULL OR occurred_at <= $9)
		AND ($10::timestamptz IS NULL OR received_at <= $10)
		AND ($11::timestamptz IS NULL OR (occurred_at,received_at,source_id,event_id) > ($11,$12,$13,$14))
		ORDER BY occurred_at,received_at,source_id,event_id LIMIT $15`,
		filter.Project, filter.SourceID, filter.EventID, filter.Run, filter.Task, filter.Actor, filter.Type,
		filter.Since, filter.Until, filter.KnownAt, cursorOccurred, cursorReceived, cursorSource, cursorEvent, limit+1)
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
	if err = json.Unmarshal(data, &event.Data); err != nil {
		return event, err
	}
	if err = json.Unmarshal(evidence, &event.Evidence); err != nil {
		return event, err
	}
	return event, nil
}
