-- Agent secrets (docs/secrets-management.md).
-- workspace_secret holds secret metadata only: name, provider, org binding.
-- Values never live in this table: workspace_secret_value stores the
-- envelope-encrypted payload (AES-256-GCM) keyed by KERNEL_SECRET_MASTER_KEY,
-- which exists only inside the agent-kernel process.
--
-- provider=builtin reads the encrypted value from workspace_secret_value;
-- env reads the process environment (local development); external providers
-- (Infisical, Vault) arrive in Phase 2 and will use external_ref.

CREATE TABLE IF NOT EXISTS workspace_secret (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    provider     TEXT NOT NULL DEFAULT 'builtin' CHECK (provider IN ('builtin', 'env')),
    external_ref TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    org_unit_id  TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Names are the resolution key for secret:// references, so they must be
-- unique across the installation even when secrets live in different units.
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_secret_name ON workspace_secret(name);

CREATE TABLE IF NOT EXISTS workspace_secret_value (
    secret_id  TEXT PRIMARY KEY REFERENCES workspace_secret(id) ON DELETE CASCADE,
    ciphertext BYTEA NOT NULL,
    nonce      BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workspace_secret_org ON workspace_secret(org_unit_id);

-- Secrets granted to an agent: resolved into the sandbox environment at the
-- execution boundary, never shown to the model (docs/secrets-management.md §4).
ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS secrets JSONB NOT NULL DEFAULT '[]';
