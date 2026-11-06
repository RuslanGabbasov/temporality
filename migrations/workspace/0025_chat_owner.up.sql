-- Chat ownership (the "foreign chats in my sidebar" leak): workspace_chat
-- rows had no owner, so the cross-device restore listed EVERY chat of the
-- project to EVERY user — Denis's chats landed in other people's sidebars,
-- and a stray delete removed them for the true owner too.
--
-- The owner column stamps the chat's creator; the listing scopes to the
-- caller's own rows. The backfill attributes existing rows through their
-- task: the task's earliest run carries the initiator in
-- exec_context.actor (the same stamp run_status searches), so chats that
-- ever ran keep their true owner. Rows without a task stay '' — the admin
-- listing keeps them visible so nothing orphans silently.
ALTER TABLE workspace_chat ADD COLUMN IF NOT EXISTS owner_user_id TEXT NOT NULL DEFAULT '';

UPDATE workspace_chat AS c
SET owner_user_id = sub.owner
FROM (
    SELECT DISTINCT ON (task_id) task_id, exec_context->>'actor' AS owner
    FROM workspace_run
    WHERE task_id IS NOT NULL AND task_id <> ''
    ORDER BY task_id, created_at
) AS sub
WHERE c.task_id <> ''
  AND c.task_id = sub.task_id
  AND c.owner_user_id = ''
  AND sub.owner IS NOT NULL
  AND sub.owner <> '';

CREATE INDEX IF NOT EXISTS workspace_chat_owner_idx ON workspace_chat (owner_user_id, project_id);
