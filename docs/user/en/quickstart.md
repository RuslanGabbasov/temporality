---
order: 2
section: Getting started
title: Quick start
description: From zero to your first agent task — installation, onboarding, the first run.
---

# Quick start

## Requirements

- Docker with Compose v2.23.3+ (Linux, macOS; Windows via WSL2). BuildKit recommended.
- ~4 GB of free memory for the stack (PostgreSQL, Temporal, Journal, Kernel, UI).
- A key from any OpenAI-compatible provider (OpenAI, OpenRouter, local Ollama,
  etc.) — the onboarding wizard asks for it on step three.

That is all: the model, the first agent, the sandbox and the supervisor model
are configured in the first-run wizard — no `.env` needed for that.

## Run it

Prebuilt images from GHCR — the recommended path, nothing to clone or build.

One-line install (recommended for beginners): the script checks the
environment, fetches the compose file, starts the stack and waits until it
is healthy:

```sh
curl -fsSL https://raw.githubusercontent.com/RuslanGabbasov/temporality/main/install.sh | sh
```

Manually, without the script — the compose file is self-contained (the
Temporal dynamic config and the SearXNG settings are embedded via
`configs:`), and the `--wait` flag holds the command until the stack is
healthy:

```sh
mkdir temporality && cd temporality
curl -O https://raw.githubusercontent.com/RuslanGabbasov/temporality/main/docker-compose.yml
docker compose up -d --wait
```

The latest published release comes up by default (images from the
[releases page](https://github.com/RuslanGabbasov/temporality/releases)). For
controlled upgrades pin a version:
`TEMPORALITY_VERSION=2026.2.113 docker compose up -d --wait`. Upgrading to a
newer release: `docker compose pull && docker compose up -d --wait` (or
`… install.sh | sh -s -- update`) — the kernel migrates on start.

The installer has two more modes: `… install.sh | sh -s -- doctor`
diagnoses an install (docker, compose version, container state, port
reachability), `… install.sh | sh -s -- uninstall` stops and removes the
stack (data volumes are kept; `--purge` deletes them too).

### No install at all: GitHub Codespaces

You can try the full stack right in the browser without a local Docker: the
button opens a codespace, brings up the whole compose stack (including the
sandbox — Docker-in-Docker works inside codespaces) and opens the UI:

[Open in GitHub Codespaces](https://codespaces.new/RuslanGabbasov/temporality)

Good to know:

- personal GitHub accounts get 120 core-hours of Codespaces per month free
  (the default machine is 2 cores / 8 GB — the stack fits); this is your
  account's quota, the project pays nothing;
- the first start pulls ~2 GB of images into the inner Docker — a few
  minutes;
- a codespace sleeps after ~30 minutes of inactivity: data is kept and
  waking it takes about a minute. A full container rebuild resets everything
  except `/workspaces` (the sandbox root lives there on purpose);
- this is a "try it" path, not production: the quota and the codespace
  lifetime are limited.

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
