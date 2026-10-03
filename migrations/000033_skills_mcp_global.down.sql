-- Revert skills and MCP servers to project-bound. Rows created while the
-- assets were global have no project; the column is restored as NOT NULL
-- with a synthetic fallback because the original binding data is gone.

UPDATE workspace_skill SET project_id = 'my-project' WHERE project_id IS NULL;
UPDATE workspace_mcp_server SET project_id = 'my-project' WHERE project_id IS NULL;

ALTER TABLE workspace_skill ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE workspace_mcp_server ALTER COLUMN project_id SET NOT NULL;
