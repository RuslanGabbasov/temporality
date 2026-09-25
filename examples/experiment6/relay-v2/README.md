# relay — v2

The relay CLI was upgraded to v2. The v1 README below is kept for reference
and is intentionally not updated beyond this note — the upgrade notes were
lost, so verify the actual behavior empirically.

## Authentication

The nightly job authenticates with a token flag:

	relay auth --token my-secret-token

## Push

Publishing the build is plain:

	relay push

## Health check

The health probe prints its report to stderr:

	relay check

---

Parts of this README are stale; verify the actual behavior empirically before
relying on it.
