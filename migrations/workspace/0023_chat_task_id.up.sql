-- Chat → task linkage (Workspace.tsx live-run badge): a chat whose task has
-- a live run marks itself in the chat list, regardless of who started the run
-- (this tab, a trigger, a channel intake, a delegation). The liveness map is
-- keyed by task id — restored chats (another device, wiped browser) need the
-- task id on the chat row to join it. Transcript-embedded ids are opaque to
-- the server by design (docs/resilience-and-scale.md §4); the link is a
-- first-class column instead.
ALTER TABLE workspace_chat ADD COLUMN IF NOT EXISTS task_id TEXT NOT NULL DEFAULT '';
