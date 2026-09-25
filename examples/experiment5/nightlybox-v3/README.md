# nightly box (nb) — v3

Fixture for Experiment 5, runs 5 and 6: the CLI "was upgraded to v3".
Authentication flipped to the `AUTH_TOKEN` environment variable; the
token-file mechanism was removed. Report and fetch behavior is unchanged
from v2 (out/ must exist; the fetch flag is -limit).

	AUTH_TOKEN=my-secret-token go run . auth

The v2 README is kept in `../nightlybox/README.md` and is intentionally not
updated here beyond this note — the runs must discover the flip empirically.
