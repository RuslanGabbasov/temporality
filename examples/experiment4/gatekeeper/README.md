# gatekeeper

A tiny access-control CLI used as a fixture for memory experiments.

## Authentication

The current version (v2) authenticates via a `.token-file` in the working directory:

	echo my-secret-token > .token-file
	go run .

Expected output: `access granted`.

Without the file the CLI exits with `access denied: no token file`.

## History

- v1 (legacy): authenticated via the `AUTH_TOKEN` environment variable. See `docs/legacy.md`.
- v2 (current): token-file only.

Docs may be stale; verify the actual behavior empirically before relying on it.
