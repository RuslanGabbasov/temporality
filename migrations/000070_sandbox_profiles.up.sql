-- Sandbox profile registry (docs/sandbox-profiles.md §3.1): builtin catalog
-- rows are seeded by the kernel at boot; custom rows are created through the
-- API. image stays empty until the profile is built and digest-pinned.
CREATE TABLE IF NOT EXISTS workspace_sandbox_profile (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    image TEXT NOT NULL DEFAULT '',
    toolchains TEXT[] NOT NULL DEFAULT '{}',
    memory TEXT NOT NULL DEFAULT '',
    cpus TEXT NOT NULL DEFAULT '',
    scratch_size TEXT NOT NULL DEFAULT '',
    tmp_size TEXT NOT NULL DEFAULT '',
    cache_env TEXT[] NOT NULL DEFAULT '{}',
    builtin BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The restricted/standard/privileged triple was a dead field: stored and
-- shown but never read. Migrate every card to the empty default (the
-- installation image); profile ids arrive with the registry.
UPDATE workspace_agent SET sandbox_profile = ''
WHERE sandbox_profile IN ('restricted', 'standard', 'privileged');
