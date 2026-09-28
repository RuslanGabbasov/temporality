# Temporality Roadmap

## Status: September 2026

### Done (P0)

- [x] **Kernel P0** — failure/reconciliation, duplicate mitigation, cost accounting, quotas, secrets, runbook
- [x] **Live MCP E2E** — test-mcp in image, sandbox volume, KERNEL_MCP_TEST_ROOT, approval flow
- [x] **Journal endpoints** — state-at-T, diff, activation chain, run state-at-T, compare runs
- [x] **Workspace refactor** — agents top-level, providers CRUD, users CRUD with DB tokens
- [x] **Experience Priming v1** — deterministic pipeline (group by scope → score → top 7)
- [x] **Carbon Design System** — full integration, dark theme, proper input styles
- [x] **Workspace chat** — SSE streaming, conversation history, Markdown rendering, agent selector on new chat
- [x] **Operations page** — human-readable tool display, verdict buttons, pending badge
- [x] **Runs trace** — rich event timeline with arguments/output, color-coded icons
- [x] **Memory Lens filters** — strength, recency, activated, cross-scope, terminal state, presets
- [x] **Experience Timeline** — SVG, scopes, lifecycle, zoom, semantic zoom, detail panel
- [x] **Pending badges** — Operations (uncertain ops) and Runs (approval requests) on nav bar
- [x] **Sandbox network** — agent network_access wired to Docker --network=bridge
- [x] **Read-only tools** — read_file/search/grep no longer flagged as uncertain operations
- [x] **Task reuse** — follow-up chat messages reuse same task, not creating new ones
- [x] **Tool arguments in traces** — tool.started has arguments, tool.completed has output
- [x] **Cost accounting v1** — model prices, per-call costs (per-run/project aggregation pending)

---

## P0/P1 — Trajectory-derived experience

- [x] **Trajectory extraction primitive**
  - Extract repeating steps, tool-call sequences, decisions and outcomes from completed runs.
  - No separate storage: result is built from event stream.
  - Separate deterministic features from LLM-derived conclusions.
  - Acceptance: for a selected run, get a set of repeating structures with provenance to original events.

- [x] **Experience pattern projection**
  - Aggregate similar memories/events into experience patterns.
  - Account for relevance, recency, validation, outcome, recurrence, contradiction, supersession.
  - Store provenance and links to original events.
  - Acceptance: hundreds of candidates collapse into a compact set of patterns without losing ability to expand to original evidence.

- [x] **Experience Priming experiment**
  - Add optional pre-run stage: candidates → patterns → ranking → 3–7 cues.
  - Limit priming with strict token budget.
  - Don't put raw hundreds of memories in prompt.
  - Allow agent to JIT-retrieve details through existing tools/memory.
  - Acceptance: compare baseline / conventional RAG / priming / priming+JIT by task success, trajectory length, token usage, unnecessary retrieval/tool calls, wrong-memory activation, contradiction rate and latency.
  - Fix regressions: priming is not mandatory until value is confirmed.

- [x] **Trajectory comparison / fork**
  - Allow running controlled variants from one source task/configuration.
  - Compare trajectories and derived experience between variants.
  - Minimum set: no priming vs priming.
  - Acceptance: differences visible at step level, tool calls, cost and outcome, not just final answer.

- [x] **Trajectory-to-artifact extraction**
  - Build general interface for extracting reusable artifacts from trajectory.
  - Minimum two types: experience pattern and repeatable workflow fragment.
  - Each artifact must have provenance to source trajectory/events.
  - Don't implement automatic agent-loop-to-workflow replacement yet.

---

## P1 — Make experience understandable

- [x] **Activation chain UI** — click knowledge → see where it arose, where recalled, where injected, what decision followed, what actions, what outcome, where validated/invalidated/superseded. Distinguish: reused/validated, failed, invalidated, superseded, resurrected.
- [x] **Semantic zoom** — Knowledge → Episode → Event. Episode = turn / tool call / observation / formation / validation / invalidation / reuse. Transition from "event log" to "experience episodes".
- [x] **Forensic View** — why did this experience appear? which evidence? which run/action/outcome?
- [x] **Experience lineage** — K82 → K73 supersession chain visible on timeline
- [x] **Shareable investigation URL** — backend done, frontend polish needed. Lens, filters, zoom, selected entity all in URL.
- [x] **Investigation flows** — "why did agent do this?", "why did knowledge disappear?", "why do two runs differ?", "what does the team know?"
- [ ] **Experiment 9 corpus** — validate human understanding of experience evolution through UI. Scenarios: competing hypotheses, repeated confirmation, contradiction, resurrection, stale knowledge, supersession, different scopes. Acceptance: which hypothesis appeared first? which confirmed? which died? which resurrected? why stale knowledge used? what evidence caused contradiction? what's valid now? what was actually used?

---

## P1 — Platform usability

- [x] **State-at-T as product feature** — not just an API endpoint. "What did the system know when this decision was made?" needs a proper UI accessible from timeline/trace.
- [ ] **Streaming improvements** — show model tokens as they arrive (not just turn-level)
- [ ] **Conversation branching** — fork a conversation from a specific point
- [x] **Agent templates** — pre-configured agents for common tasks (coder, reviewer, researcher)
- [x] **Project settings** — per-project defaults for agent, model, sandbox
- [x] **Bulk operations** — select multiple runs/operations for batch actions

---

## P1 — Sandbox security matrix

Minimal automated security acceptance (not a sandbox platform):

- [ ] `../` workspace escape — blocked
- [ ] Network egress — blocked per profile
- [ ] Credentials — inaccessible
- [ ] Docker socket — inaccessible
- [ ] Memory/CPU/PIDs — limited
- [ ] Timeout — process guaranteed to terminate
- [ ] Output — bounded
- [ ] `/scratch` — isolated
- [ ] Reviewer/QA — read-only restrictions enforced

Result: automated tests, expected-vs-actual matrix, explicitly documented accepted risks.

---

## P1 — Before inviting first users (Minimum Usable Experience)

### Setup
- [ ] project
- [ ] agent
- [ ] system prompt
- [ ] model
- [ ] skill
- [ ] MCP
- [ ] sandbox

### Execution
- [ ] task
- [ ] run
- [ ] streaming/status
- [ ] tool execution
- [ ] approval
- [ ] result

### Observation
- [ ] run timeline
- [ ] tool calls with arguments/output
- [ ] model calls
- [ ] MCP provenance
- [ ] operations
- [ ] uncertain operations

### Experience
- [ ] knowledge
- [ ] activation chain
- [ ] state-at-T
- [ ] replay
- [ ] diff

### Iteration
- [ ] change prompt/skill
- [ ] re-run
- [ ] compare runs

If this exists, we can invite a small team and stop relying only on fixtures.

---

## P2 — Advanced

- [ ] **Read-after-write probes** — operation → observable trace → probe → hint to operator. Probe gives a hint, does NOT automatically issue authoritative verdict.
- [ ] **Cost accounting v2** — cost per run, cost per project, aggregation, token quotas
- [ ] **Memory population overview** — active/stale/invalidated counts over time
- [ ] **Cross-project knowledge** — knowledge that spans multiple projects
- [ ] **Embeddings infrastructure** — if lexical clustering proves insufficient
- [ ] **Graph-based experience relations** — when timeline is not enough

---

## NOT doing (until proven necessary)

- Universal graph clustering
- Topic modeling
- Graph database
- Complex automatic clustering
- New runtime
- Complex visual editor
- Generic event viewer / second debugger
- Separate scheduler
- Complex model gateway (until second provider/fallback/routing needed)
- Complex tenant model (until multiple org structures exist)
- Full sandbox platform
- Automatic agent-loop-to-workflow replacement

Return criterion: a concrete real usage scenario that cannot be satisfied by current architecture.

---

## Deferred infrastructure

| Topic | Return when |
|---|---|
| Tenants | multiple org structures |
| Helm / non-compose | beyond single host |
| Behavioral SLO | real abuse scenarios |
| Evidence package | external audit/consumer |
| Model gateway | second provider / fallback / routing |
| Embeddings / graph DB | proven necessity |
| Complex sandbox orchestration | real requirements for multiple workload types |

---

## Architecture

```
Temporal          — durable execution and orchestration
Agent Harness     — agent execution semantics
Temporality       — experience and memory
Enterprise CP     — identity, tenants, RBAC, secrets, quotas, policies
Infrastructure    — compute, containers, DB, network
```

## Strategic conclusion

Temporality is at a transition point:

```
Phase 2: "We proved the model is correct"
    ↓
Next: "We made a system that people can use"
    ↓
Goal: "People use it to understand and improve agent experience"
```

The immediate goal is NOT to close all Phase 2 remnants.

The immediate goal:

> **Collect a minimum end-to-end working loop where a real person runs a real agent and Temporality turns the resulting experience into an investigable temporal picture.**

After that, every experiment must be simultaneously:
- acceptance corpus
- real working scenario
- source of product requirements

This way Temporality won't drift into "yet another debugger" or "yet another agent runtime".
