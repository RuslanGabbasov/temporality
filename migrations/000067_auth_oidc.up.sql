-- Auth federation phase 1 (docs/auth-federation.md §7): IdP config, sessions
-- and one-time OIDC request state. One config row per installation (id=1).
CREATE TABLE IF NOT EXISTS auth_idp_config (
    id                INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    enabled           BOOLEAN NOT NULL DEFAULT FALSE,
    issuer            TEXT NOT NULL DEFAULT '',
    client_id         TEXT NOT NULL DEFAULT '',
    client_secret_ref TEXT NOT NULL DEFAULT '',
    display_name      TEXT NOT NULL DEFAULT 'SSO',
    scopes            TEXT NOT NULL DEFAULT 'openid profile email',
    username_claim    TEXT NOT NULL DEFAULT 'preferred_username',
    email_claim       TEXT NOT NULL DEFAULT 'email',
    groups_claim      TEXT NOT NULL DEFAULT 'groups',
    default_role      TEXT NOT NULL DEFAULT 'reader',
    jit_enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    redirect_base     TEXT NOT NULL DEFAULT '',
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_session (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES workspace_user(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL,
    last_seen   TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    ip          TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_auth_session_user ON auth_session(user_id);
CREATE INDEX IF NOT EXISTS idx_auth_session_expires ON auth_session(expires_at);

-- One-time state/nonce/PKCE verifier for the authorization-code flow.
-- Deleted on read; stale rows are purged by the hourly sweeper.
CREATE TABLE IF NOT EXISTS auth_request (
    state       TEXT PRIMARY KEY,
    nonce       TEXT NOT NULL,
    verifier    TEXT NOT NULL,
    redirect_uri TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_auth_request_created ON auth_request(created_at);
