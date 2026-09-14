package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
)

func (s *Store) CreateFrame(ctx context.Context, value frame.Frame, event protocol.Event) error {
	if err := frame.ValidateCreate(value, event); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = insertEvent(ctx, tx, event); err != nil {
		return err
	}
	if err = insertFrame(ctx, tx, value, event.EventID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) TransitionFrame(ctx context.Context, parentID string, transition frame.Transition, event protocol.Event) (frame.TransitionResult, error) {
	if err := frame.ValidateTransition(parentID, transition, event); err != nil {
		return frame.TransitionResult{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return frame.TransitionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var data []byte
	err = tx.QueryRow(ctx, `SELECT data FROM frames WHERE frame_id=$1 FOR UPDATE`, parentID).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return frame.TransitionResult{}, frame.ErrFrameNotFound
	}
	if err != nil {
		return frame.TransitionResult{}, err
	}
	var parent frame.Frame
	if err = json.Unmarshal(data, &parent); err != nil {
		return frame.TransitionResult{}, err
	}
	next, err := frame.Reduce(parent, transition)
	if err != nil {
		return frame.TransitionResult{}, err
	}
	if event.Payload == nil {
		event.Payload = map[string]any{}
	}
	event.Payload["frame_id"] = next.FrameID
	event.EpisodeID = next.EpisodeID
	event.BranchID = next.BranchID
	if err = frame.ValidateCreateEventForTransition(next, event); err != nil {
		return frame.TransitionResult{}, err
	}
	if err = insertEvent(ctx, tx, event); err != nil {
		return frame.TransitionResult{}, err
	}
	if err = insertFrame(ctx, tx, next, event.EventID); err != nil {
		return frame.TransitionResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return frame.TransitionResult{}, err
	}
	return frame.TransitionResult{Frame: next, Event: event}, nil
}

func insertFrame(ctx context.Context, tx pgx.Tx, value frame.Frame, eventID string) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO frames (frame_id,protocol,version,parent_frame_id,agent_id,episode_id,branch_id,objective_id,as_of,revision,reducer_version,data,created_event) VALUES ($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, value.FrameID, value.Protocol, value.Version, value.ParentFrameID, value.AgentID, value.EpisodeID, value.BranchID, value.ObjectiveID, value.AsOf, value.Revision, frame.ReducerVersion, data, eventID)
	return err
}

func (s *Store) GetFrame(ctx context.Context, id string) (frame.Frame, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM frames WHERE frame_id=$1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return frame.Frame{}, frame.ErrFrameNotFound
	}
	if err != nil {
		return frame.Frame{}, err
	}
	var value frame.Frame
	if err = json.Unmarshal(data, &value); err != nil {
		return frame.Frame{}, err
	}
	return value, nil
}
