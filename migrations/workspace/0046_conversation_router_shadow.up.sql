-- Shadow mode for the Conversational Runtime router
-- (docs/conversational-runtime.md §23): the new router scores every inbound
-- channel message without side effects, and the legacy intake verdict lands
-- in the same row so agreement can be measured per channel before any full
-- migration. One row per inbound message; the runtime half is written at
-- dispatch time, the verdict half when the intake workflow posts its
-- classification back.
CREATE TABLE IF NOT EXISTS public.conversation_router_shadow (
    inbound_message_id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL DEFAULT '',
    channel TEXT NOT NULL DEFAULT '',
    chat_id TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL DEFAULT '',
    text_excerpt TEXT NOT NULL DEFAULT '',
    has_attachments BOOLEAN NOT NULL DEFAULT FALSE,
    runtime_intent TEXT NOT NULL DEFAULT 'unknown',
    runtime_execution TEXT NOT NULL DEFAULT 'unknown',
    runtime_confidence REAL NOT NULL DEFAULT 0,
    runtime_level TEXT NOT NULL DEFAULT 'not_evaluated',
    runtime_served BOOLEAN NOT NULL DEFAULT FALSE,
    legacy_classification TEXT,
    agreement TEXT CHECK (agreement IN ('agree', 'disagree', 'incomparable')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    verdict_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS conversation_router_shadow_agreement_idx
    ON public.conversation_router_shadow (agreement, created_at)
    WHERE agreement IS NOT NULL;
