# Temporality

[Русский](README.md) · **English** · [Docs](https://ruslangabbasov.github.io/temporality/docs/en/) · [Harness operations](https://ruslangabbasov.github.io/temporality/docs/en/operations.html)

Temporality is a runtime and control plane for AI agents: durable tasks on
Temporal, a full trace of every run, permissions and approvals — and
accumulated experience that flows back into future runs.

<p align="center">
  <a href="https://ruslangabbasov.github.io/temporality/assets/knowledge-expantions.mp4">
    <video src="pages/assets/knowledge-expantions.mp4" autoplay muted loop playsinline width="100%"></video>
  </a>
  <em>The knowledge map: the knowledge base laid out by semantic similarity — streaming in live, clusters forming on their own.</em>
</p>

```text
Agent Kernel ──events──▶ Journal ──projection──▶ Knowledge / Hints
     ▲                      │                         │
     └──────── hints ◀──────┴──── Experience Timeline ┘
```

## Quick stand

All you need is Docker (BuildKit). No .env, no keys to start — the model,
the first agent and the sandbox are configured in the onboarding wizard on
first open of the UI.

```sh
mkdir temporality && cd temporality
curl -o docker-compose.yml https://raw.githubusercontent.com/RuslanGabbasov/temporality/main/docker-compose.yml
docker compose up -d
```

Open `http://localhost:3000` — the wizard walks you through: first project →
administrator → model provider (key + auto-fetched catalog) → sandbox profile
(Node / Python / Browser E2E, one-click build) → first agent from a template →
supervisor model. Then write a task in the chat.

The full guide (including production notes like `KERNEL_SECRET_MASTER_KEY`):
[Quick start](https://ruslangabbasov.github.io/temporality/docs/en/quickstart.html).

## Development stand

```sh
git clone https://github.com/RuslanGabbasov/temporality.git && cd temporality
docker compose -f docker-compose.yml -f docker-compose.dev.yml up --build -d
```

The dev overlay builds images from sources; the kernel applies migrations on
start. Debugger dev server with live reload:

```sh
npm --prefix debugger install
npm --prefix debugger run dev     # proxies /api → :8080 and /kernel-api → :8090
```

Useful targets:

```sh
make fmt            # gofmt
make test           # go test ./...
make race           # go test -race ./...
make build          # bin/temporality-journal + bin/agent-kernel
make debugger-test  # frontend vitest
make debugger-build # UI static build
```

## Components

| Component | Path | Purpose |
|---|---|---|
| Journal | `observation/`, `cmd/temporality-journal` | append-only events, knowledge projection, hint retrieval |
| Agent Kernel | `kernel/`, `cmd/agent-kernel` | the agent harness on Temporal: model gateway, MCP, sandbox, approvals, delegation |
| Workspace UI | `debugger/` | agent chat, run traces, knowledge map, settings |

The detailed map of interfaces and internals lives in the
[documentation](https://ruslangabbasov.github.io/temporality/docs/en/) and
[`docs/architecture.md`](docs/architecture.md); event and contract schemas —
in [`schemas/`](schemas/).

## License

[Apache License 2.0](LICENSE).
