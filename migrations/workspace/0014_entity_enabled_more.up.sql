-- Uniform enable/disable reaches the rest of the workspace entities (the
-- trigger/MCP pattern): a disabled team cannot be launched, a disabled
-- sandbox profile cannot be resolved for new runs, a disabled secret stops
-- resolving to values. Users keep workspace_user.active.
ALTER TABLE public.workspace_team ADD COLUMN enabled boolean DEFAULT true NOT NULL;
ALTER TABLE public.workspace_sandbox_profile ADD COLUMN enabled boolean DEFAULT true NOT NULL;
ALTER TABLE public.workspace_secret ADD COLUMN enabled boolean DEFAULT true NOT NULL;
