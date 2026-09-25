# Experiment 6 — competing experiences across two flips

Purpose: give the Experience Timeline a corpus where memory does more than
form, get reused and die once — here experiences **compete**, **supersede each
other in a chain**, **resurrect** (a dead claim returns in a new node) and
**go stale** (challenged but neither confirmed nor contradicted).

The fixture is a small CLI (`relay`) with three independent, partially
mis-documented mechanisms and TWO environment flips:

- auth — v1 reads the `API_KEY` env var (README's `--token` is wrong
  everywhere); v2 flips to `keys/key.txt`; v3 flips BACK to `API_KEY`.
  This yields the supersession chain K(a1) → K(a2) → K(a3), where K(a3)
  resurrects the v1 claim in a new, living knowledge node.
- push — requires the `CHANNEL` env var in every version (README says plain
  `relay push` works). Stable experience, repeatedly reused and validated.
- check — v1/v2 print `health: green` (exit 0); v3 prints
  `health: unknown (probe disabled)` and exits 2 — deliberately ambiguous, so
  the v3 run can neither confirm nor contradict the prior conclusion. The
  operator then challenges it: stale knowledge that is still offered, with a
  caution.

Project: `relay`. Runs `relay-20260925-01..09` (plus `02b`, `05b` retries),
max_turns 10–14.

Reset between phases:

	v1:  rm -rf .sandbox/relay && cp -R examples/experiment6/relay-v1 .sandbox/relay
	v2:  rm -rf .sandbox/relay && cp -R examples/experiment6/relay-v2 .sandbox/relay
	v3:  rm -rf .sandbox/relay && cp -R examples/experiment6/relay-v3 .sandbox/relay

Operator steps (recorded as human-operator events):

- after run 04 — invalidate K(a1) (v1 env auth), reason references K(a2);
- after run 07 — invalidate K(a2) (v2 key-file auth), reason references K(a3);
- after run 08 — challenge K(c1) (health check): v3's probe is disabled, the
  outcome neither confirms nor contradicts.

## Schedule (as executed)

| run | phase | task | outcome |
|---|---|---|---|
| 01 | v1 | auth discovery: test the documented `--token`, find the real mechanism, remember | a1 `/89` (turn_limit after remember) |
| 02 | v1 | push discovery | FAIL: turns spent on ephemeral /scratch, no remember |
| 02b | v1 | push discovery (retry) | b1 `/76` (9 turns) |
| 03 | v1 | full pipeline (auth + push), reuse | ✓ 8 turns, a1+b1 recalled |
| 04 | v2 | verify auth, find the flip | a2 `/54` (7 turns) |
| 05 | v2 | check discovery | FAIL: `command` sent as string → all run_command rejected (kernel fixed mid-experiment) |
| 05b | v2 | check discovery (retry) | c1 `/78` green probe (9 turns) |
| 06 | v2 | full pipeline, reuse | ✓ 11 turns; self-corrected the same argv mistake via `sh -c` |
| 07 | v3 | verify auth, find the second flip | a3 `/80` + c2 `/83` side observation (10 turns) |
| 08 | v3 | full pipeline; report what held and what didn't | ✓ 8 turns |
| 09 | v3 | full pipeline after invalidations | ✓ 7 turns; rejected challenged c1 explicitly |

## Expected timeline picture

	AUTH   a1 ●━━━━━━━━━━━╳        (invalidated after 04)
	                  ╲
	                   ╲→ a2 ●━━━━━━━╳   (invalidated after 07)
	                             ╲
	                              ╲→ a3 ●━━━━━━━━━  (alive; same claim as a1)
	PUSH   b1 ●━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  (alive, reused 4×)
	CHECK  c1 ●━━━━━━━━━━━━━━━◇───────────────────  (challenged after 08, offered with caution)

## Acceptance criteria (fixture №3)

Frozen corpus: `debugger/src/fixtures/experiment6-relay.json`, knowledge
ids: a1 = `relay-20260925-01/knowledge/89`, a2 = `relay-20260925-04/knowledge/54`,
a3 = `relay-20260925-07/knowledge/80`, b1 = `relay-20260925-02b/knowledge/76`,
c1 = `relay-20260925-05b/knowledge/78`, c2 = `relay-20260925-07/knowledge/83`.

- a1 appears in run 01, a2 in run 04, a3 in run 07 — all in the auth scope;
- lineage chain a1 → a2 → a3 is visible (inferred from invalidation reasons);
- a3's proposition claims the API_KEY env var (semantic resurrection of a1);
- b1 is reused in runs 03/06/08/09 and never dies;
- c1 is challenged after run 08 and still offered in run 09 (with caution);
- run 09 is offered a3 + b1 + c1 (+ c2, the model's own check correction)
  and neither a1 nor a2;
- run 07 also produced c2 (check flip noticed without operator input).
Full write-up: `docs/experiment6-competing-experiences.md`.
