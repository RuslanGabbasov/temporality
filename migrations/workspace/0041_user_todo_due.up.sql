-- TickTick-lite due dates for the personal GTD todos («Мой день»): an
-- optional due date drives the «Сегодня» section and the brief's focus, and
-- return_grp remembers the GTD group a todo came from when the checkbox
-- moves it to done — unchecking restores it instead of dumping it in inbox.
-- Idempotent on re-run (MigrateAll).
ALTER TABLE public.user_todo ADD COLUMN IF NOT EXISTS due DATE NULL;
ALTER TABLE public.user_todo ADD COLUMN IF NOT EXISTS return_grp TEXT NOT NULL DEFAULT '';
