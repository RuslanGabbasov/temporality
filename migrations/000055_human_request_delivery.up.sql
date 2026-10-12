-- Team human requests (docs/inbound-messages.md §8): a request addressed to
-- org:<unit-id> fans out to every active member of the unit. One row per
-- (request, member) records who the question was delivered to and through
-- which channel; the first answer closes the request, the remaining delivery
-- rows are marked superseded. Single-recipient requests keep using
-- human_request.resolved_user and carry no rows here.
CREATE TABLE IF NOT EXISTS human_request_delivery (
    request_id TEXT NOT NULL REFERENCES human_request(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL,
    channel    TEXT NOT NULL,                    -- matrix | telegram | webhook | web
    status     TEXT NOT NULL DEFAULT 'delivered', -- delivered | failed | superseded
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, user_id)
);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_hrd_user_open') THEN
        CREATE INDEX idx_hrd_user_open ON human_request_delivery (user_id, request_id);
    END IF;
END $$;
