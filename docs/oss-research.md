# OSS research for the Agent Kernel

Research snapshot: 2026-09-24. This is decision-oriented; versions and project status must be rechecked before pinning a release.

## Durable execution

| Component | What it offers | Cost / fit for this project | Decision |
|---|---|---|---|
| Temporal | Durable workflow histories, activity retries, timers, child workflows, signals/cancellation; maintained Go SDK | Separate service/worker operations and deterministic workflow rules; strongest fit for long-lived agent runs, delegation and approval | Select for managed AgentRun execution; do not put Temporality semantic events into Temporal history |
| Restate | Durable async/await services with retries, timers and messaging; Go SDK | Attractive lower-level alternative, smaller workflow ecosystem and different serving model | Keep as comparator; do not build two execution paths |
| DBOS Transact | Go durable workflows checkpointed in PostgreSQL, queues/notifications/schedules; MIT Go SDK | Very close to current Go+Postgres stack and lower operational footprint; compare carefully on child-workflow model, long waits and operational maturity | Best fallback if Temporal operational cost proves too high in PoC |
| Hatchet | Distributed task/workflow orchestration, queues, concurrency and Go SDKs | More task-queue/DAG oriented; evaluate if workload is predominantly job graph rather than interactive long-running agent session | Not selected for initial harness |
| Inngest | Durable functions, retries, schedules, event-triggered execution; Go SDK; server source carries SSPL/DOSP terms | Good developer workflow and hosted-first ergonomics; license/hosting model needs review and product requires continuous interactive session semantics | Not selected |
| Trigger.dev | Durable AI jobs/workflows with Apache-2.0 repo | Strong TS/JS-oriented choice; adds another language/runtime boundary to this Go repository | Not selected for kernel, revisit if TS product client becomes primary |

Temporal's official Go guide exposes workflows, activities, child workflows, cancellation, timers, signals/message passing and Continue-As-New as first-class primitives. Its Go SDK is actively released. Child workflows have explicit start/completion and parent-close semantics; use stable child IDs and wait for child-start acknowledgement before allowing a parent to finish.

## Agent frameworks and harnesses

| Project | Useful ideas | Why not use as our Kernel base |
|---|---|---|
| Temporal Agent Harness | Durable agent Workflow, tools as Activities/inline tools, approval waits, typed composition, child agents, Code Mode, standard event stream. Current README reports 0.4.0, MIT, experimental and fast moving | Python-first and deliberately supplies broad harness abstractions. Use as reference and compare its events/approval/delegation semantics; do not make Temporality's Go runtime a wrapper around its Python internals |
| PydanticAI | Type-safe tools, structured outputs, model/provider abstractions and optional Temporal integration | Python-centric; excellent client/library reference, not a Go kernel dependency |
| OpenAI Agents SDK | Small primitives for agents/tools/handoffs/guardrails, built-in tracing and human-in-loop | Provider ecosystem and tracing are useful; tying core loop to one vendor SDK conflicts with provider-neutral gateway |
| LangGraph | Graph-based stateful agent execution, checkpoints and human intervention | Powerful, but brings its own orchestration/state model and graph model as a core abstraction. Kernel needs a smaller turn/tool contract |
| TrueForge | Current public repo advertises model/MCP/sandbox/approval/context/session handling and local or hosted modes | Strong harness-level alternative; Daytona is currently its sandbox backend (more planned), and it would replace rather than validate the kernel design. Evaluate against the PoC feature checklist before considering adoption |
| SandBase Harness | Local-first runtime with persistent sessions, MCP bridge, credentials, approvals, sandbox backends, audit/replay; Apache-2.0 repo | Useful current benchmark for a self-hosted harness; does not provide the chosen Temporal durability contract. Run an integration spike later rather than adopting its runtime state model |

Temporal Agent Harness is the closest reference for the required combination. Take its workflow/activity boundary, approval wait, typed child-agent interfaces and event lifecycle. Avoid taking its SDK-specific plugin design, Python-only implementation, or event stream as the Temporality schema. Its own event stream is a runtime UX format; Temporality must preserve project-scoped knowledge semantics and valid/transaction time.

## Model gateway

| Option | Value | Decision |
|---|---|---|
| In-kernel provider interface + adapters | No mandatory proxy dependency; lowest deployment cost for one provider; kernel controls timeouts and emits canonical metadata | Build this first and preserve OpenAI-compatible endpoint support already present in the repo |
| LiteLLM Router/Proxy | Unified provider APIs, deployment routing, retries/fallbacks, budgets, rate limits and spend tracking | Optional separately deployed gateway when multi-provider routing/admin policy is needed. Kernel calls its OpenAI-compatible endpoint; it does not embed LiteLLM (Python) |
| Provider-specific SDK in workflow code | Rich native features | Not allowed inside deterministic workflow code; put provider SDK usage in Activities behind the interface |

## MCP and tool protocol

Use the official [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) client behind the Kernel tool registry. The upstream states it is the official Go SDK, maintained with Google, implements the protocol, and v1.7.0 adds MCP revision 2026-07-28 while maintaining prior revisions. Pin a compatible version and explicitly test transport/auth behavior; the newest MCP spec changes lifecycle and transport details. A tool call is still a Kernel operation with permission, idempotency, artifact refs and Temporality events, not an opaque SDK trace.

## Sandbox and artifacts

The Sandbox is an interface with `Create`, `Exec`, `Read`, `Write`, and `Destroy`. A host-local process backend is convenience-only; do not advertise it as isolation. For untrusted code, start with Docker container isolation and explicit network, mount, resource and credential policy, then evaluate Kubernetes/remote workers. Keep Docker replaceable.

Use an S3-compatible artifact interface with a local filesystem development implementation. Do not pin MinIO as the reference deployment: upstream GitHub marks `minio/minio` archived on 2026-04-25 and its license is AGPL-3.0. Select a maintained compatible service only when deployment needs are known; MinIO may still be used by deployments after their license/maintenance review.

## OTel and event bus

OpenTelemetry Go reports stable tracing and metrics APIs; use trace/span IDs to correlate infrastructure operations with Temporality event IDs. OTel does not encode knowledge lifecycle or temporal truth. No event bus in MVP: PostgreSQL outbox and polling are enough for one publisher. NATS JetStream is a later option only if consumer fan-out or delivery backpressure becomes a measured problem.

## Language decision

| Criterion | Go | Python | TypeScript |
|---|---|---|---|
| Current repository/runtime | Existing service, PostgreSQL store, API and AML harness are Go | Would split existing persistence/runtime | UI is TS, but backend is Go |
| Temporal | Official SDK; good fit for worker/activity process | Mature SDK and most current AI examples | Official SDK and integrations |
| MCP | Official Go SDK now available | Broad AI ecosystem | Broad MCP/agent tooling ecosystem |
| Model ecosystem | Adequate through OpenAI-compatible gateway and provider interface | Broadest provider/library ecosystem | Strong web/app ecosystem |
| Ops/type safety | Single compiled service, explicit interfaces | Fast iteration, extra runtime/dependency operations | Good types, Node service adds another deployment runtime |

**Decision: Go Kernel (Option A's Go half), provider/MCP/sandbox adapters as explicit interfaces.** This minimizes duplication with the existing system and supports one owner for protocol, database and workflow worker. Python/TypeScript clients remain welcome producers. Reconsider only if PoC shows the Go AI/MCP surface blocks required model features.

## Excluded infrastructure for MVP

Neo4j duplicates relationship queries that are currently project-scoped and small; pgvector is premature before retrieval evaluation; Kafka/NATS are premature before multiple consumers or throughput evidence; ClickHouse is premature for early product analytics; Daytona is a replaceable sandbox provider, not the only backend; LangChain/LangGraph and GoClaw/TrueForge/SandBase are reference/adaptation candidates, not required foundations.
