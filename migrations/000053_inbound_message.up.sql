-- Intake pipeline (docs/inbound-messages.md §4.1): one durable row per
-- inbound channel message. The id is deterministic
-- (inbound/{channel}/{message_id}) and (channel, message_id) is unique, so a
-- /sync replay or a kernel restart never produces a second processing pass.
-- Classification and routing state live here; the dialog itself is the intake
-- run in the system "intake" project.
CREATE TABLE IF NOT EXISTS workspace_inbound_message (
    id              TEXT PRIMARY KEY,
    channel         TEXT NOT NULL,
    message_id      TEXT NOT NULL,
    sender_raw      TEXT NOT NULL DEFAULT '',
    sender_user_id  TEXT,
    room            TEXT NOT NULL DEFAULT '',
    body            TEXT NOT NULL DEFAULT '',
    attachments     JSONB NOT NULL DEFAULT '[]',
    status          TEXT NOT NULL DEFAULT 'received',   -- received|classified|clarifying|routed|rejected|expired
    classification  TEXT NOT NULL DEFAULT '',           -- task|amendment|status|cancel|chatter|reply
    intake_run_id   TEXT,
    target_run_id   TEXT,
    project_id      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel, message_id)
);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_inbound_message_created_at') THEN
        CREATE INDEX idx_inbound_message_created_at ON workspace_inbound_message (created_at DESC);
    END IF;
END $$;
