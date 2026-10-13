-- Installation-level key/value settings owned by the workspace store:
-- runtime-bootstrapped configuration that must survive restarts without
-- touching the environment (docs/ROADMAP.md — first-run onboarding).
-- Known keys:
--   secret_master_key  — auto-generated envelope-encryption key for builtin
--                        secrets when KERNEL_SECRET_MASTER_KEY is not set
--                        (the env var always wins)
--   sandbox_image      — digest-pinned sandbox image reference built from
--                        the UI when KERNEL_SANDBOX_IMAGE is not set
CREATE TABLE IF NOT EXISTS workspace_setting (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
