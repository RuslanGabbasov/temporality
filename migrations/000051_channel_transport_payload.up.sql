-- docs/channels.md §6: channel settings move from typed columns into one
-- generic JSONB payload. Drivers own their field names, so a new channel
-- ships its settings without a migration. Idempotent (migrations re-run on
-- every boot): the fold+drop happens only while the typed columns exist.
ALTER TABLE channel_transport ADD COLUMN IF NOT EXISTS payload JSONB NOT NULL DEFAULT '{}';
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'channel_transport' AND column_name = 'matrix_homeserver') THEN
        UPDATE channel_transport SET payload = (
            SELECT COALESCE(jsonb_object_agg(channel, fields), '{}'::jsonb)
            FROM (VALUES
                ('matrix',   jsonb_strip_nulls(jsonb_build_object(
                                'homeserver',   NULLIF(matrix_homeserver, ''),
                                'access_token', NULLIF(matrix_access_token, '')))),
                ('telegram', jsonb_strip_nulls(jsonb_build_object(
                                'bot_token',    NULLIF(telegram_bot_token, '')))),
                ('webhook',  jsonb_strip_nulls(jsonb_build_object(
                                'secret',       NULLIF(webhook_secret, ''))))
            ) AS channels(channel, fields)
        ) WHERE id = 1;
        ALTER TABLE channel_transport
            DROP COLUMN matrix_homeserver,
            DROP COLUMN matrix_access_token,
            DROP COLUMN telegram_bot_token,
            DROP COLUMN webhook_secret;
    END IF;
END $$;
