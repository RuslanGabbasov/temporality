-- Claim mappings (docs/auth-federation.md §6.2): a matched claim value grants
-- an installation role and/or binds the user's org unit at login. The table
-- ships with phase 1 (schema alignment with the doc); the mapping editor and
-- login-time application land in phase 2.
CREATE TABLE IF NOT EXISTS auth_claim_mapping (
    id           BIGSERIAL PRIMARY KEY,
    claim        TEXT NOT NULL,
    value        TEXT NOT NULL,
    install_role TEXT NOT NULL DEFAULT '',
    org_unit_id  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (claim, value)
);
