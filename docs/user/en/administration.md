---
order: 11
section: Administration
title: Administration
description: Users and RBAC, the org structure, providers, secrets, sandboxes and quality settings.
---

# Administration

## Upgrading

The stack runs prebuilt images from GHCR. Upgrading is pinning a new version
and recreating the containers:

```sh
TEMPORALITY_VERSION=2026.2.0 docker compose up -d
```

Database migrations apply automatically when the kernel starts. Data lives in
compose named volumes and survives container recreation. Rolling back is the
same move to the previous version (down-migrations do not exist for every
schema; take a `postgres` dump before rolling back a major step).

Building from source (`docker compose -f docker-compose.yml -f
docker-compose.dev.yml up --build`) is the development path: it overwrites
the local images and silently turns `TEMPORALITY_VERSION` into `latest` — do
not do this on a real deployment. The base compose file cannot build at all:
deployment is pull-only of the pinned version.

## Users and RBAC

The **Users** page (`/users`): creation, roles, bearer tokens, notification
channels. Roles are ordered: `reader < writer < operator < admin`.

| Role | Can |
|---|---|
| reader | Read: dashboard, traces, knowledge, timeline |
| writer | Start tasks, create project resources |
| operator | Approvals, reconciling uncertain operations, knowledge invalidation |
| admin | Users, providers, channels, org structure, everything else |

A project scope limits visibility: a user sees only the allowed projects (or
all with `*`). The token is entered via Login in the header and kept in the
browser.

## Org structure

The **Organization** page (`/org`, admin): the department tree, users on
nodes, node **policies** (model/MCP/token/network/sandbox restrictions; a ban
on channel tasks for a subtree) and inheritance modes. A project binds to a
node and inherits its policies and visibility: team access is configured by
the structure, not by lists.

## Providers and models

The **Providers** page (`/providers`): OpenAI-compatible endpoints (base URL,
key, model list — fetched automatically). A run's model is routed to the
provider that declares it; provider keys are stored as secret references.
Token prices for cost reporting — `KERNEL_MODEL_PRICES`.

## Secrets

The **Secrets** page (`/secrets`): values are encrypted with AES-256-GCM using
`KERNEL_SECRET_MASTER_KEY` and decrypted only inside the kernel. A secret is
granted to an agent on the agent's card: the value arrives in `run_command` as
a `SECRET_<NAME>` variable — the model sees the name, never the value. Grants
are journaled (`secret.issued` / `secret.denied`), and the journal strips
known token patterns from events as a second line of defense.

## Sandboxes

Sandbox profiles (Settings → Sandboxes): the image (built-in Go/Node/Python
recipes or your own digest-pinned one), memory, CPU, tmpfs limits, cache
variables. A profile is assigned to an agent; the agent's commands run in a
container with no privileges, no network (unless enabled) and a read-only
root. The kernel needs Docker daemon access only for this isolation — the
agents themselves never see Docker.

The persistent project filesystem is covered in [Observability](observability.html#sandbox-storage).

## The quality loop

In Agents → "Quality settings":

- **the judge model** — an installation-wide LLM judge for skill and agent evaluations (with agreement calibration below the threshold the judge degrades and the gate goes red);
- **the schedule** — how often evaluation suites re-run.

Applying agent and skill proposals is gated on a green evaluation run — see
[Agents](agents.html#agent-evolution).

## External authentication

OIDC federation is supported (Keycloak, authentik and others): `docs/auth-federation.md`.
