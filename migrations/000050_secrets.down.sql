ALTER TABLE workspace_agent DROP COLUMN IF EXISTS secrets;
DROP TABLE IF EXISTS workspace_secret_value;
DROP TABLE IF EXISTS workspace_secret;
