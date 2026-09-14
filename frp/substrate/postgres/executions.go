package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/protocol"
)

func (s *Store) CreateExecution(ctx context.Context, def affordance.Definition, request affordance.Request, value execution.Execution, requested, created protocol.Event) error {
	if err := execution.ValidateCreate(def, request, value, requested, created); err != nil {
		return err
	}
	definitionData, err := json.Marshal(def)
	if err != nil {
		return err
	}
	requestData, err := json.Marshal(request)
	if err != nil {
		return err
	}
	executionData, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingDefinition []byte
	err = tx.QueryRow(ctx, `SELECT data FROM affordance_definitions WHERE affordance_id=$1 FOR SHARE`, def.ID).Scan(&existingDefinition)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO affordance_definitions(affordance_id,protocol,version,data) VALUES ($1,$2,$3,$4)`, def.ID, def.Protocol, def.Version, definitionData)
	} else if err == nil {
		var existing affordance.Definition
		if err = json.Unmarshal(existingDefinition, &existing); err == nil && !reflect.DeepEqual(existing, def) {
			err = affordance.ErrDefinitionFrozen
		}
	}
	if err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, requested); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO affordance_requests(request_id,episode_id,affordance_id,data,requested_event) VALUES ($1,$2,$3,$4,$5)`, request.RequestID, request.EpisodeID, request.AffordanceID, requestData, requested.EventID); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, created); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO executions(execution_id,request_id,episode_id,affordance_id,status,data,created_event) VALUES ($1,$2,$3,$4,$5,$6,$7)`, value.ExecutionID, value.RequestID, value.EpisodeID, value.AffordanceID, value.Status, executionData, created.EventID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) TransitionExecution(ctx context.Context, id string, next execution.Status, at time.Time, executionError *execution.ExecutionError, event protocol.Event) (execution.Execution, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return execution.Execution{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var data []byte
	if err = tx.QueryRow(ctx, `SELECT data FROM executions WHERE execution_id=$1 FOR UPDATE`, id).Scan(&data); errors.Is(err, pgx.ErrNoRows) {
		return execution.Execution{}, execution.ErrNotFound
	} else if err != nil {
		return execution.Execution{}, err
	}
	var current execution.Execution
	if err = json.Unmarshal(data, &current); err != nil {
		return execution.Execution{}, err
	}
	updated, err := current.Transition(next, at, executionError)
	if err != nil {
		return execution.Execution{}, err
	}
	if err = execution.ValidateTransitionEvent(current, next, at, event); err != nil {
		return execution.Execution{}, err
	}
	if err = insertEvent(ctx, tx, event); err != nil {
		return execution.Execution{}, err
	}
	data, err = json.Marshal(updated)
	if err != nil {
		return execution.Execution{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE executions SET status=$2,data=$3 WHERE execution_id=$1`, id, updated.Status, data); err != nil {
		return execution.Execution{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return execution.Execution{}, err
	}
	return updated, nil
}

func (s *Store) GetDefinition(ctx context.Context, id string) (affordance.Definition, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM affordance_definitions WHERE affordance_id=$1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return affordance.Definition{}, affordance.ErrDefinitionNotFound
	}
	var value affordance.Definition
	if err == nil {
		err = json.Unmarshal(data, &value)
	}
	return value, err
}

func (s *Store) GetRequest(ctx context.Context, id string) (affordance.Request, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM affordance_requests WHERE request_id=$1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return affordance.Request{}, affordance.ErrRequestNotFound
	}
	var value affordance.Request
	if err == nil {
		err = json.Unmarshal(data, &value)
	}
	return value, err
}

func (s *Store) GetExecution(ctx context.Context, id string) (execution.Execution, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM executions WHERE execution_id=$1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Execution{}, execution.ErrNotFound
	}
	var value execution.Execution
	if err == nil {
		err = json.Unmarshal(data, &value)
	}
	return value, err
}

func (s *Store) ListActiveExecutions(ctx context.Context, episodeID string) ([]execution.Execution, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM executions WHERE ($1='' OR episode_id=$1::uuid) AND status IN ('created','running') ORDER BY execution_id`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]execution.Execution, 0)
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var value execution.Execution
		if err = json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
