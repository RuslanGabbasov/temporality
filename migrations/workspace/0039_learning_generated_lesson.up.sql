-- Generated lessons (docs/ai-guided-onboarding-and-learning.md §10.2
-- extended): the supervisor writes a lesson per user from the page/version
-- highlights, and the session must survive restarts (§16.4) — so the lesson
-- lives in the store, not in model memory.
--
-- One row per (user, kind, page): regeneration overwrites the CONTENT but
-- keeps the row id, so an in-flight session on the same lesson id stays
-- valid across a regenerate. Idempotent on re-run (MigrateAll re-applies).
CREATE TABLE IF NOT EXISTS public.learning_generated_lesson (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT 'page',     -- page | whats_new
    page        TEXT NOT NULL DEFAULT '',         -- route for kind=page, '' for whats_new
    feature_id  TEXT NOT NULL DEFAULT '',
    app_version TEXT NOT NULL DEFAULT '',         -- buildVersion at generation time
    title       TEXT NOT NULL DEFAULT '',
    lesson      JSONB NOT NULL,                   -- the full LearningLesson JSON
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS learning_generated_lesson_user_key
    ON public.learning_generated_lesson (user_id, kind, page);
