-- Transactional outbox for outbound conversation deliveries
-- (docs/conversational-runtime.md §13.3): an answer is persisted BEFORE it
-- is sent, retried with backoff across restarts, and never lost to a
-- crashed watcher or an unavailable transport.
CREATE TABLE IF NOT EXISTS public.conversation_outbox (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','delivered','failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS conversation_outbox_due_idx
    ON public.conversation_outbox (status, next_attempt_at)
    WHERE status IN ('pending','processing');
