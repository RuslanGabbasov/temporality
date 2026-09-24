# ADR-011: One Temporal Workflow per managed AgentRun

## Context

Temporal can provide durable execution but per-turn Workflow creation may add unnecessary history and network transitions. A purely in-process loop has a separate failure model.

## Decision

Every managed run is one Temporal Workflow. Turns are deterministic workflow decisions; model/tool calls are Activities. Delegated agents are child Workflows. Do not create a Workflow per turn or tool call. Defer an ephemeral in-process execution mode until latency measurement justifies its second semantics.

## Alternatives

Workflow per model turn; all runs ephemeral; one workflow for entire project lifetime.

## Consequences

Simple resume/cancel semantics with a single durable run ID. Long conversations must use Continue-As-New or explicit run/session boundaries to control history growth.
