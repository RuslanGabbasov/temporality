-- Per-server max tool-call timeout (docs: the MCP timeout_ms convention —
-- the caller states its wait budget; the server caps it). 0 = the 10-minute
-- default cap from the kernel. Set on the server card in the debugger.
ALTER TABLE workspace_mcp_server ADD COLUMN IF NOT EXISTS timeout_ms INT NOT NULL DEFAULT 0;
