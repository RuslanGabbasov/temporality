# Phase 2 status report

Date: 2026-09-24. Scope: state of the Agent Kernel × Temporality validation after the calculator E2E converged (`calculator-e2e-20260924-05`), including an audit of the model and MCP client implementations against the pivot contract. This report is input for planning the next phase; it does not make decisions.

## Executive summary

- The first fully converged live code-change scenario is done: run `-05` completed all four roles (Lead/Coder/Reviewer/QA) naturally, with a real defect fixed (`a - b` → `a + b`), independent verification in a read-only reviewer sandbox, and a persisted knowledge lifecycle (`proposed → confirmed → used`, plus cross-run reuse through hints).
- The Phase 2 loop (run → observe events → find semantic gap → fix kernel → rerun) is working as designed. Runs `-02`/`-03`/`-04` each exposed a real kernel gap; all three were fixed and re-validated in `-05`.
- The model client (`aml/llm/client.go`) is a minimal single-endpoint OpenAI-compatible adapter, not the Model Gateway the pivot contract (§13) expects. The gap is now observability-critical rather than feature-critical: model events cannot answer "which provider, which input/output, at what latency and cost".
- The MCP client (`kernel/mcpclient/client.go`) implements discovery/allowlist/approval and correct event types, but is single-server stdio with a fresh session per call, and its events lack server identity, result references and evidence links (§14). Live AgentRun → MCP is still unvalidated.
- Phase 2 acceptance criteria are partially met. Remaining P0/P1 gaps: failure/reconciliation model, live MCP, adversarial sandbox, semantic replay / state-at-T / diff.

## Live evidence

| Run | Outcome | What it proved / exposed |
|---|---|---|
| `calculator-e2e-20260924-02` | Converged with empty handoffs | Every role ended at `turn_limit` with no final text; reviewer summary non-empty. Exposed the missing finale. |
| `calculator-e2e-20260924-03` | Converged with empty handoffs | Knowledge heuristic live: `proposed → confirmed → used` with 3 auto nodes for 30+ verification invocations; shell-wrapped verification attribution gap found and fixed (`0974aa8`). |
| `calculator-e2e-20260924-04` | Degenerate | 152 sandbox executions from near-duplicate tool calls in single responses. Exposed the dedup gap. |
| `calculator-e2e-20260924-05` | **Fully converged** | All four roles `completed` naturally, 20 turns, substantive handoffs; reviewer re-ran every check itself in the read-only sandbox (via `/scratch`) and approved; QA approved. First architectural acceptance candidate scenario. |

## Fixes landed in this phase

| Commit | Change |
|---|---|
| `0974aa8` | Shell-wrapped verification commands attributed to their final command; pipelines/substitution/background/masking tails refused by the knowledge heuristic. |
| `d22f2f3` | Agent examples (incl. calculator fixture) built into the compose kernel image. |
| `bf92a98` | Forced finale model call at turn limit (no tools, `forced_finale` in events) + last-turn budget reminder + exact-duplicate tool calls within one response execute once (`duplicate_of`). |
| `c3d49cb` | Sandbox `/scratch` tmpfs (rw, exec, nosuid, size 512m, `KERNEL_SANDBOX_SCRATCH_SIZE`), `HOME`/`TMPDIR` pointing there — lets read-only reviewer roles compile/run without write access to the workspace. |
| `1efcb1f`, `2beb882` | Validation report updates. |

Local `main` is ahead of `origin/main` by these commits plus `0742ffa`, `c8a4e0a` (narrative provenance, compose) — **not yet pushed**.

## Client implementation audit

### Model client: `aml/llm/client.go` vs pivot §13 (Model Gateway)

The pivot requires a provider-independent gateway surface (aliases, routing, fallbacks, rate limits, cost accounting, streaming, structured output) and mandates that every model call carries `actor, run, frame, model, provider, input reference, output reference, token usage, latency, cost` and is linked into Temporality.

| Expected (§13) | Present | Gap |
|---|---|---|
| Provider abstraction | OpenAI-compatible wire format only, one endpoint via `TEMPORALITY_MODEL_*` | Kernel is effectively bound to one provider config; no alias/routing/fallback layer |
| Model aliases / routing / fallbacks / rate limits | None | Single hardcoded endpoint; no failover |
| Streaming | None | Blocking `chat/completions` only |
| Structured output | None | No `response_format` support |
| Cost accounting | None | Usage is decoded but not priced or persisted per call |
| Tool calling | Yes | Works; see pathologies below |
| Per-call Temporality identity | Partial | Events exist but under-specified (below) |

Concrete findings:

1. **Model events under-specified.** `model.started` carries `{turn, model}`; `model.completed` carries `{turn, tool_call_count, finish_reason, total_tokens}`. Missing vs §13: `provider`, input/output references, latency, cost, and the prompt/completion token split (`Usage` is decoded but only `TotalTokens` is emitted). `ModelRequest{Model, Messages, Tools}` carries no run/frame identity, so the activity cannot enrich events even if it wanted to.
2. **Provider-specific leakage.** The `reasoning` request field (`enabled/exclude/effort`) is a mimo-style provider extension hardcoded into the generic client.
3. **`tool_choice: "auto"` is always sent**, including the forced-finale call where the tools list is empty — semantically contradictory wire traffic (harmless today, but it is provider-tuned behavior baked into the kernel path).
4. **`parallel_tool_calls` is never set**, which is directly related to the observed duplicate-call pathology in run `-04`.
5. **No salvage for malformed tool arguments.** One invalid JSON `arguments` string fails the entire completion (`once()` returns an error), losing all other well-formed calls in the same response.
6. **Network errors are not retried.** `transient()` only recognizes provider HTTP 429/5xx; a transport failure from `http.Do` fails the call on the first attempt. (Also: retry jitter uses a fixed seed `1`, so backoff sequences are identical across all client instances.)
7. **Retry budget exceeds activity budget.** Client worst case is 3 × 180s HTTP timeout + backoff ≈ 9+ minutes, while `ActivityCallModel` runs with `StartToCloseTimeout: 4m` and `MaximumAttempts: 1` — the activity can be killed mid-retry, converting a recoverable provider hiccup into a run failure.
8. **`finish_reason: "length"` is silently accepted.** With the default `max_tokens` of 1024, truncated answers are recorded as normal completions; nothing in the event stream marks the truncation.
9. **Per-call client reconstruction.** `CallModel` with a non-empty `request.Model` re-reads env and builds a new client per call.

Assessment: the client is fine as a smoke-test driver and its bounded code size is a virtue, but it is the wrong shape for the contract. Either it grows into the gateway surface (provider identity, references, usage/cost/latency capture, structured output) or — per the pivot's LiteLLM research direction — an actual gateway takes over and this client becomes one provider adapter behind it. That is a planning decision, not made here.

### MCP client: `kernel/mcpclient/client.go` vs pivot §14 (MCP)

| Expected (§14) | Present | Gap |
|---|---|---|
| Official MCP SDK/protocol | Yes (`modelcontextprotocol/go-sdk`) | — |
| Discover / call / errors / timeouts | Yes (discovery at startup, `IsError` mapping, ctx timeouts) | Transport timeouts implicit via context only |
| Authorization / approval split | Yes (`KERNEL_MCP_ALLOW` + `KERNEL_MCP_APPROVAL`) | — |
| Event triple `mcp.call.started/completed/failed` | Yes (+ `mcp.call.blocked`) | Payloads under-specified |
| Per-call fields: `server, tool, actor, arguments hash, result reference, execution, frame, evidence` | `tool`, `arguments_hash`, `operation_id` (operation/run/frame come from the event envelope) | **No server identity, no result reference, no evidence link** |
| Large args/results out of event stream | Results truncated to 64 KiB inline | No artifact store / result reference; truncation is the only bound |

Structural limitations:

1. **Single stdio server**, configured by `KERNEL_MCP_COMMAND`; no multi-server model, so "server" identity is currently implicit and unrecorded.
2. **A new MCP session per call** (`connect → CallTool → Close`). No session reuse, no connection lifecycle events, no server-side state continuity — acceptable for the stdio fixture, wrong for real servers.
3. **Live AgentRun → MCP is unvalidated.** Coverage is the local integration test against `examples/test-mcp` (discovery, read/search/create calls, allowlist and workspace-escape rejection). No persisted Temporality evidence from a real run exists yet.

### Temporality adapter (`aml/temporality/client.go`)

Best-effort HTTP observer for the legacy AML harness (hooks, no delivery guarantees). Secondary to the kernel path (which publishes through the runtime with the local transactional outbox) and not a Phase 2 blocker.

## Observed model behavior pathologies (context for planning)

- **Near-duplicate tool calls.** mimo-flash emits dozens of almost-identical verification calls per response; exact-match dedup catches clones, but flag/echo variations slip through. Turn limit + forced finale bound the cost but do not eliminate the waste. `parallel_tool_calls: false` and/or fuzzy dedup are candidate mitigations.
- **Malformed argv.** The model occasionally emits glued commands (`["pwd","ls","-la"]`); the tool description does not reliably prevent this.

## Phase 2 acceptance criteria status

| Criterion group | State |
|---|---|
| Approval surface (bounded args, redaction, identity) | **Met** (workflow-tested, live auto-grant policy recorded) |
| Real agent task (Lead/Coder/Reviewer/QA converges) | **Met** in `-05`; scenario should be re-run once more after any kernel change |
| MCP real server, discovery, lifecycle, provenance | **Not met** — local test only; events lack server/result/evidence fields |
| Sandbox adversarial matrix | **Not met** — positive smoke only; fs/network/resource/credentials/lifecycle matrix unrun |
| Failure handling / delivery semantics / reconciliation | **Partial** — local transactional outbox (at-least-once publish) exists; no reconciler, crash-window semantics for remote effects undefined |
| Temporality trajectory / knowledge reconstruction / provenance | **Met for the happy path** in `-05`; needs failure scenarios to be proven robust |
| State-at-T / diff / semantic replay | **Not met** — FRP frame replay only |
| Narrative vs event record | **Met** (`agent.summary` with `derived_from` frames, bounded/redacted) |

## Candidate directions for the next phase

Ordered by dependency, not by preference — decisions belong to the plan, not this report.

1. **Model call observability** (small, unblocks §13 compliance): extend `ModelRequest`/model events with provider, prompt/completion token split, latency, input/output references; handle `finish_reason: "length"`; decide gateway-vs-adapter direction before adding more provider features.
2. **Failure/reconciliation model** (P0, architecture-critical): define delivery semantics for the crash window after a side effect; build the reconciler; inject failures in a live run.
3. **Live MCP E2E** (P1): run AgentRun → approval → MCP → persisted evidence against the test server; add server identity + result references to `mcp.call.*` events (requires an artifact/reference mechanism, which failure handling may also need — shared infrastructure).
4. **Adversarial sandbox matrix** (P1): execute the documented fs/network/resource/credentials/lifecycle battery, fix policy gaps, record expected-vs-actual per case.
5. **Semantic replay / state-at-T / diff** (P1): knowledge and execution projections over immutable history.
6. **Duplicate-call mitigation** (quality of life): `parallel_tool_calls: false`, near-duplicate throttling, malformed-argv salvage.

## Reproduction state

Compose stack: postgres, runtime (:8080), executor, debugger (:3000), temporality-dev (:8233 UI), agent-kernel (:8090) with all current fixes in the image. The fixture `.sandbox/calculator` is currently in the *fixed* state (`a + b`); reintroduce the `a - b` defect before the next E2E run.
