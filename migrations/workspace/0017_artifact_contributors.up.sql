-- Artifact contributors (docs/artifact-lifecycle.md): everyone who put a
-- hand on the artifact — the human creator, the run's agent, editors, the
-- review requester, reviewers, tag editors. The author filter and the
-- suggestions search this set: agent-created artifacts carry an empty
-- origin_user, so a lone creator column cannot answer «who touched this».
-- Idempotent: the set re-runs on every boot (workspace/store.go MigrateAll).
ALTER TABLE public.workspace_artifact ADD COLUMN IF NOT EXISTS contributors text[] NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_workspace_artifact_contributors ON public.workspace_artifact USING gin (contributors);
