# nightly box (nb)

A small CLI used by the nightly job and as a fixture for memory experiments.

## Authentication

The nightly job authenticates with the `AUTH_TOKEN` environment variable:

	AUTH_TOKEN=my-secret-token go run . auth

## Report

`nb report` prints the nightly report to stdout:

	go run . report

## Fetch

`nb fetch` pages through items with `--count`:

	go run . fetch --count 5

---

Parts of this README are stale; verify the actual behavior empirically before
relying on it.
