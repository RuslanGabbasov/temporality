package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/world"
)

func (s *Store) SaveWorld(ctx context.Context, value world.World, event protocol.Event) error {
	if err := world.ValidateSave(value, event); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
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
	var currentVersion int
	err = tx.QueryRow(ctx, `SELECT state_version FROM worlds WHERE world_id=$1 FOR UPDATE`, value.WorldID).Scan(&currentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		if value.StateVersion != 1 {
			return world.ErrWorldStateStale
		}
		if _, err = tx.Exec(ctx, `INSERT INTO worlds(world_id,state_version,data,updated_event) VALUES ($1,$2,$3,$4)`, value.WorldID, value.StateVersion, data, event.EventID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if value.StateVersion <= currentVersion {
		return world.ErrWorldStateStale
	} else if _, err = tx.Exec(ctx, `UPDATE worlds SET state_version=$2,data=$3,updated_event=$4 WHERE world_id=$1`, value.WorldID, value.StateVersion, data, event.EventID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) GetWorld(ctx context.Context, id string) (world.World, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM worlds WHERE world_id=$1`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return world.World{}, world.ErrWorldNotFound
	}
	if err != nil {
		return world.World{}, err
	}
	var value world.World
	if err = json.Unmarshal(data, &value); err != nil {
		return world.World{}, err
	}
	return value, nil
}

func (s *Store) ListWorlds(ctx context.Context) ([]world.World, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM worlds ORDER BY world_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]world.World, 0)
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var value world.World
		if err = json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
