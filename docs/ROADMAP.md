# Temporality Roadmap

## Status: October 2026

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

### Done (Platform wave, Sept–Oct 2026)

- [x] **Triggers** — schedule (cron), webhook (HTTP endpoint), event listeners; agent tools list/create/update/delete triggers
- [x] **Approvals in chat** — approval step for consequential tools, approval badge on Runs tab
- [x] **Run cancel** — POST /runs/{id}/cancel + Stop button in chat
- [x] **Thinking display** — reasoning stream shown compactly in chat widget
- [x] **i18n** — full EN/RU localization, auto-detect browser language
- [x] **Light/dark themes** — OS auto-detect via prefers-color-scheme, persisted choice
- [x] **Onboarding wizard** — welcome → project → provider → agent → done, shown on empty workspace
- [x] **Project-centric permissions** — allowed_users on projects, per-user project visibility, admin bypass
- [x] **Auth gate** — clean token-entry screen for logged-out users
- [x] **Design system** — forest/teal/amber identity per DESIGN.md, favicons, consistent dialogs

---

## P0 — Living Skills MVP

Source spec: `docs/living-skills.md` (§43 MVP).

- [x] **Skill package** — `SKILL.md` + `skill.yaml` manifest; legacy loading (manifest inferred from SKILL.md, fields marked inferred)
- [x] **Skill registry** — project-scoped storage, immutable versions, CRUD API
- [x] **Contract validation** — manifest schema, capabilities, tools, runtime requirements checked before use
- [x] **Prompt injection** — compact skill digests in agent system prompt with token budget
- [x] **Agent tools** — `skill_search`, `skill_inspect`, `skill_validate`, `skill_history`, `skill_executions`, `skill_memory`
- [x] **Execution linkage** — runs record skill versions used; executions listed per skill
- [x] **Memory linkage** — `remember` accepts `skill_id`/`capability`; per-skill memory view
- [x] **Skills UI** — tab with overview/contract/executions/memory/versions; skill selection on agent form
- [ ] **CLI** — `temporality skill list/inspect/validate/history/executions/memory`

Not in MVP: agent-driven skill mutation, evaluation engine, marketplace, policy engine (Phase 2).

### Phase 2 (after MVP)

- [ ] Evolution proposals — agent/human-driven, evidence-backed, approval workflow
- [ ] Skill diff / rollback / snapshots
- [ ] Evaluations — suites referenced by manifest
- [ ] Contextual memory retrieval per skill/capability
- [ ] Evolution analytics

---

## P0 — MCP server registry

- [ ] **MCP servers tab** — configure servers per transport: stdio, SSE, Streamable HTTP
- [ ] **Agent MCP assignment** — select configured servers on agent form
- [ ] **Agent tools panel** — per-agent tool availability toggles, including MCP-provided tools
- [ ] **Connection test** — validate server config before save

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
- [ ] **Streaming improvements** — show model tokens as they arrive (not just turn-level). LLM client supports it (`StreamComplete` + `TokenCallback`); workflow/journal/SSE wiring missing.
- [ ] **Conversation branching** — fork a conversation from a specific point
- [x] **Agent templates** — pre-configured agents for common tasks (coder, reviewer, researcher)
- [x] **Project settings** — per-project defaults for agent, model, sandbox
- [x] **Bulk operations** — select multiple runs/operations for batch actions

---

## P1 — Sandbox security matrix

Automated security acceptance (not a sandbox platform).

Existing coverage in `kernel/sandbox/docker_test.go`:

- [x] `../` workspace escape — blocked (symlink escape test)
- [x] Docker socket / privileged — inaccessible (`--cap-drop=ALL`, no `--privileged`, image digest pinned)
- [x] Memory/CPU/PIDs — limited (args enforced in test)
- [x] Output — bounded (limited buffer truncation test)
- [x] Read-only workspace for reviewer/qa roles — enforced via args

Missing:

- [ ] Network egress — blocked per profile (live test against real Docker, not just args)
- [ ] Credentials — inaccessible
- [ ] Timeout — process guaranteed to terminate
- [ ] `/scratch` — isolated between agents
- [ ] Expected-vs-actual matrix doc with explicitly accepted risks

---

## P1 — Before inviting first users (Minimum Usable Experience)

### Setup
- [x] project
- [x] agent
- [x] system prompt
- [x] model
- [x] skill — via Living Skills MVP (skills tab, agent skill selection)
- [ ] MCP — tracked in MCP server registry above
- [x] sandbox

### Execution
- [x] task
- [x] run
- [x] streaming/status
- [x] tool execution
- [x] approval
- [x] result
- [x] run cancel

### Observation
- [x] run timeline
- [x] tool calls with arguments/output
- [x] model calls
- [x] MCP provenance
- [x] operations
- [x] uncertain operations

### Experience
- [x] knowledge
- [x] activation chain
- [x] state-at-T
- [x] replay
- [x] diff

### Iteration
- [x] change prompt
- [x] re-run
- [x] compare runs
- [x] change skill — skill versions are immutable; edit a skill and bump the version

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

Living Skills extend this loop: the skill a person installs becomes an observable, versioned capability whose usage produces memory and evidence.

After that, every experiment must be simultaneously:
- acceptance corpus
- real working scenario
- source of product requirements

This way Temporality won't drift into "yet another debugger" or "yet another agent runtime".
