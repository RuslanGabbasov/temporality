# forge

A small release-pipeline CLI used by the nightly job and as a fixture for
memory experiments.

## Login

The CI job logs in with a token flag:

	forge login --token my-token

## Build

Builds are cached automatically; set `CACHE_DIR` to reuse artifacts across
runs:

	CACHE_DIR=.cache forge build

## Pack

Packing produces a zip artifact:

	forge pack

## Sign

Artifacts are signed with a key flag:

	forge sign --key my-key

## Publish

Publishing is plain:

	forge publish

---

Parts of this README are stale; verify the actual behavior empirically before
relying on it.
