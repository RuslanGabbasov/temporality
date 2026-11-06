DROP INDEX IF EXISTS workspace_chat_owner_idx;
ALTER TABLE workspace_chat DROP COLUMN IF EXISTS owner_user_id;
