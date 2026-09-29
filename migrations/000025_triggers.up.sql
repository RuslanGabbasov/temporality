CREATE TABLE IF NOT EXISTS workspace_trigger (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES workspace_project(id) ON DELETE CASCADE,
    agent_id    TEXT REFERENCES workspace_agent(id) ON DELETE SET NULL,
    name        TEXT NOT NULL,
    type        TEXT NOT NULL CHECK (type IN ('schedule', 'webhook', 'event')),
    enabled     BOOLEAN NOT NULL DEFAULT true,
    config      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_trigger_project ON workspace_trigger(project_id);
CREATE INDEX IF NOT EXISTS idx_workspace_trigger_type ON workspace_trigger(type);

-- Schedule config example:
-- {"cron": "0 9 * * 1-5", "prompt": "Review recent changes", "timezone": "UTC"}
--
-- Webhook config example:
-- {"path": "deploy-notify", "secret": "hmac-secret", "prompt_template": "Deploy completed: {{body.tag}}"}
--
-- Event config example:
-- {"event_type": "tool.failed", "filter": {"tool": "run_command"}, "prompt": "Investigate failed command"}