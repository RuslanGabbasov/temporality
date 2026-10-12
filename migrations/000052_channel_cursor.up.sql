-- docs/channels.md §5: per-channel inbound cursors. The loop persists the
-- opaque cursor plus a fingerprint of the channel settings: a credential
-- change means a different account stream, so the cursor must reset across
-- restarts too — which is why the fingerprint lives beside it.
CREATE TABLE IF NOT EXISTS workspace_channel_cursor (
    channel TEXT PRIMARY KEY,           -- driver type (matrix, telegram, …)
    cursor TEXT NOT NULL DEFAULT '',    -- opaque driver cursor (since/offset)
    identity TEXT NOT NULL DEFAULT '',  -- ChannelFingerprint of the settings the cursor belongs to
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
