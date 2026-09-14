# Temporality

Reference implementation of **Frame Runtime Protocol (FRP) v0.3**.

## Status

Implemented foundations for **M0 — Event Log + replay** and **M1 — Claims**:

- versioned FRP `Event` protocol object and JSON Schema;
- append-only PostgreSQL event store;
- deterministic replay ordered by valid time, transaction time, and event ID;
- versioned deliberate/ambient attention with stable scoring and hysteresis;
- replay boundaries by episode, branch, and `as_of`;
- versioned replay manifests included in deterministic digests;
- HTTP/JSON API with opaque cursor pagination;
- Prometheus-compatible runtime, render, and attention metrics;
- in-memory adapter for deterministic tests;
- versioned Claims and evidence relations;
- atomic Event + Claim + relations persistence.

See [`ROADMAP.md`](ROADMAP.md) for subsequent milestones.

## Run locally

Requirements: Go 1.23+, Docker Compose.

```sh
docker compose up -d postgres
DATABASE_URL=postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable go run ./cmd/temporality-runtime
```

If `DATABASE_URL` is omitted, the runtime uses an ephemeral in-memory store.

```sh
curl -X POST http://localhost:8080/v1/events \
  -H 'content-type: application/json' \
  -d '{"event_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a01","episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a02","type":"episode.started","payload":{},"provenance":{"source":"user"}}'

curl -X POST http://localhost:8080/v1/replay \
  -H 'content-type: application/json' \
  -d '{"episode_id":"018f47a7-34b2-7d10-a932-4f3ff37a4a02"}'

curl 'http://localhost:8080/v1/events?episode_id=018f47a7-34b2-7d10-a932-4f3ff37a4a02&limit=100'
curl http://localhost:8080/metrics

curl -X POST http://localhost:8080/v1/claims \
  -H 'content-type: application/json' \
  -d '{"event":{"payload":{},"provenance":{"source":"user"}},"claim":{"proposition":"The build is reproducible","confidence":0.9}}'

curl -X POST http://localhost:8080/v1/claims/CLAIM_ID/transitions \
  -H 'content-type: application/json' \
  -d '{"event":{"payload":{},"provenance":{"source":"verification"}},"to_status":"supported","confidence":0.98}'
```

## Runnable smoke scenario

With the Compose PostgreSQL healthy, run:

```sh
make smoke
```

This builds and starts `temporality-runtime` plus the separate `temporality-executor` process, creates an initial Frame, creates an Objective, verifies two identical deterministic RenderPackets, submits a validated `CognitiveEmission`, reduces its attention/frame operations into an immutable transition, restores the next Frame from PostgreSQL, atomically reduces CognitiveEmission into Frame/Claims/Attention/Executions and then runs the durable Execution lifecycle, verifies replay, prints the IDs/digest, and stops the runtime.

Render packets expose the attention engine version, scored Event/Region ambient map, selected periphery, and an outside-frame candidate count. `/metrics` includes render totals/errors, focus switches, ambient hit rate, attention entropy, missed candidates, and attention collapse score. Deliberate `attend()` operations bypass ambient hysteresis.

The M2 emission endpoint rejects emissions containing claims or actions until those intents can be committed atomically with Frame and Execution state; they are never silently dropped.

Time-travel and provenance endpoints:

```text
POST /v1/replay                  # episode replay or frame_id cursor replay
POST /v1/snapshots
GET  /v1/snapshots/{id}
POST /v1/blame
```

Frame replay is read-only, excludes events after the target Frame cursor, verifies a canonical Frame hash, and treats snapshots only as optional accelerators. Corrupt snapshots fall back to an earlier snapshot or full Event replay.

Rebuildable projection endpoints:

```text
POST /v1/projections/regions/rebuild
GET  /v1/regions
```

Regions are derived from the canonical Event Log by the versioned `region-event-type.v1` projector and can be deleted/rebuilt without memory loss.

Objective and render endpoints:

```text
POST /v1/objectives
GET  /v1/objectives/{id}
POST /v1/render
```

Atomic cognition endpoint:

```text
POST /v1/step
```

A Step commits the validated CognitiveEmission record/hash, candidate Claims, deliberate attention Events, affordance requests, created Executions, `frame.transitioned`, and the next immutable Frame in one transaction. The emission itself is not an Event.

Execution endpoints:

```text
POST /v1/executions
GET  /v1/executions/{id}
POST /internal/v1/executions/{id}/transitions
```

Execution requests and `execution.created` are committed before the Executor can move an execution to `running`. The PoC executor supports the versioned `inspect_environment` deterministic workflow and bounded adaptive executions through durable recorded planner traces through a safe Go filesystem adapter; Every adaptive proposal is persisted before its effect and checked against capabilities/max steps. The recorded adapter performs no inference or network calls, and shell execution is not embedded in the Runtime.

Frame endpoints:

```text
POST /v1/frames
POST /v1/frames/{id}/transitions
POST /v1/frames/{id}/emissions
GET  /v1/frames/{id}
```

## Development

```sh
make fmt
make test
make race

# With a running PostgreSQL instance:
TEST_DATABASE_URL='postgres://temporality:temporality@localhost:5432/temporality?sslmode=disable' make integration
```

Pagination responses contain `next_cursor` when another page exists. Pass it back as the `cursor` query parameter; clients must treat it as opaque.

The runtime/executor boundary is preserved: this service records runtime facts and does not execute external side effects.
