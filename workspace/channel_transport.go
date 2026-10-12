package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ChannelTransport is the single-row admin configuration of the kernel's
// delivery channels (docs/channels.md §6): a generic per-channel settings
// payload plus the shared UI base URL. The payload is driver-owned — the
// kernel routes whole maps by channel type and never interprets field names,
// so a new channel ships its settings without a storage migration.
//
// Empty fields fall back to the KERNEL_CHANNEL__<TYPE>__<FIELD> environment
// variables (legacy KERNEL_MATRIX_* & co remain aliases), so the row is a
// pure overlay — deployments configured via env keep working unchanged.
type ChannelTransport struct {
	// Payload maps channel type → setting name → value. Secret values never
	// travel back from the API; the masked view is built at the API layer.
	Payload   map[string]map[string]string
	UIURL     string
	UpdatedAt time.Time
}

// ChannelTransport returns the stored overlay; a missing row reads as an
// empty overlay (everything falls back to env).
func (s *Store) ChannelTransport(ctx context.Context) (ChannelTransport, error) {
	var cfg ChannelTransport
	var payload []byte
	err := s.pool.QueryRow(ctx,
		`SELECT payload, ui_url, updated_at FROM channel_transport WHERE id = 1`).
		Scan(&payload, &cfg.UIURL, &cfg.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelTransport{}, nil
	}
	if err != nil {
		return ChannelTransport{}, err
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &cfg.Payload); err != nil {
			return ChannelTransport{}, fmt.Errorf("channel transport payload: %w", err)
		}
	}
	return cfg, nil
}

// SaveChannelTransport upserts the single configuration row. The payload is
// stored verbatim — validation of channel/field names happens at the API
// layer, which knows the driver registry.
func (s *Store) SaveChannelTransport(ctx context.Context, cfg ChannelTransport) error {
	if cfg.Payload == nil {
		cfg.Payload = map[string]map[string]string{}
	}
	encoded, err := json.Marshal(cfg.Payload)
	if err != nil {
		return err
	}
	cfg.UpdatedAt = time.Now().UTC()
	_, err = s.pool.Exec(ctx,
		`INSERT INTO channel_transport (id, payload, ui_url, updated_at)
		 VALUES (1, $1, $2, $3)
		 ON CONFLICT (id) DO UPDATE SET
		   payload = EXCLUDED.payload,
		   ui_url = EXCLUDED.ui_url,
		   updated_at = EXCLUDED.updated_at`,
		encoded, cfg.UIURL, cfg.UpdatedAt)
	return err
}
