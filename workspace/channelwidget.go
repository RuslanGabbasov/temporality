package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Interactive widget sessions (Bot API 10.4, docs/interactive-HITL.md): the
// mapping between a posted channel widget and the human request it renders,
// plus the authoritative widget state. The notification activity creates the
// row through the internal API right after sendWidget; the inbound loop
// reads and advances the state as widget queries arrive. Values never sit in
// the channel alone — a crash must not lose what the user already typed.
type ChannelWidget struct {
	Channel   string          `json:"channel"`
	ChatID    string          `json:"chat_id"`
	MessageID string          `json:"message_id"`
	RequestID string          `json:"request_id"`
	State     json.RawMessage `json:"state"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// UpsertChannelWidget creates or refreshes the widget session row.
func (s *Store) UpsertChannelWidget(ctx context.Context, w ChannelWidget) error {
	state := normalizeChannelWidgetState(w.State)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_channel_widget (channel, chat_id, message_id, request_id, state, updated_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (channel, chat_id, message_id) DO UPDATE SET
		   request_id = EXCLUDED.request_id,
		   state = EXCLUDED.state,
		   updated_at = now()`,
		w.Channel, w.ChatID, w.MessageID, w.RequestID, state)
	return err
}

// GetChannelWidget resolves one widget session by its channel placement.
func (s *Store) GetChannelWidget(ctx context.Context, channel, chatID, messageID string) (ChannelWidget, error) {
	var w ChannelWidget
	var state []byte
	err := s.pool.QueryRow(ctx,
		`SELECT channel, chat_id, message_id, request_id, state, updated_at
		 FROM workspace_channel_widget
		 WHERE channel = $1 AND chat_id = $2 AND message_id = $3`,
		channel, chatID, messageID).
		Scan(&w.Channel, &w.ChatID, &w.MessageID, &w.RequestID, &state, &w.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChannelWidget{}, ErrNotFound
		}
		return ChannelWidget{}, err
	}
	w.State = json.RawMessage(state)
	return w, nil
}

// SaveChannelWidgetState advances the widget state after an accepted op.
func (s *Store) SaveChannelWidgetState(ctx context.Context, channel, chatID, messageID string, state json.RawMessage) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_channel_widget
		 SET state = $4, updated_at = now()
		 WHERE channel = $1 AND chat_id = $2 AND message_id = $3`,
		channel, chatID, messageID, normalizeChannelWidgetState(state))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// normalizeChannelWidgetState guards against nil states sneaking into JSONB.
func normalizeChannelWidgetState(state json.RawMessage) json.RawMessage {
	if len(state) == 0 {
		return json.RawMessage(`{}`)
	}
	return state
}
