package workspace

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Supervisor verdict acknowledgements (docs/supervisor-model.md §7): durable
// state, not an event replay. The UI used to rebuild the closed set from the
// newest ack events; once more than a page of acknowledgements accumulated,
// older ones fell out of the window and closed rows resurrected — single and
// bulk queue actions looked broken.

// AckSupervisorVerdicts records acknowledgements for the given verdict event
// ids (idempotent: re-acking is a no-op). The actor is stored for the audit.
func (s *Store) AckSupervisorVerdicts(ctx context.Context, verdictEventIDs []string, actor string) error {
	batch := &pgx.Batch{}
	for _, id := range verdictEventIDs {
		if id == "" {
			continue
		}
		batch.Queue(`INSERT INTO supervisor_ack (verdict_event_id, actor) VALUES ($1, $2)
			 ON CONFLICT (verdict_event_id) DO NOTHING`, id, actor)
	}
	if batch.Len() == 0 {
		return nil
	}
	return s.pool.SendBatch(ctx, batch).Close()
}

// ListSupervisorAcks returns every acknowledged verdict event id. The set
// grows with the queue's lifetime; ids are short strings, so the full listing
// stays well under a megabyte for years of use.
func (s *Store) ListSupervisorAcks(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT verdict_event_id FROM supervisor_ack`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
