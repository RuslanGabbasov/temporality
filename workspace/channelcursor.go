package workspace

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ChannelCursor is the persisted inbound state of one channel loop
// (docs/channels.md §5): the opaque driver cursor (matrix since, telegram
// offset) plus the fingerprint of the settings it belongs to. A credential
// change means a different account stream — the loop detects the mismatch
// and resets the cursor instead of trusting a stale position.
type ChannelCursor struct {
	Cursor   string
	Identity string
}

// ChannelCursor loads the stored loop state; a missing row reads as a fresh
// start (empty cursor, unknown identity).
func (s *Store) ChannelCursor(ctx context.Context, channel string) (ChannelCursor, error) {
	var cursor ChannelCursor
	err := s.pool.QueryRow(ctx,
		`SELECT cursor, identity FROM workspace_channel_cursor WHERE channel = $1`, channel).
		Scan(&cursor.Cursor, &cursor.Identity)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelCursor{}, nil
	}
	return cursor, err
}

// SaveChannelCursor upserts the loop state after a successful cycle.
func (s *Store) SaveChannelCursor(ctx context.Context, channel string, cursor ChannelCursor) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_channel_cursor (channel, cursor, identity, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (channel) DO UPDATE SET
		   cursor = EXCLUDED.cursor,
		   identity = EXCLUDED.identity,
		   updated_at = now()`,
		channel, cursor.Cursor, cursor.Identity)
	return err
}
