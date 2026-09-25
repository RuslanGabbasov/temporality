# Experiment 7 — reinforcement and scale

Purpose: give the Experience Timeline a corpus at the scale where lane
structure — not individual markers — carries the picture: 20 knowledge rows,
110 activations, 16 runs. Two things this corpus adds over experiments 4–6:

- **reinforcement loops**: mechanisms that never change must be recalled in
  EVERY later run (login: 15 uses) — the "agent stops re-deriving what it
  knows" signature;
- **a competing pair that coexists**: b2 appears while challenged b1 is
  still alive; the operator resolves the pair only after run 12.

The fixture is a Go CLI (`forge`) with five mechanisms, two of which are
stable and three change in the v2 upgrade:

- login — reads the `FORGE_TOKEN` env var in both versions (README's
  `--token` is wrong everywhere). Stable → reinforcement row.
- publish — requires a non-empty `CHANNEL` env var in both versions (README
  says plain `forge publish`). Stable → reinforcement row.
- build — v1: always full rebuild, `CACHE_DIR` ignored; v2: non-empty
  `CACHE_DIR` writes a cache marker ("cache v2"). The v1 note gets
  challenged BEFORE the flip is even verified, so the pair b1/b2 coexists
  as competing live claims for four runs.
- pack — v1 writes `dist.tar.gz`; v2 writes `dist.zip` (matching what the
  README always claimed).
- sign — v1 reads `SIGN_KEY`; v2 renames it to `SIGNING_KEY` (README's
  `--key` is wrong in both).

Project: `forge`. Runs `forge-20260925-01..14` (plus `08b`, `10b` retries),
max_turns 10–14. Driver: `scripts/exp7/run_series.py` with the
`phase-a/b1/b1-retry/b2/b3` specs.

Reset between phases:

	v1:  rm -rf .sandbox/forge && cp -R examples/experiment7/forge-v1 .sandbox/forge
	v2:  rm -rf .sandbox/forge && cp -R examples/experiment7/forge-v2 .sandbox/forge

Operator steps (recorded as human-operator events, see
`scripts/exp7/operator_event.py`):

- after run 07 — challenge K(b1): the cache conclusion was only verified on
  the v1 build and an upgrade is landing;
- after run 10b — invalidate K(k1) (reason names K(k2)); invalidate K(s1)
  (reason names K(s2));
- after run 12 — invalidate K(b1) (reason names K(b2)); invalidate K(e1)
  (reason names K(e2)).

## Schedule (as executed)

| run | phase | task | outcome |
|---|---|---|---|
| 01 | v1 | login discovery | a1 `/99` + 3 auto (turn_limit after remember) |
| 02 | v1 | publish discovery | p1 `/90` (turn_limit after remember) |
| 03 | v1 | build discovery | b1 `/89` |
| 04 | v1 | pack discovery | k1 `/69` |
| 05 | v1 | sign discovery | s1 `/104` |
| 06 | v1 | full pipeline, reuse | e1 `/102` |
| 07 | v1 | full pipeline, reuse | h1 `/96` (harness facts) |
| 08 | v2 | verify build | FAIL: turn_limit, conclusion in answer only |
| 09 | v2 | verify pack | k2 `/99` |
| 10 | v2 | verify sign | FAIL: turn_limit, conclusion in answer only |
| 08b | v2 | verify build (retry) | b2 `/118` — the competing pair is now both alive |
| 10b | v2 | verify sign (retry) | s2 `/88` |
| 11 | v2 | full pipeline; what holds, what broke | e2 `/105` |
| 12 | v2 | full pipeline; conflicts → execution decides | e3 `/112` (turn_limit after remember) |
| 13 | v2 | post-invalidation pipeline | dup `/99`: pack re-derived (budget displacement!) |
| 14 | v2 | final audit | c91 `/91`, c94 `/94` |

## Expected timeline picture

	LOGIN    a1  ●━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  (15 activations, never dies)
	PUBLISH  p1  ●━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  (14 activations)
	BUILD    b1  ●━━━━━━━━━━━━━━━◇━━━━━━━━━━━━━╳          (challenged after 07, dies after 12)
	              ╲ (b2 appears in 08b while b1 is alive)
	         b2      ●━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
	PACK     k1  ●━━━━━━━━━━━━━━━━━━━━━━━╳                 (dies after 10b)
	         k2                    ●──────────────────     (alive but NEVER recalled: budget)
	SIGN     s1  ●━━━━━━━━━━━━━━━━━━╳                     (dies after 10b)
	         s2                         ●─────────────     (alive but never recalled)
	E2E      e1  ●━━━━━━━━━━━━━━━━━━━━━━━━━━╳             (dies after 12)
	         e2                              ●─────────    (never recalled)
	         e3                               ●────────    (never recalled)
	         dup                               ●──────     (run 13 re-derives pack)

## Acceptance criteria (fixture №4)

Frozen corpus: `debugger/src/fixtures/experiment7-forge.json`, knowledge
ids: a1 = `forge-20260925-01/knowledge/99`, p1 = `…-02/knowledge/90`,
b1 = `…-03/knowledge/89`, k1 = `…-04/knowledge/69`, s1 = `…-05/knowledge/104`,
e1 = `…-06/knowledge/102`, h1 = `…-07/knowledge/96`, b2 = `…-08b/knowledge/118`,
k2 = `…-09/knowledge/99`, s2 = `…-10b/knowledge/88`, e2 = `…-11/knowledge/105`,
e3 = `…-12/knowledge/112`, dup = `…-13/knowledge/99`, c91 = `…-14/knowledge/91`,
c94 = `…-14/knowledge/94`.

- corpus shape: 1884 events, 16 runs, 20 rows, 110 activation links;
- reinforcement: a1 and the auto go-test row are used in all 15 runs after
  appearance;
- the build pair coexists: b2 appears in 08b while b1 is alive, b1 dies
  only after run 12 (b2.firstAt < b1.terminal.at), b1's lifecycle keeps the
  run-07 challenge point;
- lineage: four inferred edges b1→b2, k1→k2, s1→s2, e1→e2;
- selective death: b1/k1/s1/e1 invalidated; a1/p1/b2/k2/s2/e2/e3 alive;
- runs 13/14 receive none of the dead claims and all of a1/p1/b2;
- budget displacement (observed, not desired): k2/s2/e2/e3 have ZERO
  activations; run 13 re-derives pack and records a duplicate claim;
- lanes: login/publish/build/pack/sign/test/end-to-end with the v1/v2
  pairs sharing lanes.

Full write-up: `docs/experiment7-reinforcement-and-scale.md`.
