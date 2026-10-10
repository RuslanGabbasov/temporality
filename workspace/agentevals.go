package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Agent evaluation models (docs/plan-evaluable-agent.md §2.2 stage 8): a suite
// of cases stored per agent; a run records per-case outcomes for a tested
// definition version. Mirrors skill evaluations (skillevals.go) with one key
// difference: a run is pinned to the definition version whose compiled prompt
// snapshot was tested — comparing versions compares definitions, not the
// drifting current prompt.

// AgentEvaluationSuite is the stored suite of an agent (one per agent, MVP).
type AgentEvaluationSuite struct {
	AgentID   string           `json:"agent_id"`
	Cases     []EvaluationCase `json:"cases"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// AgentEvaluationRun is one executed suite pass over an agent definition version.
type AgentEvaluationRun struct {
	ID           int64                  `json:"id"`
	AgentID      string                 `json:"agent_id"`
	AgentVersion int                    `json:"agent_version"`
	Passed       int                    `json:"passed"`
	Failed       int                    `json:"failed"`
	Cases        []EvaluationCaseResult `json:"cases"`
	CreatedAt    time.Time              `json:"created_at"`
}

// GetAgentEvaluationSuite returns the stored suite; an empty suite (no row) is
// not an error — callers decide whether an empty suite is runnable.
func (s *Store) GetAgentEvaluationSuite(ctx context.Context, agentID string) (AgentEvaluationSuite, error) {
	suite := AgentEvaluationSuite{AgentID: agentID, Cases: []EvaluationCase{}}
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT cases, updated_at FROM workspace_agent_evaluation_suite WHERE agent_id = $1`, agentID).
		Scan(&raw, &suite.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return suite, nil
	}
	if err != nil {
		return suite, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &suite.Cases); err != nil {
			return suite, err
		}
	}
	return suite, nil
}

// SaveAgentEvaluationSuite replaces the stored suite.
func (s *Store) SaveAgentEvaluationSuite(ctx context.Context, agentID string, cases []EvaluationCase) error {
	if cases == nil {
		cases = []EvaluationCase{}
	}
	raw, err := json.Marshal(cases)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO workspace_agent_evaluation_suite (agent_id, cases, updated_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (agent_id) DO UPDATE SET cases = EXCLUDED.cases, updated_at = now()`,
		agentID, raw)
	return err
}

// RecordAgentEvaluationRun stores one executed suite pass.
func (s *Store) RecordAgentEvaluationRun(ctx context.Context, run *AgentEvaluationRun) error {
	raw, err := json.Marshal(run.Cases)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_agent_evaluation_run (agent_id, agent_version, passed, failed, cases)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		run.AgentID, run.AgentVersion, run.Passed, run.Failed, raw).
		Scan(&run.ID, &run.CreatedAt)
}

// ListAgentEvaluationRuns returns recent runs for an agent.
func (s *Store) ListAgentEvaluationRuns(ctx context.Context, agentID string, limit int) ([]AgentEvaluationRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, agent_id, agent_version, passed, failed, cases, created_at
		 FROM workspace_agent_evaluation_run WHERE agent_id = $1 ORDER BY created_at DESC LIMIT $2`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AgentEvaluationRun
	for rows.Next() {
		var run AgentEvaluationRun
		var raw []byte
		if err := rows.Scan(&run.ID, &run.AgentID, &run.AgentVersion, &run.Passed, &run.Failed, &raw, &run.CreatedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &run.Cases); err != nil {
				return nil, err
			}
		}
		result = append(result, run)
	}
	return result, rows.Err()
}
