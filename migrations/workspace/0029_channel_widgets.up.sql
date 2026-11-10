-- Interactive widget sessions (Bot API 10.4, docs/interactive-HITL.md):
-- the channel placement of one posted widget, the human request it renders
-- and the authoritative widget state the kernel advances as queries arrive.
CREATE TABLE IF NOT EXISTS workspace_channel_widget (
    channel    TEXT        NOT NULL,
    chat_id    TEXT        NOT NULL,
    message_id TEXT        NOT NULL,
    request_id TEXT        NOT NULL,
    state      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel, chat_id, message_id)
);

-- Cleanup joins: close widget sessions of an answered/cancelled request.
CREATE INDEX IF NOT EXISTS idx_workspace_channel_widget_request
    ON workspace_channel_widget (request_id);
