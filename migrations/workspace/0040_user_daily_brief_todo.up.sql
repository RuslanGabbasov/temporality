-- Personal daily brief («Мой день») and the personal GTD todo list: the
-- supervisor's morning digest for one user lives one row per (user, day) —
-- the payload keeps the narrative summary plus the factual snapshot the UI
-- renders — and the todo widget clusters personal notes by GTD groups
-- (inbox/next/waiting/someday/done). Idempotent on re-run (MigrateAll).
CREATE TABLE IF NOT EXISTS public.user_daily_brief (
    user_id      TEXT NOT NULL,
    day          TEXT NOT NULL,                    -- YYYY-MM-DD (UTC)
    payload      JSONB NOT NULL,                   -- the full UserBriefPayload JSON
    generated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, day)
);
CREATE TABLE IF NOT EXISTS public.user_todo (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    grp        TEXT NOT NULL DEFAULT 'inbox',     -- inbox | next | waiting | someday | done
    text       TEXT NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    sort       INT  NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS user_todo_user_idx ON public.user_todo (user_id, grp, sort);
