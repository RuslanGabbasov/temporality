# Phase 2 validation report

Status: **in progress**. This report records evidence and gaps, not planned behavior as if it had passed.

## Phase 1 inherited evidence

The dated live smoke report is [`benchmarks/agent-kernel-live-integration-2026-09-24.md`](benchmarks/agent-kernel-live-integration-2026-09-24.md). It covers generic runs, knowledge retrieval/use/invalidation, built-in tools, approval, Docker execution and the four-stage delegation example. It explicitly does not prove a repository-changing engineering task, real MCP semantics, adversarial isolation, replay or reconciliation.

## Phase 2 progress

| Area | State | Evidence / remaining gap |
|---|---|---|
| Execution-observation knowledge heuristic | Implemented and workflow-tested | `kernel-heuristic/execution-observation.v1`: successful (exit 0) sandbox verification/build commands emit `knowledge.proposed` (`kind: observation`, evidence = `operation_id`) with a project-scoped, run-independent identity; the Kernel looks up the projection first, so repeated successes become `knowledge.confirmed` (`execution-reverification.v1`) or `knowledge.used` (`execution-reuse.v1`) instead of duplicate nodes. Identical re-proposals are idempotent in the projection; conflicting ones are rejected. Failures stay runtime events; live multi-run evidence pending the next team run |
| Approval operation observability | Implemented and workflow-tested | Bounded redacted display, canonical arguments hash, stable operation ID; mismatched approval hash cannot run the tool; event order/correlation checked. Sandbox `run_command` now uses auditable `approval.auto_granted` under `sandbox.workspace.v1`, with no human prompt. |
| Code-change fixture | In progress | `examples/code-change/calculator` has an intentional defect and verified failing baseline. Live team run `calculator-e2e-20260924-01` has reached Lead's approval gate for `ls -la`; execution has not started pending a human decision. |
| Event/provenance audit | Documented | See [`event-model.md`](event-model.md); operation/evidence artifacts and semantic lifecycle gaps remain |
| Failure/reconciliation | Audited, incomplete | Local transactional outbox gives at-least-once publish; remote effects still have an uncertain crash window; no reconciler |
| Semantic replay/state-at-T | Audited, incomplete | FRP frame replay exists; AgentRun semantic replay and execution/knowledge `diff(T1,T2)` remain unimplemented |
| MCP protocol adapter | Integration-tested locally | `go test ./kernel/mcpclient` launches the stdio fixture in `examples/test-mcp`, discovers all three tools, performs read/search/create calls, and verifies allowlist and workspace escape rejection. Full live AgentRun → approval → MCP → persisted Temporality evidence remains unvalidated. |
| Adversarial sandbox | Not validated | Isolation flags and positive smoke are documented; attack/resource-exhaustion matrix remains unrun |
| Narrative provenance | Gap confirmed | Final answer is stored in Temporal result; it is not a Temporality artifact with `derived_from` links |

## Verification run in this phase

- Go workflow/unit tests cover approval request → matching grant → tool start → completion, shared operation/run/frame/hash, causal parent links, secret absence, canonical hash stability, and mismatch rejection.
- Calculator fixture baseline fails for the intended reason. The post-fix run and independent QA have not been performed.
- Live approval event currently pending for the Phase 2 team run: operation `calculator-e2e-20260924-01/lead/turn/01/call_00_hhHvflBqy6qFFN5yiDu10715`, display `ls -la`, risk `high`, workspace scoped to the fixture. No tool execution occurred before approval.

## Exit decision

Phase 2 is not complete. Do not claim the architectural acceptance scenario passed until the live code change, independent review/QA, MCP and injected failure cases have persisted evidence and reproducible results.
