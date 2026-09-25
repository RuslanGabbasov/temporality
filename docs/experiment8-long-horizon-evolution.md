# Experiment 8 — long-horizon evolution

The fifth acceptance corpus for the Experience Timeline. Experiments 4–7
proved formation, reuse, selective death, supersession chains, competing
pairs and scale. This corpus runs one CLI through THREE versions with a
partial revert, which surfaces what none of the earlier corpora could:

- **full-circle resurrection**: login flips `LH_TOKEN` env → `.lh-token`
  file → `LH_TOKEN` env again. The v1 note dies into the v2 note, the v2
  note dies into the v3 note — and the v3 note restores the v1 mechanism.
  Resurrection is a NEW living node, not a revival of the dead one;
- **composite death**: the v1 deploy+report note (`/83`) bundles two
  mechanisms with different fates. Its deploy half breaks in v2 while its
  report half stays true for the whole of v2. The operator challenges it,
  then invalidates it with a reason naming TWO successors — producing two
  lineage edges from a single event;
- **a stale-reinforcement window**: for four runs (04–07) the composite
  note kept entering recall with a wrong deploy half — memory truth lags
  environment truth until evidence or an operator catches up;
- **kernel dedup validated**: every `go build -o <dir> .` invocation
  canonicalizes to `go build -o OUT .` and folds into ONE auto node with 7
  activations (in the forge corpus, pre-fix, near-identical build variants
  were separate rows crowding the hint budget);
- **the hint-budget ranking fix holds at scale**: runs 09–11 recall every
  living claim; after each invalidation dead knowledge never leaks back
  into recall.

Fixture: `examples/experiment8/lighthouse-v1|v2|v3` — a Go CLI `lh` with
four mechanisms, three of which change across versions (login twice, deploy
once, report once; build is the stable anchor). The README is byte-identical
in all three versions and wrong about login everywhere — a fixed lie the
agent must empirically correct in every generation. Project: `lighthouse`.
Fixture: `debugger/src/fixtures/experiment8-lighthouse.json` (834 events,
11 runs, 13 rows, 52 activation links).

## Run matrix

| run | task | hints used | new knowledge | outcome |
|---|---|---|---|---|
| 01 | v1 login+build discovery | — | a1 `/101` (LH_TOKEN env); auto `go test`, `go build` | turn_limit† (13 turns) |
| 02 | v1 deploy+report discovery | a1 + 2 auto | d1 `/83` — composite: deploy `--env` + report.txt | completed (10) |
| 03 | v1 full pipeline | a1, d1 + auto | e1 `/43` — v1 pipeline | completed (6) |
| 04 | v2 verify login | a1, d1, e1 + 2 auto | a2 `/55` — `.lh-token` file | completed (9) |
| 05 | v2 verify deploy | d1, e1, a2 + 2 auto | d2 `/56` — `LH_STAGE` env | completed (8) |
| 06 | v2 full pipeline | d1, e1, a2, d2 + 2 auto | e2 `/48` — v2 pipeline | completed (6) |
| 07 | v2 full pipeline (conflicts → execution decides) | a2, d2, e2, d1 + 2 auto | h1 `/92` — harness quirks | turn_limit† (13 turns) |
| 08 | v3 verify login | a2, d2, e2, h1 + 2 auto | a3 `/41` — `LH_TOKEN` back (resurrection) | completed (6) |
| 09 | v3 verify report | d2, e2, h1, a3 + 2 auto | r2 `/37` — `--format json` | completed (5) |
| 10 | v3 full pipeline | d2, e2, h1, a3, r2 + 2 auto | e3 `/49` — v3 pipeline | completed (7) |
| 11 | final audit + supersession summary | d2, h1, a3, r2, e3 + auto test | c1 `/43` — supersession summary | completed (7) |

† forced finale at the turn budget, after `remember` had fired.

Operator steps (recorded as `human-operator` events):

- after run 04 — invalidate a1 (`/101`), reason names a2 (`/55`);
- after run 05 — challenge d1 (`/83`): "deploy half was verified only on
  the v1 build … report half still believed current";
- after run 06 — invalidate e1 (`/43`), reason names e2 (`/48`);
- after run 07 — invalidate d1 (`/83`): "deploy half is obsolete on v2
  (--env ignored, LH_STAGE required, see …/56); superseded pair with the v2
  pipeline note …/48" — one reason, two named refs, two lineage edges;
- after run 08 — invalidate a2 (`/55`): "v3 reverted login to the LH_TOKEN
  env var; token-file auth is gone", reason names a3 (`/41`);
- after run 10 — invalidate e2 (`/48`), reason names e3 (`/49`).

## Observed behavior

1. **Resurrection is full-circle and honest.** a1 (v1: `LH_TOKEN`) → a2
   (v2: `.lh-token`) → a3 (v3: `LH_TOKEN` again — its proposition opens
   with "v3 is a partial revert back to the v1 mechanism" and names both
   the env var and the dead file). The fold shows the chain
   a1→a2→a3 with only a3 alive: the dead notes keep their history, the
   restored truth lives in a new node. Knowledge ids never get reused.

2. **Composite death happens in two steps.** d1's challenge (after 05)
   contradicts without replacing — `supersededBy: []`. Its invalidation
   (after 07) names d2 AND e2, so `supersededBy: [d2, e2]` and two lineage
   edges (`/83→/56`, `/83→/48`) originate from one event. The reason text
   is the provenance: the deploy half died into the dedicated deploy
   correction, the note as a whole into the v2 pipeline.

3. **The stale-reinforcement window is visible.** From run 04 (the v2
   reset) through run 07, d1 kept entering recall while its deploy half was
   already wrong. The pipeline runs 06/07 exposed the conflict in
   execution ("where you hold conflicting notes, test and let execution
   decide"), and the operator retired the note only after 07. Meanwhile
   the report half of the same note was still true — the challenge reason
   says so explicitly. Partial correctness of a composite note is
   representable, but only the operator (or re-derivation, as r2 in run 09)
   can split it.

4. **Report truth was re-derived, not resurrected.** When v3 broke report
   (`--format json`), no lineage edge pointed at the new note r2 — it
   formed as fresh knowledge in run 09. Supersession edges exist only
   where an invalidation reason names a successor; re-derivation after
   death leaves no edge. Both shapes are now pinned by tests.

5. **Stable mechanisms anchor the timeline.** The auto `go test ./...`
   observation was confirmed after run 01 and recalled in all ten later
   runs; the harness-quirks note h1 was recalled in every run after its
   appearance (08–11). These are the continuous activation bands of the
   corpus.

6. **Kernel dedup collapses the build variants.** All eight `go build -o
   <dir> .` invocations across the corpus folded into the single auto node
   `auto/eb2d265c…` with the canonical command `go build -o OUT .` (7
   activations). The forge corpus's near-identical build rows — the main
   budget polluter — no longer exist as separate nodes.

7. **The hint budget serves claims first.** With the ranking fix live
   (claims above observations at equal match strength), runs 09–11
   recalled every living claim; run 11 received exactly the five living
   claims plus the go-test observation and none of the five dead ones.
   Selective death stays hygienic across a three-version horizon.

## Fixture view notes

- Scope lanes: `test` (auto go-test), `build` (auto go-build), `report`
  (e1, e2, r2, e3), `end-to-end` (a1, d1, a2, d2, h1, a3, c1). The
  correction notes enumerate several mechanisms each, so most claims land
  in `end-to-end` with their specific mechanisms (login, deploy, …) as
  secondary scopes — coarser lanes than forge's, with the detail preserved
  in the secondary list.
- Lineage: six inferred edges — a1→a2, a2→a3, d1→d2, d1→e2, e1→e2, e2→e3.
- Forensic: a3 forms in run 08 (evidence + four commands), activates three
  times, all in completed runs, and has no deaths. d1 carries TWO death
  records — contradicted (no successor) then archived (two successors).
- Acceptance assertions live in `debugger/src/experience.fixtures.test.ts`
  (`Experiment 8 fixture — long-horizon evolution, resurrection, composite
  death`).

Full fixture mechanics and schedule: `examples/experiment8/README.md`.
