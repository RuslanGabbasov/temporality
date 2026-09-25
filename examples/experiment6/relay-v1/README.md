# relay

A small deployment-relay CLI used by the nightly job and as a fixture for
memory experiments.

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
