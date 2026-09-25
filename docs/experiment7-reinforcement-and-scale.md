# Experiment 7 — reinforcement and scale

The fourth acceptance corpus for the Experience Timeline. Experiments 4–6
proved formation, reuse, selective death, supersession chains, resurrection
and staleness on small corpora (3–6 experiences, up to 11 runs). This corpus
adds what none of them could show:

- **scale**: 20 knowledge rows, 110 activations, 16 runs, 1884 events —
  enough rows that lane structure, not individual markers, carries the
  picture;
- **reinforcement loops**: stable experiences recalled in EVERY later run
  (login and the auto build/test observations: 15 uses each);
- **a competing pair that coexists**: b2 appears while challenged b1 is
  still alive, and the operator resolves the competition only four runs
  later;
- **the hint-budget finding**: the kernel offers a fixed budget of knowledge
  hints; at this scale stable auto observations crowd the corrected v2
  claims out of recall entirely, and run 13 re-derives pack semantics from
  scratch and records a duplicate. This is the corpus's main kernel feed.

Fixture: `examples/experiment7/forge` — a Go CLI `forge` with five
mechanisms, three of which change in the v2 upgrade. Project: `forge`.
Fixture: `debugger/src/fixtures/experiment7-forge.json` (1884 events,
16 runs).

## Run matrix

| run | task | hints used | new knowledge | outcome |
|---|---|---|---|---|
| 01 | v1 login discovery | — | `/99` a1: FORGE_TOKEN env auth; auto `go build` ×2, `go test` | turn_limit† |
| 02 | v1 publish discovery | a1 + 3 auto | `/90` p1: CHANNEL required | turn_limit† |
| 03 | v1 build discovery | a1 + 3 auto | `/89` b1: no cache layer, CACHE_DIR ignored | completed |
| 04 | v1 pack discovery | a1 + 3 auto | `/69` k1: dist.tar.gz, not zip | completed |
| 05 | v1 sign discovery | a1 + 3 auto | `/104` s1: SIGN_KEY env, `--key` ignored | completed |
| 06 | v1 full pipeline | a1 + 3 auto | `/102` e1: complete v1 pipeline | completed |
| 07 | v1 full pipeline (reuse) | a1 + 3 auto | `/96` h1: harness facts (`sh -c`, ephemeral /scratch) | completed |
| 08 | v2 verify build | (budget full, see below) | — | turn_limit: conclusion in answer, no `remember` |
| 08b | retry | 8 (incl. k1, s1) | `/118` b2: cache layer landed | completed |
| 09 | v2 verify pack | 8 | `/99` k2: dist.zip | completed |
| 10 | v2 verify sign | 8 | — | turn_limit: conclusion in answer, no `remember` |
| 10b | retry | 8 | `/88` s2: SIGNING_KEY rename | completed |
| 11 | v2 full pipeline | 8 | `/105` e2: corrected v2 pipeline | completed |
| 12 | v2 full pipeline (conflicts → execution decides) | 8 | `/112` e3: v2 semantics consolidated | turn_limit† |
| 13 | post-invalidation pipeline | 8 (no dead, no k2!) | `/99` dup: pack re-derived — the displacement symptom | completed |
| 14 | final audit | 8 | `/91` c91: sign/pack supersede map; `/94` c94: /scratch ephemerality | completed |

† turn budget reached after `remember` had fired.

Operator steps (recorded as `human-operator` events):

- after run 07 — challenge `/89` (b1): the cache conclusion was only verified
  on the v1 build and an upgrade is landing;
- after run 10b — invalidate `/69` (k1), reason names `/99` (k2); invalidate
  `/104` (s1), reason names `/88` (s2);
- after run 12 — invalidate `/89` (b1), reason names `/118` (b2); invalidate
  `/102` (e1), reason names `/105` (e2).

## Observed behavior

1. **Reinforcement is total for stable experiences.** a1 (login) and the
   auto `go build`/`go test` observations were recalled in all 15 runs after
   their appearance; p1 (publish) in 14. On the timeline these rows are
   continuous activation bands — the visual signature of "the agent stops
   re-deriving what it already knows".

2. **The competing build pair coexists for four runs.** b2 (`/118`) appeared
   in 08b while b1 (`/89`) was challenged-but-alive; both stayed non-terminal
   through runs 09–12 (the spec explicitly told run 12: "where you hold
   conflicting notes, test and let execution decide"). The operator resolved
   the pair only after run 12. The fold shows b2.firstAt < b1.terminal.at —
   overlap, not replacement.

3. **Selective death stays hygienic at scale.** After the invalidations,
   runs 13/14 received exactly the living set (a1, p1, b2 + 5 auto) and none
   of the dead v1 claims. Dead knowledge did not leak into recall.

4. **The hint budget displaces corrected knowledge — the main finding.**
   The kernel offers a fixed budget of 8 knowledge hints. Five stable auto
   observations (three of them near-identical `go build -o <dir> .` variants)
   occupied most of it in every v2 run. Result: k2, s2, e2 and e3 were NEVER
   recalled (used=0 across the whole corpus); run 13 — asked to "use what
   you already know" — silently re-derived pack semantics and recorded a
   DUPLICATE of k2 (`forge-20260925-13/knowledge/99`). Correct-but-unhinted
   knowledge is operationally dead: the memory is written but never read.
   Kernel follow-up (P0 candidate): cap auto/observation nodes per budget
   and/or rank `kind=claim` above `kind=observation` in KnowledgeHints.

5. **Challenged knowledge was also displaced.** b1 stayed challengable in
   theory but never made the 8-slot budget after 08b — unlike Experiment 6's
   c1, which was offered with a caution. Displacement does not care about
   state, only about rank.

6. **Failure/retry lanes are ordinary history.** Runs 08 and 10 established
   their conclusions in the final answer but hit the turn budget before
   `remember`; the 08b/10b retries (per the exp5/6 convention) made the
   durable record explicit.

## Fold refinements recorded by this corpus

The forge corpus forced the vocabulary and scope fold to get considerably
more literal, because agents quote far more than they claim about:

- **shell-wrapped argv**: `sh -c` script bodies are tokenized (operators
  become standalone tokens) and fed to the segmenter — the script carries
  the real mechanisms;
- **operator runs**: any run of `;&|>` chars splits segments (a `2>&1`
  redirect used to glue an entire echo line into one segment, leaking
  echo'd PROSE into the vocabulary — `flags`, `forge` itself);
- **shell builtins**: `exit`, `unset` & co. own no mechanism (before, the
  wordTail after a non-builtin head leaked `go`, `sign_key`);
- **bare env names**: ALL_CAPS tokens in argv (`env -u SIGN_KEY SIGNING_KEY
  …`) are environment names, never mechanisms (`signing_key` leak);
- **toolchain heads**: `go` means something only in head position;
- **subject frequency**: a thorough claim enumerates related behaviours
  ("related v1 behaviors: sign requires…, publish requires…"), and
  verification prose (`go build -o …`) routinely precedes the actual
  finding — so the subject of a claim is its most-mentioned mechanism, not
  the first one quoted. Explicit step enumerations / "end to end" wording
  still force the end-to-end lane.

## Fixture view notes

- Scope lanes: `login` (a1), `publish` (p1), `build` (b1, b2, h1, c94 + 4
  auto), `pack` (k1, k2), `sign` (s1, s2, c91), `test` (auto go-test),
  `end-to-end` (e1, e2, e3, dup).
- Lineage: four inferred edges k1→k2, s1→s2, b1→b2, e1→e2 from the
  invalidation reasons.
- Acceptance assertions live in `debugger/src/experience.fixtures.test.ts`
  (`Experiment 7 fixture — reinforcement, competing pair, scale`). The
  displacement assertions pin the OBSERVED behaviour (used=0), not the
  desired one — fixing the kernel will require updating them deliberately.
