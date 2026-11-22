-- The learning program upsert keys on user_id alone:
--   INSERT ... ON CONFLICT (user_id) DO UPDATE ...
-- Migration 0034 created a NON-unique (user_id, status) index, which
-- PostgreSQL rejects as the ON CONFLICT target (error 42P10 "no unique or
-- exclusion constraint matching the ON CONFLICT specification") — every
-- POST /v1/learning/program/build failed with 500. One program per user is
-- the intended invariant, so the key becomes a unique index on user_id.
-- The CREATE TABLE IF NOT EXISTS keeps the migration correct on databases
-- where 0034 never ran (fresh manual application).
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
DROP INDEX IF EXISTS learning_program_user_idx;
CREATE UNIQUE INDEX IF NOT EXISTS learning_program_user_unique ON public.learning_program (user_id);
