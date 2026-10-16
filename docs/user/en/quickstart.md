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
- A key from any OpenAI-compatible provider (OpenAI, OpenRouter, local Ollama, etc.).

## Run it

```sh
git clone https://github.com/RuslanGabbasov/temporality.git
cd temporality
cp .env.example .env
# edit .env: TEMPORALITY_MODEL_BASE_URL / _ID / _API_KEY
docker compose up --build -d
```

This starts PostgreSQL, the Journal (`:8080`), the UI (`:3000`), Temporal (UI `:8233`),
the Agent Kernel (`:8090`), SearXNG (`127.0.0.1:8889`, meta-search for the agent)
and a headless browser. `TEMPORALITY_MODEL_BASE_URL` + `_ID` are the mandatory
minimum; the kernel refuses to start without them.

Provider configuration examples:

```dotenv
# OpenAI
TEMPORALITY_MODEL_BASE_URL=https://api.openai.com/v1
TEMPORALITY_MODEL_ID=gpt-4.1-mini
TEMPORALITY_MODEL_API_KEY=sk-...

# Local Ollama
TEMPORALITY_MODEL_BASE_URL=http://host.docker.internal:11434/v1
TEMPORALITY_MODEL_ID=llama3.2
TEMPORALITY_API_KEY=
```

Stop the stack keeping the data: `make stack-stop`.

## Onboarding

Open `http://localhost:3000`. On the first visit an onboarding wizard walks you
through the required steps and hides navigation until they are done:

1. **First project** — a workspace for the agent's tasks.
2. **Administrator** — your user.
3. **Model provider** — base URL, key (stored in encrypted secrets), automatic model listing.
4. **Agent** — from a template or from a plain-language description.
5. **Sandbox build** — the image the agent's commands will run in (requires Docker daemon access).

## Your first task

Open **Workspace** — the agent chat — and write a task. The agent responds with
streaming output; along the way it may:

- run commands in the sandbox (`/workspace` of the project is persistent storage),
- delegate subtasks to other agents (`plan`/`delegate`),
- ask you a question through an **approval** card (in the chat, mirrored in "Runs"),
- record a useful observation into the project's knowledge.

When it finishes, look at **Runs** (the step-by-step trace) and **Knowledge**
(what the agent learned from the task).

## What to set up next

- **Channels** — let the agent receive tasks from Matrix/Telegram/Slack and ask questions where you live: [Inbound and channels](inbound.html).
- **Triggers** — tasks on a schedule or on events: [Triggers](triggers.html).
- **Skills** — capture a repeating procedure: [Skills](skills.html).
- **Users and access** — your team and RBAC: [Administration](administration.html).
