package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/procedure"
)

func (s *Store) ReplaceProcedures(ctx context.Context, episodeID string, values []procedure.Procedure) error {
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return err
		}
		if value.EpisodeID != episodeID {
			return errors.New("procedure episode does not match replacement scope")
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM procedures WHERE episode_id=$1`, episodeID); err != nil {
		return err
	}
	for _, value := range values {
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.Exec(ctx, `INSERT INTO procedures(procedure_id,episode_id,protocol,version,semantic_trigger,successes,failures,success_rate,projection_version,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, value.ProcedureID, value.EpisodeID, value.Protocol, value.Version, value.SemanticTrigger, value.Successes, value.Failures, value.SuccessRate, value.ProjectionVersion, data)
		if err != nil {
			return err
		}
		for _, executionID := range value.EvidenceExecutionIDs {
			if _, err = tx.Exec(ctx, `INSERT INTO procedure_evidence_memberships(procedure_id,execution_id) VALUES($1,$2)`, value.ProcedureID, executionID); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListProcedures(ctx context.Context, filter procedure.Filter) ([]procedure.Procedure, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM procedures WHERE ($1='' OR episode_id=$1::uuid) ORDER BY procedure_id`, filter.EpisodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]procedure.Procedure, 0)
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var value procedure.Procedure
		if err = json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) GetProcedure(ctx context.Context, id string) (procedure.Procedure, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM procedures WHERE procedure_id=$1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return procedure.Procedure{}, procedure.ErrNotFound
	}
	var value procedure.Procedure
	if err == nil {
		err = json.Unmarshal(data, &value)
	}
	return value, err
}

func (s *Store) ListProcedureEvidence(ctx context.Context, episodeID string) ([]procedure.Evidence, error) {
	rows, err := s.pool.Query(ctx, `SELECT e.data,r.data FROM executions e JOIN affordance_requests r ON r.request_id=e.request_id WHERE e.episode_id=$1 AND e.status IN ('completed','failed','cancelled') ORDER BY e.execution_id`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]procedure.Evidence, 0)
	for rows.Next() {
		var executionData, requestData []byte
		if err = rows.Scan(&executionData, &requestData); err != nil {
			return nil, err
		}
		var e execution.Execution
		var r affordance.Request
		if err = json.Unmarshal(executionData, &e); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(requestData, &r); err != nil {
			return nil, err
		}
		result = append(result, procedure.Evidence{Execution: e, Request: r})
	}
	return result, rows.Err()
}
