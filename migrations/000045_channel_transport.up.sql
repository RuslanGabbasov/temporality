-- Kernel-managed transport credentials (docs/triggers-and-escalations.md §7):
-- admin-configured bot credentials for the delivery channels, editable in the
-- UI instead of redeploying with new env vars. Values overlay the KERNEL_*
-- environment variables; env remains the fallback, so the row starts as a
-- pure overlay and existing deployments keep working unchanged.
CREATE TABLE IF NOT EXISTS channel_transport (
    id INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    matrix_homeserver TEXT NOT NULL DEFAULT '',
    matrix_access_token TEXT NOT NULL DEFAULT '',
    telegram_bot_token TEXT NOT NULL DEFAULT '',
    webhook_secret TEXT NOT NULL DEFAULT '',
    ui_url TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO channel_transport (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
