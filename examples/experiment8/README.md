# Experiment 8 — long-horizon evolution

Purpose: give the Experience Timeline a corpus where knowledge evolves
across THREE versions of one CLI with a partial revert — the long-horizon
picture: resurrection (a mechanism that went away comes back), composite
notes whose halves die at different times, stale knowledge that keeps being
reinforced while half-wrong, and stable anchors that never die.

The fixture is a Go CLI (`lh`) with four mechanisms. The README is
byte-identical in every version and lies about login in all of them:

| mechanism | v1 | v2 | v3 | README says |
|---|---|---|---|---|
| login | `LH_TOKEN` env | `.lh-token` file in CWD | `LH_TOKEN` env (revert) | `lh login --token` — wrong in all versions |
| build | plain, no requirements | unchanged | unchanged | correct |
| deploy | `--env prod` flag required | `LH_STAGE` env (`--env` ignored) | unchanged from v2 | matches only v1 |
| report | writes `report.txt` | unchanged | `--format json` required, writes `report.json` | matches v1/v2 only |

Login is the resurrection axis (mechanism leaves in v2 and returns in v3),
deploy is the competition axis, report is the late-breaker, build is the
stable anchor.

Project: `lighthouse`. Runs `lighthouse-20260925-01..11`, max_turns 12.
Driver: `scripts/exp8/run_series.py` with the `phase-a`, `phase-b`,
`phase-b-NN`, `phase-c`, `phase-c-NN` specs (the per-run split spec files
exist so operator events could be recorded between individual runs).

Reset between phases:

	v1:  rm -rf .sandbox/lighthouse && cp -R examples/experiment8/lighthouse-v1 .sandbox/lighthouse
	v2:  rm -rf .sandbox/lighthouse && cp -R examples/experiment8/lighthouse-v2 .sandbox/lighthouse
	v3:  rm -rf .sandbox/lighthouse && cp -R examples/experiment8/lighthouse-v3 .sandbox/lighthouse

The workspace resets before EVERY run, so all reuse comes from knowledge,
never from leftover files.

Operator steps (recorded as human-operator events, see
`scripts/exp8/operator_event.py`):

- after run 04 — invalidate a1 `/101` (reason names a2 `/55`);
- after run 05 — challenge d1 `/83`: deploy half verified only on v1,
  report half still believed current;
- after run 06 — invalidate e1 `/43` (reason names e2 `/48`);
- after run 07 — invalidate d1 `/83` (reason names d2 `/56` AND e2 `/48`
  — two successors from one reason);
- after run 08 — invalidate a2 `/55` (reason names a3 `/41`);
- after run 10 — invalidate e2 `/48` (reason names e3 `/49`).

## Schedule (as executed)

| run | phase | task | outcome |
|---|---|---|---|
| 01 | v1 | login+build discovery | a1 `/101` + auto test/build (turn_limit after remember) |
| 02 | v1 | deploy+report discovery | d1 `/83` — composite note |
| 03 | v1 | full pipeline | e1 `/43` |
| 04 | v2 | verify login | a2 `/55` (.lh-token) |
| 05 | v2 | verify deploy | d2 `/56` (LH_STAGE) |
| 06 | v2 | full pipeline; what holds, what broke | e2 `/48` |
| 07 | v2 | full pipeline; conflicts → execution decides | h1 `/92` (harness quirks; turn_limit after remember) |
| 08 | v3 | verify login | a3 `/41` — LH_TOKEN is back (resurrection) |
| 09 | v3 | verify report | r2 `/37` (--format json) |
| 10 | v3 | full pipeline | e3 `/49` |
| 11 | v3 | final audit + supersession summary | c1 `/43` (0 uses — formed only) |

## Expected timeline picture

	LOGIN   a1  ●━━━━━━━━━━━╳                     (dies after 04)
	                 ╲ (a2: .lh-token)
	        a2        ●━━━━━━━━━━━╳               (dies after 08)
	                             ╲ (a3: LH_TOKEN back)
	        a3                      ●────────────  (alive — resurrection)
	DEPLOY  d1  ●━━━━━━━━━━━━━━━━━━━◇━━━━━━━╳     (challenged after 05, dies after 07)
	        d2            ●━━━━━━━━━━━━━━━━━━━━━  (alive)
	REPORT  r2                        ●──────────  (v3 --format json, alive)
	E2E     e1  ●━━━━━━━━━━╳                      (dies after 06)
	        e2             ●━━━━━━━━━━━━━╳        (dies after 10)
	        e3                           ●───────  (alive)
	HARNESS h1                 ●━━━━━━━━━━━━━━━━━  (stable anchor)
	TEST    auto ●━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  (confirmed; 10 uses)
	BUILD   auto  ●━━━━━━━━━━━━━━━━━━━━━━━━━━━━    (ONE deduped node `go build -o OUT .`; 7 uses)
	AUDIT   c1                              ●      (run 11 summary, formed only)

## Acceptance criteria (fixture №5)

Frozen corpus: `debugger/src/fixtures/experiment8-lighthouse.json`, knowledge
ids: a1 = `lighthouse-20260925-01/knowledge/101`, d1 = `…-02/knowledge/83`,
e1 = `…-03/knowledge/43`, a2 = `…-04/knowledge/55`, d2 = `…-05/knowledge/56`,
e2 = `…-06/knowledge/48`, h1 = `…-07/knowledge/92`, a3 = `…-08/knowledge/41`,
r2 = `…-09/knowledge/37`, e3 = `…-10/knowledge/49`, c1 = `…-11/knowledge/43`,
auto test = `auto/f9a3bc240d25d398a36c870e`, auto build =
`auto/eb2d265c3abc778ffbd624ee`.

- corpus shape: 834 events, 11 runs, 13 rows, 52 activation links;
- resurrection: a1 → a2 → a3 lineage chain; only a3 alive; a3's proposition
  restores the v1 mechanism (`LH_TOKEN`) while naming the dead `.lh-token`;
- composite death: d1 has two death records — contradicted (no successor),
  then archived with supersededBy = [d2, e2]; both lineage edges exist;
- pipeline chain: e1 → e2 → e3; only e3 alive;
- full lineage set: exactly six inferred edges;
- competition: d1 and d2 are both recalled in runs 06–07 (d1 recalled in
  runs 03–07, d2 in runs 06–11);
- stable anchors: h1 recalled in every run 08–11; auto go-test in all ten
  runs after appearance;
- kernel dedup: exactly one build auto row, canonical command
  `go build -o OUT .`, 7 activations;
- hygiene: run 11 recalls exactly the living claims (d2, h1, a3, r2, e3)
  plus auto go-test, none of the five dead notes; c1 has zero uses;
- population: 7 alive rows at run 11's start;
- lanes: test / build / report / end-to-end, with e1, e2, r2, e3 in report;
- forensic: a3 formed in run 08, three activations, all completed, no
  deaths; a2's death records the revert and names a3.

Full write-up: `docs/experiment8-long-horizon-evolution.md`.
