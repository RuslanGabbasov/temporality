# Agent Kernel live integration report — 2026-09-24

## Scope

Live smoke verification against the local Temporal dev server, PostgreSQL, Temporality runtime, OpenAI-compatible model endpoint and Docker daemon. Temporary runs used project `temporality-live-verification`. No repository files were changed by the sandbox commands. The hint-test claim and all seven model-generated smoke claims were invalidated afterward so they are excluded from future hints while their event history remains available.

## Verified

- Runtime health returned HTTP 200; PostgreSQL Compose health was `healthy`; Temporal UI and the Agent Kernel health endpoints returned HTTP 200.
- Generic `AgentRun` completed with a live model response. Runtime events reached Temporality and were retrievable by project and run ID.
- A deterministic knowledge proposal was retrieved by the hint API. A matching AgentRun recorded `memory.read` with `hint_count: 1` and `knowledge.used`. Manual invalidation changed its projected state to `invalidated`, and subsequent hint retrieval excluded it.
- The live model invoked the built-in `echo` tool. Temporality recorded `tool.started` and `tool.completed` with the same operation ID.
- A human approval signal was accepted, followed by `approval.granted` and tool execution.
- The Docker sandbox ran `/bin/sh -c 'printf sandbox-live-ok'` in the preloaded `golang:1.27-alpine` image pinned to digest `sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125`. The result was exit code 0 and stdout `sandbox-live-ok`; no matching container remained afterward.
- The opt-in Lead/Coder/Reviewer/QA parent workflow and all four child workflows reached `completed` using the live model and sandbox-enabled Kernel. Parent `delegation.started` and `delegation.completed` events were present.
- Coder's command `printf coder-stage-ok` ran in the sandbox with exit code 0.
- QA's read-only `pwd; ls -la` ran successfully and showed the workspace mounted at `/workspace`.

## Incomplete validation

The role pipeline was a smoke scenario, not a code-change task. Lead's first `run_command` request was rejected because its arguments were not present in the approval event. The model later produced the plan without executing that command.

Reviewer attempted `git status --porcelain`, which failed because `git` is absent from the sandbox image. Its later `git diff`, extra workspace listings and repeated Coder command were rejected. Reviewer correctly returned `NOT VERIFIED` and did not claim to have inspected the repository.

QA inspected the workspace and `.git` metadata. It proposed `go build ./...`, but that approval was rejected because the sandbox has no network and its read-only root cannot populate build caches; the host build had already passed. No build or test ran inside QA's sandbox. Therefore this run does not verify source edits, review quality, or QA acceptance criteria.

## Observability finding

`approval.requested` records the action and operation ID but omits the pending tool arguments. A reviewer or operator using only the Temporality event API cannot see which command they are approving. The exact arguments were recoverable from Temporal Workflow history during this investigation. Include a bounded, redacted command summary in the approval surface before relying on human review for arbitrary model-authored commands.

The model's final text can also contradict recorded events: the earlier sandbox smoke answer said no approval prompt was needed even though `approval.requested` and `approval.granted` were both present. Treat the event stream as the execution record; agent-authored summaries are not authoritative.

## Result

Generic execution, hint retrieval/use/invalidation, tool execution, approval signaling, Docker command execution and four-stage delegation all passed live smoke checks. The product-level code-change scenario, real MCP server, adversarial sandbox validation, semantic AgentRun replay and uncertain-side-effect reconciliation remain open.
