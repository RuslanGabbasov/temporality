package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
)

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

func (s *Store) Migrate(ctx context.Context, path string) error {
	sql, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, string(sql))
	return err
}

func (s *Store) Append(ctx context.Context, e protocol.Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	provenance, err := json.Marshal(e.Provenance)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO events (event_id,protocol,version,tx_time,valid_time,agent_id,episode_id,branch_id,parent_id,type,payload,provenance,source_trust,evidence_strength,freshness,agent_trust,consensus,contradiction) VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, e.EventID, e.Protocol, e.Version, e.TransactionTime, e.ValidTime, e.AgentID, e.EpisodeID, e.BranchID, e.ParentID, e.Type, payload, provenance, e.SourceTrust, e.EvidenceStrength, e.Freshness, e.AgentTrust, e.Consensus, e.Contradiction)
	return err
}

const selectFields = `event_id::text,protocol,version,tx_time,valid_time,COALESCE(agent_id::text,''),COALESCE(episode_id::text,''),COALESCE(branch_id::text,''),COALESCE(parent_id::text,''),type,payload,provenance,source_trust,evidence_strength,freshness,agent_trust,consensus,contradiction`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (protocol.Event, error) {
	var e protocol.Event
	var payload, provenance []byte
	err := row.Scan(&e.EventID, &e.Protocol, &e.Version, &e.TransactionTime, &e.ValidTime, &e.AgentID, &e.EpisodeID, &e.BranchID, &e.ParentID, &e.Type, &payload, &provenance, &e.SourceTrust, &e.EvidenceStrength, &e.Freshness, &e.AgentTrust, &e.Consensus, &e.Contradiction)
	if err != nil {
		return e, err
	}
	if err = json.Unmarshal(payload, &e.Payload); err != nil {
		return e, err
	}
	if err = json.Unmarshal(provenance, &e.Provenance); err != nil {
		return e, err
	}
	return e, nil
}

func (s *Store) Get(ctx context.Context, id string) (protocol.Event, error) {
	e, err := scan(s.pool.QueryRow(ctx, `SELECT `+selectFields+` FROM events WHERE event_id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return e, substrate.ErrNotFound
	}
	return e, err
}

func (s *Store) List(ctx context.Context, f substrate.EventFilter) ([]protocol.Event, error) {
	query := `SELECT ` + selectFields + ` FROM events WHERE ($1='' OR episode_id=$1::uuid) AND ($2='' OR branch_id=$2::uuid) AND ($3::timestamptz IS NULL OR valid_time <= $3) ORDER BY valid_time,tx_time,event_id`
	rows, err := s.pool.Query(ctx, query, f.EpisodeID, f.BranchID, f.AsOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]protocol.Event, 0)
	for rows.Next() {
		e, scanErr := scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, e)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return result, nil
}

func (s *Store) ListPage(ctx context.Context, request substrate.PageRequest) (substrate.EventPage, error) {
	limit, err := substrate.NormalizePageSize(request.Limit)
	if err != nil {
		return substrate.EventPage{}, err
	}
	cursor, err := substrate.DecodeCursor(request.Cursor)
	if err != nil {
		return substrate.EventPage{}, err
	}
	var cursorValid, cursorTx, cursorID any
	if cursor != nil {
		cursorValid = cursor.ValidTime
		cursorTx = cursor.TransactionTime
		cursorID = cursor.EventID
	}
	query := `SELECT ` + selectFields + ` FROM events WHERE ($1='' OR episode_id=$1::uuid) AND ($2='' OR branch_id=$2::uuid) AND ($3::timestamptz IS NULL OR valid_time <= $3) AND ($4::timestamptz IS NULL OR (valid_time,tx_time,event_id) > ($4,$5,$6::uuid)) ORDER BY valid_time,tx_time,event_id LIMIT $7`
	rows, err := s.pool.Query(ctx, query, request.Filter.EpisodeID, request.Filter.BranchID, request.Filter.AsOf, cursorValid, cursorTx, cursorID, limit+1)
	if err != nil {
		return substrate.EventPage{}, err
	}
	defer rows.Close()
	events := make([]protocol.Event, 0, limit+1)
	for rows.Next() {
		event, scanErr := scan(rows)
		if scanErr != nil {
			return substrate.EventPage{}, scanErr
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return substrate.EventPage{}, fmt.Errorf("list event page: %w", err)
	}
	return substrate.BuildPage(events, limit), nil
}
