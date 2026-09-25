# relay — v3

The relay CLI was upgraded again (v3). The README below is the stale v1 text
kept for reference — verify the actual behavior empirically.

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
