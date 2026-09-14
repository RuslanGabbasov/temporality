package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/planner"
)

func (s *Store) EnsurePlannerRun(ctx context.Context, value planner.Run) (planner.Run, error) {
	contextData, err := json.Marshal(value.Context)
	if err != nil {
		return planner.Run{}, err
	}
	stateData, err := json.Marshal(value.State)
	if err != nil {
		return planner.Run{}, err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO planner_runs(execution_id,context,state,status,error,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (execution_id) DO NOTHING`, value.ExecutionID, contextData, stateData, value.Status, value.Error, value.CreatedAt, value.UpdatedAt)
	if err != nil {
		return planner.Run{}, err
	}
	return s.GetPlannerRun(ctx, value.ExecutionID)
}

func (s *Store) GetPlannerRun(ctx context.Context, executionID string) (planner.Run, error) {
	var value planner.Run
	var contextData, stateData []byte
	err := s.pool.QueryRow(ctx, `SELECT execution_id::text,context,state,status,error,created_at,updated_at FROM planner_runs WHERE execution_id=$1`, executionID).Scan(&value.ExecutionID, &contextData, &stateData, &value.Status, &value.Error, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return planner.Run{}, planner.ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(contextData, &value.Context)
	}
	if err == nil {
		err = json.Unmarshal(stateData, &value.State)
	}
	return value, err
}

func (s *Store) ListPlannerSteps(ctx context.Context, executionID string) ([]planner.DurableStep, error) {
	rows, err := s.pool.Query(ctx, `SELECT execution_id::text,ordinal,proposal,result,status,created_at,updated_at FROM planner_steps WHERE execution_id=$1 ORDER BY ordinal`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]planner.DurableStep, 0)
	for rows.Next() {
		var value planner.DurableStep
		var proposalData []byte
		var resultData []byte
		if err = rows.Scan(&value.ExecutionID, &value.Ordinal, &proposalData, &resultData, &value.Status, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(proposalData, &value.Proposal); err != nil {
			return nil, err
		}
		if resultData != nil {
			value.Result = &planner.StepResult{}
			if err = json.Unmarshal(resultData, value.Result); err != nil {
				return nil, err
			}
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) RecordPlannerProposal(ctx context.Context, executionID string, proposal planner.Proposal, state planner.State, at time.Time) (planner.DurableStep, error) {
	proposalData, err := json.Marshal(proposal)
	if err != nil {
		return planner.DurableStep{}, err
	}
	stateData, err := json.Marshal(state)
	if err != nil {
		return planner.DurableStep{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return planner.DurableStep{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status planner.RunStatus
	if err = tx.QueryRow(ctx, `SELECT status FROM planner_runs WHERE execution_id=$1 FOR UPDATE`, executionID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return planner.DurableStep{}, planner.ErrNotFound
	} else if err != nil {
		return planner.DurableStep{}, err
	}
	if status != planner.RunRunning {
		return planner.DurableStep{}, errors.New("planner run is terminal")
	}
	var ordinal int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(ordinal),0)+1 FROM planner_steps WHERE execution_id=$1`, executionID).Scan(&ordinal); err != nil {
		return planner.DurableStep{}, err
	}
	stepStatus := planner.StepProposed
	if proposal.Complete {
		stepStatus = planner.StepCompleted
	}
	if _, err = tx.Exec(ctx, `INSERT INTO planner_steps(execution_id,ordinal,proposal,status,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$5)`, executionID, ordinal, proposalData, stepStatus, at); err != nil {
		return planner.DurableStep{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE planner_runs SET state=$2,updated_at=$3 WHERE execution_id=$1 AND status='running'`, executionID, stateData, at); err != nil {
		return planner.DurableStep{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return planner.DurableStep{}, err
	}
	return planner.DurableStep{ExecutionID: executionID, Ordinal: ordinal, Proposal: proposal, Status: stepStatus, CreatedAt: at, UpdatedAt: at}, nil
}

func (s *Store) RecordPlannerResult(ctx context.Context, executionID string, ordinal int, result planner.StepResult, state planner.State, at time.Time) error {
	resultData, err := json.Marshal(result)
	if err != nil {
		return err
	}
	stateData, err := json.Marshal(state)
	if err != nil {
		return err
	}
	status := planner.StepFailed
	if result.Success {
		status = planner.StepCompleted
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE planner_steps SET result=$3,status=$4,updated_at=$5 WHERE execution_id=$1 AND ordinal=$2 AND status='proposed'`, executionID, ordinal, resultData, status, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("planner step not found or already has result")
	}
	if _, err = tx.Exec(ctx, `UPDATE planner_runs SET state=$2,updated_at=$3 WHERE execution_id=$1 AND status='running'`, executionID, stateData, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) FinishPlannerRun(ctx context.Context, executionID string, status planner.RunStatus, state planner.State, message string, at time.Time) error {
	if status != planner.RunCompleted && status != planner.RunFailed {
		return errors.New("invalid terminal planner status")
	}
	stateData, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE planner_runs SET state=$2,status=$3,error=$4,updated_at=$5 WHERE execution_id=$1 AND status='running'`, executionID, stateData, status, message, at)
	if err == nil && tag.RowsAffected() != 1 {
		err = errors.New("planner run not found or already terminal")
	}
	return err
}
