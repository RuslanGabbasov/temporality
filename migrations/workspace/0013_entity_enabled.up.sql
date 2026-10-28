-- Uniform enable/disable for workspace entities (the same mechanism the
-- trigger table already has): a disabled agent cannot start new runs, a
-- disabled skill is never served to agents. Users keep workspace_user.active.
ALTER TABLE public.workspace_agent ADD COLUMN enabled boolean DEFAULT true NOT NULL;
ALTER TABLE public.workspace_skill ADD COLUMN enabled boolean DEFAULT true NOT NULL;
