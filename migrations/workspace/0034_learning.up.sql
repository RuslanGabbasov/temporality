-- Learning subsystem (docs/ai-guided-onboarding-and-learning.md, §4/§11):
-- the Definition lives in code (the lesson catalog ships with the feature),
-- the Execution (programs, sessions) and the Evidence (progress, mastery)
-- live here — per user, never mixed between organizations.
CREATE TABLE IF NOT EXISTS public.learning_program (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'initial_onboarding',
    goal       TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'active',  -- active|paused|completed|cancelled
    items      JSONB NOT NULL DEFAULT '[]',     -- [{lesson_id, state, why, scheduled_at}]
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS learning_program_user_idx ON public.learning_program (user_id, status);

CREATE TABLE IF NOT EXISTS public.learning_session (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    lesson_id  TEXT NOT NULL,
    program_id TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'active',  -- active|waiting_for_user|paused|completed|aborted
    step_index INT NOT NULL DEFAULT 0,
    history    JSONB NOT NULL DEFAULT '[]',     -- [{at, kind, payload}]
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS learning_session_user_idx ON public.learning_session (user_id, status);

CREATE TABLE IF NOT EXISTS public.learning_progress (
    user_id    TEXT NOT NULL,
    feature_id TEXT NOT NULL,
    lesson_id  TEXT NOT NULL DEFAULT '',
    completion BOOLEAN NOT NULL DEFAULT false,
    mastery    TEXT NOT NULL DEFAULT 'unknown', -- unknown|introduced|practiced|verified|needs_refresh
    source     TEXT NOT NULL DEFAULT '',        -- lesson|verification|self|suggestion
    detail     TEXT NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, feature_id)
);

CREATE TABLE IF NOT EXISTS public.learning_suggestion (
    user_id    TEXT NOT NULL,
    feature_id TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'suggested', -- suggested|dismissed|accepted
    at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, feature_id)
);
