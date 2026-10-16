-- Slack-style conversation threads (docs/channels.md §9): thread_ref stores
-- the channel's thread pointer for the delivered question — the posted
-- message id (slack ts) when the transport reports it, or the thread the
-- user's first threaded reply opened (backfilled by the inbound loop).
-- Follow-up questions of the same run to the same recipient join that thread
-- instead of scattering across the channel, and replies inside the thread
-- match their ask exactly instead of "chat + oldest open".
ALTER TABLE human_request ADD COLUMN IF NOT EXISTS thread_ref TEXT NOT NULL DEFAULT '';
