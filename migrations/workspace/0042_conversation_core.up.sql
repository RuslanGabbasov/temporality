-- Durable conversational records. Legacy workspace_chat remains available
-- as a compatibility surface while clients migrate to these normalized rows.
CREATE TABLE IF NOT EXISTS public.conversation (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL DEFAULT '',
    owner_id TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
    title TEXT NOT NULL DEFAULT '',
    last_message_sequence BIGINT NOT NULL DEFAULT 0 CHECK (last_message_sequence >= 0),
    context_version BIGINT NOT NULL DEFAULT 1 CHECK (context_version > 0),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS conversation_owner_updated_idx
    ON public.conversation (organization_id, owner_id, updated_at DESC);

-- Maps a transport's stable chat/room identity to the durable conversation.
-- Threads and individual message IDs remain on conversation_message.
CREATE TABLE IF NOT EXISTS public.conversation_transport (
    organization_id TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    source_conversation_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES public.conversation(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, source, source_conversation_id),
    UNIQUE (conversation_id, source, source_conversation_id)
);

CREATE TABLE IF NOT EXISTS public.conversation_participant (
    conversation_id TEXT NOT NULL REFERENCES public.conversation(id) ON DELETE CASCADE,
    participant_type TEXT NOT NULL CHECK (participant_type IN ('human','agent','system','external_identity')),
    participant_id TEXT NOT NULL,
    permissions JSONB NOT NULL DEFAULT '{}'::jsonb,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at TIMESTAMPTZ,
    PRIMARY KEY (conversation_id, participant_type, participant_id)
);

CREATE TABLE IF NOT EXISTS public.conversation_message (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL REFERENCES public.conversation(id) ON DELETE CASCADE,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    parent_message_id TEXT REFERENCES public.conversation_message(id) ON DELETE SET NULL,
    sender_type TEXT NOT NULL CHECK (sender_type IN ('human','agent','system','external_identity')),
    sender_id TEXT NOT NULL DEFAULT '',
    message_type TEXT NOT NULL DEFAULT 'text',
    content JSONB NOT NULL DEFAULT '{}'::jsonb,
    attachments JSONB NOT NULL DEFAULT '[]'::jsonb,
    source TEXT NOT NULL DEFAULT '',
    source_message_id TEXT NOT NULL DEFAULT '',
    source_thread_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'received' CHECK (status IN ('received','persisted','queued','processing','completed','retryable_failure','failed','cancelled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (conversation_id, sequence)
);

CREATE UNIQUE INDEX IF NOT EXISTS conversation_message_source_event_idx
    ON public.conversation_message (organization_id, source, source_message_id)
    WHERE source <> '' AND source_message_id <> '';
CREATE INDEX IF NOT EXISTS conversation_message_history_idx
    ON public.conversation_message (conversation_id, sequence);

CREATE TABLE IF NOT EXISTS public.conversation_branch (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES public.conversation(id) ON DELETE CASCADE,
    parent_message_id TEXT REFERENCES public.conversation_message(id) ON DELETE SET NULL,
    title TEXT NOT NULL DEFAULT '',
    context_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.conversation_task (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES public.conversation(id) ON DELETE CASCADE,
    origin_message_id TEXT REFERENCES public.conversation_message(id) ON DELETE SET NULL,
    task_id TEXT NOT NULL,
    run_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS conversation_task_lookup_idx
    ON public.conversation_task (conversation_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS conversation_task_pair_idx
    ON public.conversation_task (conversation_id, task_id);

CREATE TABLE IF NOT EXISTS public.conversation_interaction (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES public.conversation(id) ON DELETE CASCADE,
    origin_message_id TEXT REFERENCES public.conversation_message(id) ON DELETE SET NULL,
    source_message_id TEXT NOT NULL DEFAULT '',
    interaction_type TEXT NOT NULL,
    domain_object_type TEXT NOT NULL,
    domain_object_id TEXT NOT NULL,
    target_version INTEGER,
    assigned_to TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    UNIQUE (interaction_type, domain_object_id, conversation_id)
);

-- Down migration is intentionally destructive and is only for controlled
-- development rollback; production rollback must preserve conversational data.
