# Phase 2 validation report

Status: **in progress**. This report records evidence and gaps, not planned behavior as if it had passed.

## Phase 1 inherited evidence

The dated live smoke report is [`benchmarks/agent-kernel-live-integration-2026-09-24.md`](benchmarks/agent-kernel-live-integration-2026-09-24.md). It covers generic runs, knowledge retrieval/use/invalidation, built-in tools, approval, Docker execution and the four-stage delegation example. It explicitly does not prove a repository-changing engineering task, real MCP semantics, adversarial isolation, replay or reconciliation.

## Phase 2 progress

| Area | State | Evidence / remaining gap |
|---|---|---|
| Execution-observation knowledge heuristic | **Live-validated** | `kernel-heuristic/execution-observation.v1`: successful (exit 0) sandbox verification/build commands emit `knowledge.proposed` (`kind: observation`, evidence = `operation_id`) with a project-scoped, run-independent identity; the Kernel looks up the projection first, so repeated successes become `knowledge.confirmed` (`execution-reverification.v1`) or `knowledge.used` (`execution-reuse.v1`) instead of duplicate nodes. Identical re-proposals are idempotent in the projection; conflicting ones are rejected. Failures stay runtime events. Live evidence: team run `calculator-e2e-20260924-03` produced the full lifecycle — coder `knowledge.proposed` (`go test ./...`, `go test -v ./...`, `go vet ./...`), reviewer `knowledge.confirmed` (reverification), QA `knowledge.used` (reuse) — with 3 auto nodes for 30+ verification invocations, no flood. The first live runs (`-02`) exposed and fixed a coverage gap: models run verification through `sh -c "cd dir && GOCACHE=… go test ./…"`, which the argv-only classifier ignored; shell-wrapped scripts are now attributed to their final command (exit-code owner), with pipelines, command substitution, background jobs and masking tails (`go test; echo EXIT=$?`) refused so unproven successes cannot pollute memory (commit `0974aa8`). |
| Approval operation observability | Implemented and workflow-tested | Bounded redacted display, canonical arguments hash, stable operation ID; mismatched approval hash cannot run the tool; event order/correlation checked. Sandbox `run_command` now uses auditable `approval.auto_granted` under `sandbox.workspace.v1`, with no human prompt. |
| Code-change fixture | **Live-validated (knowledge goal); convergence gap open** | `examples/code-change/calculator` has an intentional defect and verified failing baseline. Full compose team runs `calculator-e2e-20260924-02`/`-03`: Coder fixed `Add` (`a - b` → `a + b`), verified via sandbox `go test`, Reviewer independently confirmed fix correctness (and correctly refused to claim validation when evidence was missing), bug was fixed at run end. All four roles ended at `turn_limit` with empty final answers in both runs: the model spends every turn on tool calls and never converges to a final message, so handoffs are empty (root cause of Reviewer's `-02` objection). Turn budget/finale prompting needs kernel-side attention before the scenario counts as fully passed. |
| Event/provenance audit | Documented | See [`event-model.md`](event-model.md); operation/evidence artifacts and semantic lifecycle gaps remain |
| Failure/reconciliation | Audited, incomplete | Local transactional outbox gives at-least-once publish; remote effects still have an uncertain crash window; no reconciler |
| Semantic replay/state-at-T | Audited, incomplete | FRP frame replay exists; AgentRun semantic replay and execution/knowledge `diff(T1,T2)` remain unimplemented |
| MCP protocol adapter | Integration-tested locally | `go test ./kernel/mcpclient` launches the stdio fixture in `examples/test-mcp`, discovers all three tools, performs read/search/create calls, and verifies allowlist and workspace escape rejection. Full live AgentRun → approval → MCP → persisted Temporality evidence remains unvalidated. |
| Adversarial sandbox | Not validated | Isolation flags and positive smoke are documented; attack/resource-exhaustion matrix remains unrun |
| Narrative provenance | **Live-validated** | The final answer is now recorded as a derived `agent.summary` event (bounded to 4 KiB, redacted, whitespace-collapsed) with `derived_from` frame links — every turn frame in AgentRun, every delegation frame in the team workflow. It follows `run.completed` and is never treated as an execution source of truth; `model.failed`/`run.failed` now carry a bounded redacted `error` detail. Live evidence: `calculator-e2e-20260924-02` (non-empty reviewer summary) and `-03` (summary present, honestly empty because every role ended at `turn_limit` with no final text), both with `derived_from` linking all delegation frames. |

## Verification run in this phase

- Go workflow/unit tests cover approval request → matching grant → tool start → completion, shared operation/run/frame/hash, causal parent links, secret absence, canonical hash stability, and mismatch rejection.
- Calculator fixture baseline fails for the intended reason. Compose team runs `calculator-e2e-20260924-02`/`-03` executed the full Lead/Coder/Reviewer/QA loop: the defect was fixed and verified in-run; knowledge lifecycle `proposed → confirmed → used` and cross-run knowledge reuse (`hint.offered` → `knowledge.used` in later roles) are persisted in project `calculator-e2e`.
- The historical approval gate of run `-01` is obsolete: sandbox `run_command` operations are auto-granted under `sandbox.workspace.v1` policy (recorded as `approval.auto_granted`), so team runs no longer block on human approval.
- Known live gaps observed in `-02`/`-03`: every role ends at `turn_limit` with an empty final answer (no convergence to a final message); Reviewer sandbox mounts the workspace read-only and its writable `/tmp` filled up after repeated compilations (`no space left on device`), driving duplicate verification retries (161 tool calls in `-03`); models sometimes emit malformed multi-command argv (`["pwd","ls","-la"]`).

## Exit decision

Phase 2 is not complete. Do not claim the architectural acceptance scenario passed until the live code change, independent review/QA, MCP and injected failure cases have persisted evidence and reproducible results.
