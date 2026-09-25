# Experiment 5 — long-horizon memory

Memory accumulating, being reused and selectively dying across a six-run
series on the live stack. This is the §14.6 step of the phase plan: after
Experiment 4 proved single-experience formation around a rejected path, this
experiment tests the longer horizon — several independent experiences,
repeated recall, and an environment flip that kills exactly one of them.

Fixture: `examples/experiment5/nightlybox` — a Go CLI `nb` with three
independent, partially mis-documented mechanisms (auth, report, fetch), plus
a v3 variant that flips only auth. Project: `nightlybox`.

## Run matrix

| run | task | hints offered (→used) | new knowledge | turns | commands ok/fail |
|---|---|---|---|---|---|
| 01 | auth discovery | — | — | — | model endpoint hang → run.failed |
| 01b | auth discovery | 0 | `/82` token-file, AUTH_TOKEN rejected | 11 | 9/4 |
| 02 | report discovery | `/82` (→used) | — | — | model endpoint timeout → run.failed |
| 02b | report discovery | `/82` (→used) | `/56` out/ required | 8 | 7/1 |
| 03 | full nightly | `/82`,`/56` (→both) | `/40` combined setup | 6 | 4/1 |
| 04 | fetch discovery | `/82`,`/56`,`/40` (→all) | `/37` -limit flag | 5 | 4/1 |
| 05 | v3 upgrade | `/82`,`/56`,`/40`,`/37` (→all 4) | `/73` corrected auth | 10 | 8/2 |
| 06 | full nightly (post-invalidation) | `/56`,`/37`,`/73` (→all 3) | `/51` combined v3 setup | 7 | 6/1 |

After run 05 the operator invalidated `/82` (v2 token-file auth) and `/40`
(embeds the token-file claim); `/56` and `/37` remain valid — verified by
run 06's recall set.

## Observed behavior

1. **Accumulation works.** Each discovery run added one experience; recall
   events (`hint.offered` → `knowledge.used`) fired in every subsequent run,
   growing from 1 to 4 simultaneous recalls by run 05.

2. **Knowledge changes actions, not just answers.** Run 03's trajectory is
   the direct evidence: after `ls`/`cat README`/`cat main.go`, turn 3 issued
   ONE composed command built entirely from prior knowledge
   (`token-file + mkdir out + auth + report + verify`). No doc-lure path was
   re-investigated. Run 06 reproduced this on v3 knowledge in 7 turns.

3. **Selective death.** Run 05 recalled all four knowledges; the v3 flip
   broke only the auth claim. The run answer explicitly separated "prior
   assumption invalidated" (token-file) from "assumptions that still hold"
   (report, fetch) — and reused the survivors. Operator invalidation then
   retired `/82` and `/40` while `/56`/`/37` stayed live.

4. **Recall hygiene after invalidation.** Run 06 was offered exactly the
   three living knowledges and neither invalidated one — the matcher
   excludes terminal states, so stale knowledge stops leaking into prompts.

5. **Failure lanes are ordinary history.** Runs 01 and 02 died on transient
   model-endpoint failures (hang / empty 200 bodies); their recall events
   remain in the stream and the fold shows them as short failed lanes.

## Metrics note

Turn counts: discovery runs 8-11, reuse runs 5-7. The sharper metric this
corpus enables (now that `tool.completed` carries `exit_code`) is *failed
attempts per task*: discovery runs paid 1-4 command failures each (the
documented-but-wrong mechanism), while reuse runs paid at most 1 (negative
controls), and runs 03/06 paid none on known ground.

## Kernel changes driven by this experiment

- `tool.completed` now records `exit_code` for sandbox commands (commit
  `1912283`) — trajectory replay can distinguish failed attempts without
  parsing model-authored text.
- `kernel.call_model` retries transient endpoint failures (3 attempts,
  15-60s backoff) instead of failing the run on the first hang — deployed
  mid-experiment; the very next run hit a real timeout on attempt 1 and
  completed normally via retry.

## Fold observations (Experience Timeline)

424 events → 6 knowledge items, 8 runs, 36 lifecycle points, 14 activation
links. The full `appeared → recalled → injected → archived` arc is visible
for the auth experience, including the archive markers at 05:06:25 and the
post-invalidation recall set of run 06.

One finding: **lexical claim clustering over-merges on this corpus.** All
six propositions share domain vocabulary (`nb`, `nightly`, `auth`, `report`,
`exit`…), and the late-stage "end-to-end" propositions (`/40`, `/51`, `/73`)
legitimately reference every mechanism, so token-overlap grouping collapses
everything into a single cluster (title "nb auth", aggregate state
`invalidated` despite four living members). A df-based ubiquitous-token
filter does not separate them (pairwise overlaps stay ≥3 after filtering).
This is a property of the corpus as much as of the heuristic: the nightly
setup experience genuinely spans all three mechanisms. Recorded as a known
limitation; candidate refinements (matcher-tier-aware grouping, topic
vectors) are deferred until a larger corpus justifies them — per the phase
constraint against premature graph generality.

## Reproduction

See `examples/experiment5/README.md` for the fixture layout, run prompts and
workspace reset commands.
