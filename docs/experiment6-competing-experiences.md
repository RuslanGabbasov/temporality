# Experiment 6 — competing experiences across two flips

The third acceptance corpus for the Experience Timeline. Experiment 4 proved
single-experience formation around a rejected path; Experiment 5 proved
accumulation and selective death. This corpus adds what neither could show:

- a **supersession chain** a1 → a2 → a3 across TWO environment flips, where
  a3 **resurrects** the a1 claim semantically (env-var auth → key file →
  env-var auth again) in a new, living knowledge node;
- **stale knowledge**: a claim the operator challenges but neither confirms
  nor contradicts — it must stay in recall with a caution instead of being
  retired.

Fixture: `examples/experiment6/relay` — a Go CLI `relay` with three
independent, partially mis-documented mechanisms (auth, push, check) and two
flips (v1 → v2 → v3). Project: `relay`. Fixture:
`debugger/src/fixtures/experiment6-relay.json` (966 events, 11 runs).

## Run matrix

| run | task | hints offered (→used) | new knowledge | turns | commands ok/fail |
|---|---|---|---|---|---|
| 01 | v1 auth discovery | — | `/89` a1: API_KEY env auth, `--token` rejected | 9† | 16/0 |
| 02 | v1 push discovery | `89` | — | — | 17/0, but no `remember` → turn_limit |
| 02b | v1 push discovery | `89` | `/76` b1: CHANNEL required | 9 | 14/0 |
| 03 | v1 full pipeline | `89`,`76` | — | 8 | 11/0 |
| 04 | v2 verify auth | `89`,`76` | `/54` a2: key-file auth | 7 | 9/0 |
| 05 | v2 check discovery | `76`,`54` | — | — | 2/12: model sent `command` as string → all `run_command` rejected |
| 05b | v2 check discovery | `76`,`54` | `/78` c1: green probe, exit 0 | 9 | 14/0 |
| 06 | v2 full pipeline | `76`,`54`,`/78` | — | 11 | 11/0 |
| 07 | v3 verify auth | `76`,`54`,`/78` | `/80` a3: API_KEY again; `/83` c2: probe disabled | 10 | 14/0 |
| 08 | v3 full pipeline | `76`,`/78`,`/80`,`/83` | — | 8 | 11/0 |
| 09 | v3 full pipeline (post-invalidation) | `76`,`/80`,`/83`,`/78` | `/51` bonus: `go run` masks exit codes | 7 | 7/0 |

† turn_limit reached after `remember` had fired — recorded as a failure lane.

Operator steps (recorded as `human-operator` events):

- after run 04 — invalidate `/89` (a1), reason references `/54` (a2);
- after run 07 — invalidate `/54` (a2), reason references `/80` (a3);
- after run 08 — challenge `/78` (c1): the v3 probe is disabled, so run 08's
  outcome neither confirms nor contradicts the green-probe conclusion.

## Observed behavior

1. **The supersession chain forms.** Two flips produced three auth
   experiences; the fold reconstructs lineage a1 → a2 → a3 from the
   invalidation reasons alone (each reason names the successor). All three
   share the `auth` scope lane.

2. **Semantic resurrection.** a3 (`/80`, proposed) re-asserts a1's exact
   mechanism (non-empty `API_KEY` env var) after a1 died invalidated. The
   timeline shows a dead claim returning as a new living node — not a
   revival of the dead node.

3. **Stale ≠ dead.** The challenged c1 (`/78`) stayed in recall: run 09 was
   offered it alongside the living a3/b1/c2, with the runtime's caution
   ("challenged knowledge; inspect the history before relying on it").
   Run 09's answer explicitly listed it under "notes I chose NOT to trust" —
   the caution changed how the model treated the hint, and direct execution
   decided. Invalidated a1/a2 were not offered at all.

4. **The model corrected the record itself.** Run 07 was asked only about
   auth but noticed the check flip as a side observation and stored c2
   (`/83`) — the CHECK scope lane ends up with two competing claims without
   any operator involvement.

5. **Failure lanes are ordinary history.** Run 02 spent its turns on an
   ephemeral `/scratch` file and never called `remember`; run 05 fed
   `command` as a string and burned 12 tool calls. Both stay in the corpus
   as turn-limit lanes (see the kernel fix below).

## Kernel fix recorded mid-experiment

Run 05 failed because the model sent `run_command` arguments with `command`
as a string; the kernel retried the deterministic schema violation five
times and reported each failure as "effect may be uncertain", which the
model could not act on. Fix (`d329430`): `InvalidToolArguments` is now a
non-retryable `ApplicationError`, and the turn message says the call was
rejected before execution with no side effects and names the expected shape.
Run 06 then hit the same mistake once, self-corrected via `sh -c`, and
completed — the failure became recoverable within a single turn.

## Fixture view notes

- Scope lanes: `auth` holds a1/a2/a3 (a2, a3 also via lineage inheritance),
  `push` holds b1, `check` holds c2. c1 lands in `end-to-end` with secondary
  scopes — its proposition contrasts all three commands, matching the
  Experiment 5 convention for cross-mechanism claims.
- This corpus forced one fold refinement: mechanism matching now reads
  backtick code spans first (a quoted `relay auth` is a mechanism; the word
  "build" inside prose like "In this relay CLI build" is not), with a
  whole-text fallback for claims that quote no commands (nightly K82).
- Acceptance assertions live in `debugger/src/experience.fixtures.test.ts`
  (`Experiment 6 fixture — competing experiences across two flips`).
