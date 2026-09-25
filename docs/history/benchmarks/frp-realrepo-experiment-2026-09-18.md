# FRP Real-Repository Memory Experiment — 2026-09-18

## 1. Repository and why

| candidate | files | offline build | offline `go test ./...` | verdict |
|---|---:|---|---|---|
| **blevesearch/bleve** `v2.6.1` (048761396d42661336db8caa0bed1e98cf2aeaa6) | 804 | ✅ | ✅ 87 pkgs, ~90s | **chosen** |
| cli/cli `v2.101.0` (0cf10924) | 1406 | ✅ | ❌ pre-existing `TestNewCmdExtension` | rejected: polluted checker signal |
| go-gorm/gorm `v1.31.2` | 185 | — | — | rejected: < 500 files |

Bleve: pure Go (no CGO), several subsystems (analysis / index / search / mapping / geo), full
suite green **offline** (GOPROXY=off) — the checker signal (`go test ./...`) is deterministic.
Commit pinned; every fixture workspace is a local clone at that commit.

## 2. Tasks (injected bugs, symptom-only objectives)

All patches round-trip validated: bug → exactly the named tests fail, suite otherwise green;
reference fix → 87/87 green. Patches are **committed** into the workspace ("chore: workspace
snapshot") so `git status` does not leak injection points. Tests are never modified by the
fixture between the arms of the same task.

| task | type | bug | symptom (what the agent is told) |
|---|---|---|---|
| T1 | local | inverted match cost in `LevenshteinDistance` (`search/levenshtein.go`) | `TestLevenshteinDistance` fails |
| T2 | cross-module | sign-magnitude fixup removed in `numeric.Float64ToInt64` | `TestSortabledFloat64ToInt64` (numeric) **and** `TestBytesRead` (root pkg) fail |
| T3 | temporal | refactor: `levenshtein.go`→`edit_distance.go`, `LevenshteinDistance*`→`EditDistance*`; then off-by-one in `EditDistanceMaxReuseSlice` early-exit | `TestFuzzySearch`, `TestFuzzyMultiPhraseSearch` fail |

T3 is the stale-knowledge probe: everything episode 1 learned about `search/levenshtein.go` and
`LevenshteinDistance` is obsolete in T3's repository state.

## 3. Setup

* Chain: E1 = T1 cold on the canonical substrate S1 → procedures rebuild. For T2/T3: cold
  (fresh DB) ∥ facts (clone of S_prev, `TEMPORALITY_WORLD_MEMORY_MODE=confirmed`) ∥ inv (clone
  of S_prev, mode=all). S_{k+1} = inv arm's DB. Same world id `bleve-261`, `state_version` =
  task index (repo state changed between tasks).
* Warm arms receive memory **only** via the штатный render path (world_memory / procedures /
  entities). No transcripts, no manual summaries.
* Model: `deepseek/deepseek-v4.1-flash` via OpenRouter, temp 0, reasoning low,
  max_output 16384 — identical everywhere. Render budget 16 000 tokens, ≤16 steps,
  ≤1500 s per episode, 2 attempts max per arm.
* Limits: `max_read_bytes` 131072, `max_entries` 4000, `timeout_sec` 300 (full suite ~90–160 s).
* Driver: `scripts/realrepo_benchmark.py`; patches: `scripts/realrepo/patches/`.

## 4. Results

| task | arm | success | total tokens | prompt | compl | steps | reads | uniq | repeat | run_tests | revisits | memory refs | time |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| T1 | cold (seed) | ✅ | 80 739 | 74 858 | 5 881 | 4 | 2 | 2 | 0 | 2 | 0 | 0 | 99 s |
| T2 | cold | ✅ | 130 828 | 123 166 | 7 662 | 10 | 11 | 5 | 6 | 4 | 1 | 0 | 275 s |
| T2 | facts | ❌ step limit | 219 065 | 203 725 | 15 340 | 16 | 14 | 5 | 9 | 5 | 3 | 30 | 816 s |
| T2 | **inv** | ✅ | **78 549** | 73 053 | 5 496 | **7** | 6 | 4 | 2 | 3 | **0** | 48 | 325 s |
| T3 | cold | ❌ step limit | 309 281 | 252 156 | 57 125 | 16 | 21 | 5 | 16 | 3 | 2 | 0 | 237 s |
| T3 | facts | ❌ step limit | 238 066 | 226 022 | 12 044 | 16 | 27 | 6 | 21 | 2 | **16** | 70 | 367 s |
| T3 | inv | ❌ step limit ×2 | 249 224 / 337 546 | 231 549 | 17 675 | 16 | 19 / 22 | 3 / 4 | 16 / 18 | 1 | **18 / 7** | 24 / 173 | 265 / 949 s |

(T3-inv attempt 1 of the first run died on an infrastructure bug at step 10/…; both listed
attempts ran the full 16 steps otherwise. T3-facts first run was killed at step 10 by the same
infra bug — redone cleanly, the redo numbers are in the table.)

**Key ratios:** T2 inv/cold tokens = 78 549 / 130 828 = **0.60×**; steps 7 vs 10; revisits 0 vs 1;
wall-clock 325 s vs 275 s (warm was slower per-step: it read the delivered memory and reasoned
about it). T2 facts/cold = 1.67× **and failed**.

## 5. Where memory helped (T2)

T2-inv (clone of the same S1 substrate as facts, mode=all): solved the cross-module numeric bug
in 7 steps, 0 revisits, cheapest episode of the whole experiment. What it actually received:
episode-1 confirmed claims about the levenshtein defect (all correctly marked stale v1→v2),
module facts, **plus mode=all extras** — refuted paths, investigation history, focus changes.
Trajectory: read `numeric/float.go` early, patched the sign-magnitude fixup, ran the full suite
green, completed. The facts arm, on the *same substrate* with mode=confirmed, wandered
(16 steps, 9 repeated reads, wrong patch in `numeric/float.go`, fail).

→ The useful ingredient was the **investigation story, not the bare facts** — consistent with
the pivot benchmark's conclusion, now reproduced on a real 804-file repository.

## 6. Where memory did not help / hurt (T3)

Nobody solved T3 (cold included): the fuzzy early-exit off-by-one is beyond this model within
16 steps. But the failure signatures differ sharply:

* cold: 2 revisits — a disciplined (if lost) search.
* facts: **16 revisits**, 27 reads. Render forensics: in **14/16 steps** world_memory delivered
  stale confirmed facts («Float64ToInt64 не инвертирует биты» — already fixed in T3's repo;
  levenshtein defects — renamed away). Crucially, **every such item carried `stale: true` with
  the earned-version note** («earned against v1/v2; world is now v3 — treat as history,
  re-verify before relying»). The model re-verified. Repeatedly. The stale-annotation's
  «re-verify before relying» instruction, in a weak model, induces exactly the re-verification
  loops we measure as revisits.
* inv: 18 revisits; stale levenshtein claims appeared in only 2 early steps (relevance ranking
  crowded them out) — its wandering is mostly model limitation plus low-signal memory
  (top-delivered items were go.mod boilerplate).

## 7. Temporal-validity protection: measured answer

Mechanism works end-to-end: world registration v3 on a cloned v2 substrate → every prior claim
rendered with `stale: true`, `world_version`, and a self-explaining note (verified in stored
render packets, T3-facts: 28+28 item deliveries marked). The model did not respect the
annotation. So:

> Warm-агент **видит**, что знание устарело (доставка работает), но **не ведёт себя** соответственно (когниция не использует).

This is a render/cognition-prompt problem, not a storage problem.

## 8. Confirmed conclusions

1. **Memory-as-asset is real on a real repo** — for the investigation-story form: T2 solved at
   0.60× cold tokens with a cleaner trajectory (single measurement, no repeats — directional).
2. **Facts-only memory is not an asset** for this class of tasks: T2-facts 1.67× cold and a
   fail; on T3 stale facts correlated with 8× more refuted-path revisits than cold.
3. The stale-delivery mechanism survives contact with a real repository (804 files, 3 world
   versions, cloned substrates).
4. Forensics is fully replayable: per-step render packets persisted in `cognitive_steps`
   answered «what did the agent see at step k» for every claim in this report.

## 9. Not confirmed / open

1. Whether memory *causally hurt* on T3: n=1 per arm and cold failed too; the revisit gap
   (16–18 vs 2) is suggestive, not proven.
2. Single run per arm (economics); no median-of-3. The 0.60× figure needs repeats.
3. Accumulation beyond one transition (S1→S2) untested: T3 had 2 prior episodes, but nobody
   solved it, so the marginal value of episode 2's knowledge is unmeasured.
4. A stronger model might convert the same memory into a T3 win — model capability and memory
   value are confounded here.

## 10. Infra bugs found & fixed during the run

1. `cognition.ParseRef` whitelist lacked `entity:` — any warm substrate with a rich entity
   graph 422'd every model-step (`frp/cognition/emission.go`; render map → ambient suggestions).
2. Model-reused `emission_id` across steps → `cognitive_steps_pkey` duplicate → arm death.
   Now runtime mints a fresh id on collision (`frp/runtime/modelstep/modelstep.go`).
3. Driver contract: the first registration of a world must be `state_version=1`
   (`substrate/*/worlds.go`) — cold arms of T2/T3 died on v2/v3-first registration; driver fixed.
4. Executor-container/port hygiene, `DROP DATABASE … WITH (FORCE)` for arm isolation.

## 11. Costs

Total: ≈1.81M tokens (prompt 1.57M / completion 240K), ~2 h wall-clock for 9 episodes + redos,
deepseek-v4.1-flash pricing ≈ negligible ($ ~1–2 equivalent).

## 12. Verdict

**PARTIAL.** The core hypothesis (accumulated investigation memory reduces the cost of the
next task in the same world) got its first positive measurement on a real repository
(0.60× tokens, fewer steps, cleaner trajectory). The facts-only form failed, and stale
memory + a weak model produced pathological re-verification loops. Nothing here justifies
architectural expansion; the leverage points are (a) what memory is *selected* for render
(stale and boilerplate items crowd out signal), and (b) making the stale annotation
behaviorally effective (or cheaper to obey).

## 13. Next experiment

1. Repeat T2 3–5× (cold ∥ facts ∥ inv) to put a median on 0.60× — the cheapest decisive step.
2. T3 variant with a stronger model (or 32 steps) to deconfound task difficulty from memory harm.
3. Render-side: down-rank stale items; collapse them into a one-line history digest instead of
   per-item confirmed entries; measure whether facts-arm revisits drop.
4. Only then: 4–6-episode accumulation chain on one repository.

## Appendix: artifacts

* Driver: `scripts/realrepo_benchmark.py` (chain, arms, retries, guardrails, metrics).
* Patches: `scripts/realrepo/patches/{bug1,fix1,bug2,fix2,refactor3,bug3,fix3}.patch`.
* Dumps: `benchmarks/realrepo-e1.json`, `realrepo-full.json`, `realrepo-cold-redo.json`,
  `realrepo-t3-warm-redo.json`.
* Databases kept for forensics: `frp_real_t1_cold` (S1), `frp_real_t2_inv` (S2),
  `frp_real_t2_{cold,facts}`, `frp_real_t3_{cold,facts,inv}`.
* Forensic queries: render packets live in `cognitive_steps.render_packet` (jsonb);
  `world_memory` items per episode via `jsonb_array_elements`.
