---
order: 2
section: Getting started
title: Quick start
description: From zero to your first agent task — installation, onboarding, the first run.
---

# Quick start

## Requirements

- Docker (Linux, macOS; Windows via WSL2). BuildKit recommended.
- ~4 GB of free memory for the stack (PostgreSQL, Temporal, Journal, Kernel, UI).
- A key from any OpenAI-compatible provider (OpenAI, OpenRouter, local Ollama,
  etc.) — the onboarding wizard asks for it on step three.

That is all: the model, the first agent, the sandbox and the supervisor model
are configured in the first-run wizard — no `.env` needed for that.

## Run it

Prebuilt images from GHCR — the recommended path, nothing to clone or build:

```sh
mkdir temporality && cd temporality
curl -o docker-compose.yml https://raw.githubusercontent.com/RuslanGabbasov/temporality/main/docker-compose.yml
docker compose up -d
```

The latest published release comes up by default (images from the
[releases page](https://github.com/RuslanGabbasov/temporality/releases)). For
controlled upgrades pin a version:
`TEMPORALITY_VERSION=2026.2.113 docker compose up -d`. Upgrading to a newer
release: `docker compose pull && docker compose up -d` (or set the new pinned
version) — the kernel migrates on start.

Building from source (for developing Temporality itself):

```sh
git clone https://github.com/RuslanGabbasov/temporality.git && cd temporality
docker compose -f docker-compose.yml -f docker-compose.dev.yml up --build -d
```

This starts PostgreSQL, the Journal (`:8080`), the UI (`:3000`), Temporal (UI `:8233`),
the Agent Kernel (`:8090`), SearXNG (`127.0.0.1:8889`, meta-search for the agent)
and a headless browser.

Stop the stack keeping the data: `make stack-stop`.

## Onboarding

Open `http://localhost:3000` — the wizard walks you through the setup and
hides navigation until it is done:

1. **First project** — the workspace for agent tasks.
2. **Administrator** — your user (anonymous access closes after this step).
3. **Model provider** — base URL and key: one button validates the key and
   fetches the model catalog automatically. The provider and the key live in
   the database; the key is encrypted — with `KERNEL_SECRET_MASTER_KEY` unset,
   an encryption key is generated on first start and stored next to it. For
   production set that variable in env before the first start, so secrets
   survive moving the database.
4. **Sandbox** — pick a ready-made environment profile (Node, Python, Browser
   E2E) and build its image in one click (needs access to the Docker daemon).
   Skippable — build it later.
5. **First agent** — from a template: contract, permissions and role are
   already set up (Coder, Reviewer, Researcher, QA, DevOps). Name it and pick
   a model.
6. **Supervisor model** — the model that watches runs and judges agent
   evolution. Defaults to the agent's model; change it later in Settings.

Two cases where the wizard behaves differently:

- `KERNEL_AUTH_TOKENS` is set — authentication is enabled by your env: the
  wizard does not run anonymously, sign in with one of those tokens
  ("API-token" mode) or comment the variable out for anonymous onboarding;
- `KERNEL_SANDBOX_IMAGE` is set — the sandbox image is pinned by your env:
  the build step offers a skip.

The provider model can also come from env
(`TEMPORALITY_MODEL_BASE_URL` / `_ID` / `_API_KEY`) — a legacy fallback
"default model" for runs without an explicit model; the onboarding wizard is
the main path.

## The first task

Open **Workspace** — the agent chat — and write a task. The agent answers
with streaming; along the way it can:

- run commands in the sandbox (`/workspace` of the project — persistent storage),
- delegate subtasks to other agents (`plan`/`delegate`),
- ask you a question via an **approval** (a card in the chat, mirrored in Runs),
- record a useful observation into the project's knowledge.

When it finishes, check **Runs** (the step-by-step trace) and **Knowledge**
(what the agent learned from the task).

## What to set up next

- **Channels** — let the agent receive tasks from Matrix/Telegram/Slack and ask questions where you are: [Inbound and channels](inbound.html).
- **Triggers** — tasks on a schedule or on events: [Triggers](triggers.html).
- **Skills** — capture a repeating procedure: [Skills](skills.html).
- **Users and access** — the team and RBAC: [Administration](administration.html).
