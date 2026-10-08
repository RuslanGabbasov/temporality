package workspace

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ChannelTransport is the single-row admin configuration of the kernel's
// delivery transports (docs/triggers-and-escalations.md §7): bot credentials
// for bot-mediated channels plus the shared webhook secret and UI base URL.
// Empty fields fall back to the KERNEL_* environment variables, so the row is
// a pure overlay — deployments configured via env keep working unchanged.
//
// Secret fields carry json:"-" so accidental marshaling of the struct never
// leaks credentials; the API layer builds its masked response explicitly.
type ChannelTransport struct {
	MatrixHomeserver  string    `json:"matrix_homeserver"`
	MatrixAccessToken string    `json:"-"`
	TelegramBotToken  string    `json:"-"`
	WebhookSecret     string    `json:"-"`
	UIURL             string    `json:"ui_url"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ChannelTransport returns the stored overlay; a missing row reads as an
// empty overlay (everything falls back to env).
func (s *Store) ChannelTransport(ctx context.Context) (ChannelTransport, error) {
	var cfg ChannelTransport
	err := s.pool.QueryRow(ctx,
		`SELECT matrix_homeserver, matrix_access_token, telegram_bot_token, webhook_secret, ui_url, updated_at
		 FROM channel_transport WHERE id = 1`).
		Scan(&cfg.MatrixHomeserver, &cfg.MatrixAccessToken, &cfg.TelegramBotToken, &cfg.WebhookSecret, &cfg.UIURL, &cfg.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelTransport{}, nil
	}
	return cfg, err
}

// SaveChannelTransport upserts the single configuration row.
func (s *Store) SaveChannelTransport(ctx context.Context, cfg ChannelTransport) error {
	cfg.UpdatedAt = time.Now().UTC()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO channel_transport (id, matrix_homeserver, matrix_access_token, telegram_bot_token, webhook_secret, ui_url, updated_at)
		 VALUES (1, $1, $2, $3, $4, $5, $6)
		 ON CONFLICT (id) DO UPDATE SET
		   matrix_homeserver = EXCLUDED.matrix_homeserver,
		   matrix_access_token = EXCLUDED.matrix_access_token,
		   telegram_bot_token = EXCLUDED.telegram_bot_token,
		   webhook_secret = EXCLUDED.webhook_secret,
		   ui_url = EXCLUDED.ui_url,
		   updated_at = EXCLUDED.updated_at`,
		cfg.MatrixHomeserver, cfg.MatrixAccessToken, cfg.TelegramBotToken, cfg.WebhookSecret, cfg.UIURL, cfg.UpdatedAt)
	return err
}
