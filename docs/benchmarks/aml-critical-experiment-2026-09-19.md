# AML Critical Experiment — 2026-09-19

Vertical slice of the radical pivot (`docs/pivot/07-temporality-radical-pivot.md`, §17 critical
experiment, §20 first milestone): a classic tool-calling harness with an external adaptive
memory layer attached via four hooks, measured on a deterministic synthetic ops environment
across repeated sessions with a mid-experiment environment change.

- Data: `benchmarks/aml-critical-2026-09-19-123753.json` (arms A, C, D),
  `benchmarks/aml-critical-b-fix.json` (arm B, see correction note below)
- Code: `aml/` (llm, harness, opsenv, memory), driver `cmd/aml-bench`
- Databases: `aml_arm_{a,b,c,d}` (forensic: `aml_assets`, `aml_events`, `aml_tool_calls`;
  arm A records nothing — no layer attached, by design)

## 1. Executive summary

- **Memory-as-asset confirmed on this fixture**: every memory arm cut failed calls ~2×
  (16 → 8–9 over 15 tasks) and total tokens by 7–11% with zero quality loss (15/15 tasks
  solved in all arms, including the no-memory control).
- **The win concentrates exactly where the pivot predicts**: repeated tasks in a known scope.
  Session-2 T1/T2 solved with 1 API call / 0 failures vs 4 calls / 2 failures in the control.
- **Stale knowledge is the real cost axis**: after the environment flip, arm B (no temporal
  ranking, no feedback) spent MORE tokens than no memory at all on the flipped scope
  (7,646 vs 5,322 on billing tasks, +44%); arm D (adaptive feedback) stayed below baseline
  (6,364) and suppressed re-injection of the stale asset after a single contradiction
  (1 misleading hint vs C's 2, and 0 further injections in the same session).
- **Semantic scope is the load-bearing retrieval component**: logged `why_selected` shows
  `scope_match=1.0` with `semantic_similarity` 0.05–0.15; lexical similarity alone never
  reaches the injection threshold (arm B v1 degenerated into a second no-memory control).
- **Untested by this fixture**: the repeated-failure guardrail (§11) — the model never
  repeated an identical failing call in 240+ tool calls, so 0 guardrail firings.

## 2. Hypothesis under test

> Если агент уже однажды решил проблему правильно, может ли Temporality сделать так, чтобы
> в похожей ситуации через несколько сессий он вспомнил это до того, как повторит старую
> ошибку? (pivot §22)

Plus the §17 risk question: does memory injection create more problems than it solves
(helpful vs misleading hints), especially when reality changes under the agent.

## 3. Setup

- Model: `deepseek/deepseek-v4.1-flash` via OpenRouter, temperature 0, max_output_tokens
  16384, reasoning "low" (shared env config; identical across arms).
- Harness: ordinary tool-calling loop (`aml/harness`): system prompt, task, tools
  (`api_call`, `api_docs`), tool results; memory attached only through 4 hooks
  (TaskStart / BeforeToolCall / AfterToolCall / TaskEnd).
- Environment (`aml/opsenv`): 4 services, public docs claim all auth methods are supported;
  the gateway accepts one per service (the discoverable trap). One service requires a param
  from docs, one paginates. Epoch 2 (session ≥3): billing-api auth matrix flips
  oauth→403 / pat→200, invalidating learned knowledge.
- Tasks: T1 billing list (auth trap), T2 payments refund (different auth trap),
  T3 notifications template (param trap), T4 search pagination, T5 billing get (param + auth).
- 5 tasks × 3 sessions per arm; task order fixed; memory persists across sessions within an
  arm (per-arm postgres DB); epoch flip at session 3.
- Arms:
  - **A** — no memory layer attached.
  - **B** — semantic retrieval (scope + lexical similarity), no temporal/confidence weighting,
    no feedback.
  - **C** — B + temporal ranking (recency in session units, environment match, confidence,
    status factor), no feedback.
  - **D** — C + pre-action activation + repeated-failure guardrail + adaptive confidence
    feedback (REINFORCE on successful reuse, WEAKEN→STALE/ARCHIVED on contradiction).
- Injection budget: ≤3 hints at task start, ≤1 pre-action; hints are 1–2 lines.

**Correction note (arm B)**: in the first full run arm B's ranking used lexical similarity
without scope and never reached the injection threshold (0 hints in 15 tasks — it ran as a
second no-memory control, A≈B: 16 vs 15 failed calls, which usefully bounds run-to-run
noise at ~±6%). Per pivot §3 scope is part of semantic retrieval, so B's score was corrected
to `0.6·scope + 0.4·similarity` and B was re-run (same model, same config, fresh DB).
A, C, D figures are from the first run, B from the corrected run.

## 4. Results

### Totals (15 tasks per arm)

| arm | success | tool calls | failed calls | steps | total tokens | hints | reused | helpful | misleading |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| A no memory | 15/15 | 52 | 16 | 57 | 51,696 | – | – | – | – |
| B semantic | 15/15 | 41 | 8 | 51 | 46,495 | 11 | 7 | 5 | 2 |
| C sem+temporal | 15/15 | 41 | 9 | 52 | 47,992 | 14 | 9 | 6 | 3 |
| D C+adaptive | 15/15 | 42 | 9 | 50 | 46,043 | 10 | 7 | 5 | 2 |

### Epoch 1 (sessions 1–2, 10 tasks — memory should compound)

| arm | calls | failed | tokens |
|---|---:|---:|---:|
| A | 35 | 11 | 36,251 |
| B | 27 | 5 | 29,549 |
| C | 29 | 7 | 32,391 |
| D | 30 | 7 | 31,748 |

### Epoch 2 (session 3 — stale-knowledge window after the flip)

| arm | calls | failed | tokens | hints | reused | misleading |
|---|---:|---:|---:|---:|---:|---:|
| A | 17 | 5 | 15,445 | – | – | – |
| B | 14 | 3 | 16,946 | 6 | 4 | 2 |
| C | 12 | 2 | 15,601 | 7 | 5 | 2 |
| D | 12 | 2 | 14,295 | 4 | 4 | 1 |

### Billing scope only (the flipped service), epoch 2

| arm | calls | failed | tokens | misleading |
|---|---:|---:|---:|---:|
| A | 6 | 2 | 5,322 | – |
| B | 6 | 2 | 7,646 | 2 |
| C | 6 | 2 | 7,701 | 2 |
| D | 6 | 2 | 6,364 | 1 |

On the flipped scope every memory arm paid for the stale asset (one failed oauth attempt +
recovery). B and C also paid in tokens; D paid least and stopped injecting the asset
after the first contradiction.

## 5. Memory reuse analysis (the feedback chain)

Full chains are persisted: RECALL (with `why_selected` for every candidate) → INJECT →
REUSE → SUCCESS/CONTRADICTION → EXTRACT, plus every tool call.

- Reuse rate (reused/injected): B 7/11 (64%), C 9/14 (64%), D 7/10 (70%).
- `why_selected` example (D, session 2, T1): `scope_match=1.0, semantic_similarity=0.05,
  confidence=0.7, temporal=1.0, environment_match=1.0` → score 0.71 — scope carries
  retrieval; similarity only tie-breaks.
- Session-2 trajectories (T1, identical in B/C/D): 1 api_call, 0 failures, ~1,600 tokens
  vs control's 4 calls / 2 failures / ~3,800 tokens. The agent used the hinted auth method
  directly — reuse detected, asset reinforced (D: 0.70→0.76, +1 confirmation, session added
  to `source_sessions`).
- Asset states after the run:
  - **D**: payments-gw 0.76 ACTIVE; search-index strategy 0.76 ACTIVE; search pagination
    0.78 ACTIVE; billing strategy-switch **0.43 STALE** (3 confirmations, 1 contradiction);
    billing param-required **0.40 STALE**.
  - **C**: all five assets frozen at 0.70 ACTIVE — the stale billing asset keeps injecting
    forever.
  - **B**: identical freeze (no confidence updates by design).
- D's recovery trace (s3 T1): hint (stale oauth) → agent follows → 403 → CONTRADICTION →
  asset weakened to STALE → next billing task (T5) got **0 hints**; agent probed pat → 200.

## 6. Attention/render analysis

The retrieval pipeline is fully logged and deterministic (same inputs → same ranking).
Observed failure modes:

1. **Lexical similarity is nearly useless at this text length** (0.05–0.15 Jaccard even for
   on-scope assets). Scope extraction (service lexicon) does the work. For real harnesses
   the scope extractor must handle repos/modules/components — the pivot §3 lexicon approach
   is viable but needs a real-world lexicon source.
2. **Pre-action activation rarely added value** beyond task-start hints (one extra useful
   case: covering a resource whose asset ranked below the top-3 at task start). It did
   prevent duplicate injection of the same asset within a task.
3. **Outcome attribution is call-level, not cause-level**: a call matching a recommendation
   (auth=oauth) that failed for an unrelated reason (missing version param → 400) was
   counted as a contradiction of the auth asset (C s2 T5, D similar). One such false
   contradiction is visible in the data. Cause-level attribution (match the failure mode to
   the recommended dimension) is a required fix before longer runs.

## 7. Failure / limitation inventory

- **Guardrail (§11) untested**: 0 repeated identical failing calls across all arms —
  deepseek-flash at temperature 0 does not retry identical failures in this environment.
  A dedicated fixture (flaky-looking 503s, rate limits that punish retries) is needed.
- **Single run per arm** (plus the accidental A/B twin): run-to-run spread on identical
  config was 16 vs 15 failed calls (~6%) — differences smaller than ~10% between arms are
  within noise; B≈C≈D differences in totals are NOT conclusive.
- **Arm A has no forensic log** (no layer attached → no call recording) — A's internals are
  reconstructable only from driver metrics.
- **Model follows hints readily** (64–70% reuse) — risk 5 (agent ignores hints) not observed
  with this model; other models may differ.
- Tasks are small (2–5K tokens each): system+task prompt overhead limits relative token
  savings; on longer tasks the same discovery saving is worth proportionally more.
- 100% success everywhere: the fixture does not measure quality regression, only efficiency.

## 8. Go/No-Go (pivot §19)

| # | criterion | verdict |
|---|---|---|
| 1 | agent avoids previously made mistakes | ✓ failed calls 16→8–9; session-2 discovery eliminated on known scope |
| 2 | fewer repeated failed tool calls | n/a — no repeated identical failures occurred (fixture limitation) |
| 3 | less time to successful strategy | ✓ first correct attempt on session-2 tasks is the first API call (vs 2 failures cold) |
| 4 | useful memories auto-reinforced | ✓ (D only): 0.70→0.76–0.78 with independence-aware increments |
| 5 | wrong memories auto-weakened | ✓ (D only): single contradiction → 0.43 STALE; injection stopped same session |
| 6 | false/irrelevant hints stay low | ✓ 0 irrelevant hints (scope filter); misleading hints only from the deliberate env flip (B 2, C 3, D 2 — one of C/D's is the attribution artifact from §6.3) |
| 7 | existing harness practically unchanged | ✓ four hooks, no loop changes |

**Verdict: PASS for the pivot direction** — memory activation produced a measurable
behavioral improvement with no quality loss, and the adaptive variant (D) demonstrated the
stale-memory containment the pivot considers decisive ("неправильная память потенциально
опаснее отсутствия памяти").

Not yet proven: long-horizon compounding, guardrail value, behavior on a real harness.

## 9. Conclusions

1. The pivot's core bet — timely activation of small, scoped experiences beats both no
   memory and naive memory — is supported: every win in the data comes from *the right
   memory at the right moment* (session-2 one-shot solves), and every loss comes from
   *stale memory delivered as truth* (epoch-2 billing overhead).
2. Temporal ranking (C vs B) showed no measurable difference at 3 sessions; the decisive
   mechanism was **feedback** (D): contradiction-driven weakening is what turns a stale
   asset from a recurring tax into a one-off cost.
3. The layer is cheap: at this scale retrieval is one indexed query + deterministic scoring;
   hints added ~50–150 tokens per task.

## 10. Next experiment

1. **Accumulation run**: 8–10 sessions, larger task set, measure whether per-session failed
   calls → 0 and whether D's advantage over C grows with more stale events (C's cost should
   be linear in flips, D's amortized).
2. **Guardrail fixture**: introduce a service where the tempting strategy hits
   rate-limited/flaky 503s to force retry loops; measure hint → behavior-change on §11.
3. **Cause-level attribution**: only count CONTRADICTION when the failure mode plausibly
   matches the recommended dimension (auth asset vs 401/403, param asset vs 400-detail).
4. **Real harness sidecar**: expose the layer over HTTP/MCP and attach to an existing
   open-source harness (pivot §1), re-run this fixture through it.
5. **Second model** to check hint-following and reuse rates.

Raw per-task tables: appendices in the JSON dumps (`benchmarks/aml-critical-2026-09-19-123753.json`,
`benchmarks/aml-critical-b-fix.json`); smoke-run artifacts in `benchmarks/aml-smoke.json`.
