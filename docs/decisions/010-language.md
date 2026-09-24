# ADR-010: Go Agent Kernel

## Context

Go, Python and TypeScript each have viable Temporal/MCP/model ecosystems, but the repository's API, PostgreSQL layer, executor and current harness are Go.

## Decision

Build the Kernel in Go. Use provider and tool interfaces to avoid provider SDK lock-in. Keep Python and TypeScript producer clients first-class through the HTTP contract.

## Alternatives

Python kernel (broader agent SDK ecosystem, but split persistence/runtime); TypeScript kernel (strong web ecosystem, extra Node backend); adopt an existing Python harness.

## Consequences

One server language and direct database reuse; some fast-moving AI SDK integrations may need a gateway or sidecar. Reassess if a required capability has no acceptable Go adapter.
