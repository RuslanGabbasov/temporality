-- Artifact review sweep (docs/artifact-lifecycle.md §6 Phase 2): reminder and
-- escalation bookkeeping for open review rounds. NULL = not sent yet; the
-- sweep stamps them best-effort after the channel text goes out.
ALTER TABLE public.workspace_artifact_review
    ADD COLUMN IF NOT EXISTS reminded_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS escalated_at TIMESTAMPTZ;
