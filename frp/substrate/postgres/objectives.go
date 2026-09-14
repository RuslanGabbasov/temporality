package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
)

func (s *Store) CreateObjective(ctx context.Context, value objective.Objective, event protocol.Event) error {
	if err := objective.ValidateCreate(value, event); err != nil {
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
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO objectives(objective_id,protocol,version,episode_id,data,created_event) VALUES($1,$2,$3,$4,$5,$6)`, value.ObjectiveID, value.Protocol, value.Version, value.EpisodeID, data, event.EventID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) GetObjective(ctx context.Context, id string) (objective.Objective, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM objectives WHERE objective_id=$1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return objective.Objective{}, objective.ErrNotFound
	}
	if err != nil {
		return objective.Objective{}, err
	}
	var value objective.Objective
	if err = json.Unmarshal(data, &value); err != nil {
		return objective.Objective{}, err
	}
	return value, nil
}
