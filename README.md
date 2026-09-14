# Temporality

Reference implementation of **Frame Runtime Protocol (FRP) v0.3**.

## Status

Work has started with **M0 — Event Log + replay**:

- versioned FRP `Event` protocol object and JSON Schema;
- append-only PostgreSQL event store;
- deterministic replay ordered by valid time, transaction time, and event ID;
- replay boundaries by episode, branch, and `as_of`;
- HTTP/JSON API;
- in-memory adapter for deterministic tests.

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
```

## Development

```sh
make fmt
make test
```

The runtime/executor boundary is preserved: this service records runtime facts and does not execute external side effects.
