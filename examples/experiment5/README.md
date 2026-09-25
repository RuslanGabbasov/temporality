# Experiment 5 — long-horizon memory

Purpose: show memory accumulating and working over a longer horizon —
several independent experiences form, get recalled across runs, and exactly
one dies when the environment changes, while the others keep working.

The fixture is a small CLI (`nb`) with three independent, partially
mis-documented mechanisms:

- auth — README says `AUTH_TOKEN` env var; v2 actually reads `.token-file`;
  v3 (the upgrade) flips back to the env var.
- report — README says it prints to stdout; actually it requires an
  existing `out/` directory and writes `out/report.txt` (unchanged in v3).
- fetch — README says `--count`; actually `-limit` (unchanged in v3).

Project: `nightlybox`. Runs `nightly-20260925-01..06`, max_turns 10.

Reset between runs:

	rm -rf .sandbox/nightlybox && cp -R examples/experiment5/nightlybox .sandbox/nightlybox

For runs 5–6 (the v3 upgrade):

	rm -rf .sandbox/nightlybox && cp -R examples/experiment5/nightlybox-v3/. .sandbox/nightlybox/

## Schedule

1. `nightly-20260925-01` — auth task: investigate the auth discrepancy,
   test the AUTH_TOKEN hypothesis empirically, remember the conclusion.
2. `nightly-20260925-02` — report task: make `nb report` work, remember the
   undocumented requirement.
3. `nightly-20260925-03` — full nightly setup: auth + report end to end;
   should recall both prior knowledges.
4. `nightly-20260925-04` — fetch task: make `nb fetch` work against the
   documented `--count`, remember the actual flag.
5. `nightly-20260925-05` — v3 upgrade: auth+report+fetch; the recalled auth
   knowledge breaks, report/fetch knowledge still holds; remember the
   correction. Afterwards the operator invalidates the stale auth knowledge.
6. `nightly-20260925-06` — full nightly again: must recall the corrected
   auth knowledge plus report/fetch, and must NOT be offered the invalidated
   one.

Exact prompts are recorded in `docs/experiment5-long-horizon.md` together
with the observed event history.
