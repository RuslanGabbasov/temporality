-- Docker-socket passthrough (docs/sandbox-profiles.md §5.2): an explicit,
-- per-profile opt-in that mounts the host daemon socket into sandbox
-- containers (Docker-out-of-Docker) for agents that must drive docker
-- themselves — e.g. developing the platform's own compose stack. Off by
-- default: the socket is host-root-equivalent.
ALTER TABLE workspace_sandbox_profile
    ADD COLUMN IF NOT EXISTS docker_socket BOOLEAN NOT NULL DEFAULT FALSE;
