-- User-owned communication channels (docs/triggers-and-escalations.md §6):
-- delivery transports are part of the user profile, not agent configuration.
-- channels: [{type: matrix|telegram, address, enabled}]
-- preferred_channel: "" (web inbox), web, or a configured channel type.
ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS channels JSONB NOT NULL DEFAULT '[]';
ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS preferred_channel TEXT NOT NULL DEFAULT '';
