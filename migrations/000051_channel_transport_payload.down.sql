-- Restore the typed columns from the generic payload and drop it.
-- Idempotent: the fold runs only while the payload column exists.
ALTER TABLE channel_transport
    ADD COLUMN IF NOT EXISTS matrix_homeserver TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS matrix_access_token TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS telegram_bot_token TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS webhook_secret TEXT NOT NULL DEFAULT '';
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'channel_transport' AND column_name = 'payload') THEN
        UPDATE channel_transport SET
            matrix_homeserver   = COALESCE(payload->'matrix'->>'homeserver', ''),
            matrix_access_token = COALESCE(payload->'matrix'->>'access_token', ''),
            telegram_bot_token  = COALESCE(payload->'telegram'->>'bot_token', ''),
            webhook_secret      = COALESCE(payload->'webhook'->>'secret', '');
        ALTER TABLE channel_transport DROP COLUMN payload;
    END IF;
END $$;
