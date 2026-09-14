package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/frame"
	stepRuntime "github.com/temporality-project/temporality/frp/runtime/step"
)

func (s *Store) CommitStep(ctx context.Context, prepared stepRuntime.Prepared) (stepRuntime.Result, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return stepRuntime.Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var parentData []byte
	if err = tx.QueryRow(ctx, `SELECT data FROM frames WHERE frame_id=$1 FOR UPDATE`, prepared.Current.FrameID).Scan(&parentData); errors.Is(err, pgx.ErrNoRows) {
		return stepRuntime.Result{}, frame.ErrFrameNotFound
	} else if err != nil {
		return stepRuntime.Result{}, err
	}
	var persisted frame.Frame
	if err = json.Unmarshal(parentData, &persisted); err != nil {
		return stepRuntime.Result{}, err
	}
	if !reflect.DeepEqual(persisted, prepared.Current) {
		return stepRuntime.Result{}, stepRuntime.ErrCurrentFrameChanged
	}
	var hasSuccessor bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM frames WHERE parent_frame_id=$1)`, prepared.Current.FrameID).Scan(&hasSuccessor); err != nil {
		return stepRuntime.Result{}, err
	}
	if hasSuccessor {
		return stepRuntime.Result{}, stepRuntime.ErrCurrentFrameChanged
	}
	if err = validateStepRefsPostgres(ctx, tx, prepared); err != nil {
		return stepRuntime.Result{}, err
	}

	if _, err = tx.Exec(ctx, `INSERT INTO cognitive_steps(emission_id,parent_frame_id,next_frame_id,emission,emission_hash,committed_at) VALUES($1,$2,$3,$4,$5,$6)`, prepared.Emission.EmissionID, prepared.Current.FrameID, prepared.Decision.Frame.FrameID, prepared.EmissionJSON, prepared.EmissionHash, prepared.Transition.TransactionTime); err != nil {
		return stepRuntime.Result{}, err
	}
	claims := make([]cognition.Claim, 0, len(prepared.Claims))
	for _, candidate := range prepared.Claims {
		if err = insertEvent(ctx, tx, candidate.Event); err != nil {
			return stepRuntime.Result{}, err
		}
		c := candidate.Value
		if _, err = tx.Exec(ctx, `INSERT INTO claims(claim_id,protocol,version,proposition,confidence,status,created_event,valid_from,valid_to) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, c.ClaimID, c.Protocol, c.Version, c.Proposition, c.Confidence, c.Status, c.CreatedEvent, c.ValidFrom, c.ValidTo); err != nil {
			return stepRuntime.Result{}, err
		}
		claims = append(claims, c)
	}

	nonEntityEvents := prepared.Events[len(prepared.Claims):]
	actionEventCount := len(prepared.Actions) * 2
	attentionCount := len(nonEntityEvents) - actionEventCount - 1
	for i := 0; i < attentionCount; i++ {
		if err = insertEvent(ctx, tx, nonEntityEvents[i]); err != nil {
			return stepRuntime.Result{}, err
		}
	}
	executions := make([]execution.Execution, 0, len(prepared.Actions))
	for _, action := range prepared.Actions {
		if err = insertStepAction(ctx, tx, action); err != nil {
			return stepRuntime.Result{}, err
		}
		executions = append(executions, action.Execution)
	}
	if err = insertEvent(ctx, tx, prepared.Transition); err != nil {
		return stepRuntime.Result{}, err
	}
	if err = insertFrame(ctx, tx, prepared.Decision.Frame, prepared.Transition.EventID); err != nil {
		return stepRuntime.Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return stepRuntime.Result{}, err
	}
	return stepRuntime.Result{Decision: prepared.Decision, Frame: prepared.Decision.Frame, Claims: claims, Executions: executions, Events: prepared.Events}, nil
}

func insertStepAction(ctx context.Context, tx pgx.Tx, action stepRuntime.Action) error {
	definitionData, err := json.Marshal(action.Definition)
	if err != nil {
		return err
	}
	requestData, err := json.Marshal(action.Request)
	if err != nil {
		return err
	}
	executionData, err := json.Marshal(action.Execution)
	if err != nil {
		return err
	}
	var existingData []byte
	err = tx.QueryRow(ctx, `SELECT data FROM affordance_definitions WHERE affordance_id=$1 FOR SHARE`, action.Definition.ID).Scan(&existingData)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO affordance_definitions(affordance_id,protocol,version,data) VALUES($1,$2,$3,$4)`, action.Definition.ID, action.Definition.Protocol, action.Definition.Version, definitionData)
	} else if err == nil {
		var existing affordance.Definition
		if err = json.Unmarshal(existingData, &existing); err == nil && !reflect.DeepEqual(existing, action.Definition) {
			err = affordance.ErrDefinitionFrozen
		}
	}
	if err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, action.Requested); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO affordance_requests(request_id,episode_id,affordance_id,data,requested_event) VALUES($1,$2,$3,$4,$5)`, action.Request.RequestID, action.Request.EpisodeID, action.Request.AffordanceID, requestData, action.Requested.EventID); err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, action.Created); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO executions(execution_id,request_id,episode_id,affordance_id,status,data,created_event) VALUES($1,$2,$3,$4,$5,$6,$7)`, action.Execution.ExecutionID, action.Execution.RequestID, action.Execution.EpisodeID, action.Execution.AffordanceID, action.Execution.Status, executionData, action.Created.EventID)
	return err
}

func validateStepRefsPostgres(ctx context.Context, tx pgx.Tx, prepared stepRuntime.Prepared) error {
	for _, observation := range prepared.Emission.Observation {
		ref, _ := cognition.ParseRef(observation.Ref, false)
		if err := stepRefExists(ctx, tx, ref); err != nil {
			return fmt.Errorf("observation ref %s: %w", observation.Ref, err)
		}
	}
	for _, op := range prepared.Decision.Transition.Operations {
		if op.Ref != nil {
			if err := stepRefExists(ctx, tx, *op.Ref); err != nil {
				return fmt.Errorf("frame ref %s:%s: %w", op.Ref.Type, op.Ref.ID, err)
			}
		}
		if op.Focus != nil && op.Focus.Type != frame.RefQuery {
			if err := stepRefExists(ctx, tx, frame.Ref{Type: op.Focus.Type, ID: op.Focus.ID}); err != nil {
				return fmt.Errorf("attention ref %s:%s: %w", op.Focus.Type, op.Focus.ID, err)
			}
		}
	}
	return nil
}

func stepRefExists(ctx context.Context, tx pgx.Tx, ref frame.Ref) error {
	queries := map[frame.RefType]string{frame.RefEvent: `SELECT 1 FROM events WHERE event_id=$1`, frame.RefClaim: `SELECT 1 FROM claims WHERE claim_id=$1`, frame.RefExecution: `SELECT 1 FROM executions WHERE execution_id=$1`, frame.RefRegion: `SELECT 1 FROM regions WHERE region_id=$1`}
	query, ok := queries[ref.Type]
	if !ok {
		return fmt.Errorf("unsupported ref type %q", ref.Type)
	}
	var one int
	if err := tx.QueryRow(ctx, query, ref.ID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
		return errors.New("reference not found")
	} else {
		return err
	}
}
