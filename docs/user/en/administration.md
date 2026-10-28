---
order: 11
section: Administration
title: Administration
description: Users and RBAC, the org structure, providers, secrets, sandboxes and quality settings.
---

# Administration

## Backup and restore

Three stores — three stories:

| Store | Contents | Backup |
|---|---|---|
| PostgreSQL | runs, knowledge, tasks, users, chats, journal | `pg_dump` (scripts below) |
| Sandbox root | project /workspace files written by agents | tar the directory |
| rembed cache | embedding model weights | not needed — re-downloads itself |

Chats sync to the server (the `workspace_chat` table) — clearing browser
data no longer loses them, and they follow the user across devices.

**Scripts** (from a repository checkout):

```sh
scripts/backup.sh [target-dir]     # DB dump + sandbox tar, keeps 14 of each
scripts/restore.sh db.sql.gz sandbox.tar.gz   # stops writers, restores, starts
```

**Built-in schedule**: the `backup` compose profile — nightly DB dump and
sandbox tar into ./backups, keeping 14 of each:

```sh
docker compose --profile backup up -d backup
```

For production campaigns layer restic/borg on the backups directory with
off-site upload (S3/ssh): a local backup does not survive host loss.

## Upgrading

The stack runs prebuilt images from GHCR. Upgrading to the latest release:

```sh
docker compose pull && docker compose up -d
```

For a controlled upgrade (or a rollback) pin the version explicitly:

```sh
TEMPORALITY_VERSION=2026.2.113 docker compose up -d
```

Database migrations apply automatically when the kernel starts. Data lives in
compose named volumes and survives container recreation. Rolling back is the
same move to the previously pinned version (down-migrations do not exist for
every schema; take a `postgres` dump before rolling back a major step).

Building from source (`docker compose -f docker-compose.yml -f
docker-compose.dev.yml up --build`) is the development path: it overwrites
the local images and silently turns `TEMPORALITY_VERSION` into `latest` — do
not do this on a real deployment, or "latest release" stops pointing at a
published one. The base compose file cannot build at all: deployment is
pull-only.

## Users and RBAC

![Users and roles](../assets/team.webp)

The **Users** page (`/users`): creation, roles, bearer tokens, notification
channels. Roles are ordered: `reader < writer < operator < admin`.

| Role | Can |
|---|---|
| reader | Read: dashboard, traces, knowledge, timeline |
| writer | Start tasks, create project resources |
| operator | Approvals, reconciling uncertain operations, knowledge invalidation |
| admin | Users, providers, channels, org structure, everything else |

A project scope limits visibility: a user sees only the allowed projects (or
all with `*`). Login is password-first: the "Password | API token" form, with
the token as a secondary mode for service accounts and scripts.

### Changing the password

Your own — via the account menu (`POST /v1/auth/password`): an account with a
password confirms the current one; an SSO account without a local password
sets its first one with an empty current field (minimum 8 characters, the
session is not interrupted). With SSO enabled, password login remains for the
admin only — IdP unavailability does not block operations (break-glass).

## Org structure

The **Organization** page (`/org`, admin): the department tree, users on
nodes, node **policies** (model/MCP/token/network/sandbox restrictions; a ban
on channel tasks for a subtree) and inheritance modes. A project binds to a
node and inherits its policies and visibility: team access is configured by
the structure, not by lists.

## Providers and models

The **Providers** page (`/providers`): OpenAI-compatible endpoints (base URL,
key, model catalog — fetched automatically on the `KERNEL_PROVIDER_MODELS_REFRESH`
schedule plus a manual refresh and a probe check). A run's model is routed to
the provider that declares it; catalog models carry roles (chat / embedding /
asr) — draft wizards (agent, skill, team) pick a model through the chain
"env model → judge model → first chat candidate in the catalog", and non-chat
entries (ASR, embedding, image) are filtered out of the candidates. Provider
keys are stored as secret references. Token prices for cost reporting —
`KERNEL_MODEL_PRICES`.

## Secrets

The **Secrets** page (`/secrets`): values are encrypted with AES-256-GCM using
`KERNEL_SECRET_MASTER_KEY` and decrypted only inside the kernel. A secret is
granted to an agent on the agent's card: the value arrives in `run_command` as
a `SECRET_<NAME>` variable — the model sees the name, never the value. Grants
are journaled (`secret.issued` / `secret.denied`), and the journal strips
known token patterns from events as a second line of defense.

## Sandboxes

![Sandbox profiles](../assets/sandbox.webp)

Sandbox profiles (Settings → Sandboxes): the image (built-in Go/Node/Python
recipes or your own digest-pinned one), memory, CPU, tmpfs limits, cache
variables. A profile is assigned to an agent; the agent's commands run in a
container with no privileges, no network (unless enabled) and a read-only
root. The kernel needs Docker daemon access only for this isolation — the
agents themselves never see Docker.

The persistent project filesystem is covered in [Observability](observability.html#sandbox-storage).

## The quality loop

In Agents → "Quality settings":

- **the judge model** — an installation-wide LLM judge: skill and agent
  evaluations, knowledge area normalization, supervisor passes and cases, the
  `worth_skill` verdict when mining skills, model selection in the draft
  wizards (with agreement calibration below the threshold the judge degrades
  and the gate goes red);
- **the schedule** — how often evaluation suites re-run.

Applying agent and skill proposals is gated on a green evaluation run — see
[Agents](agents.html#agent-evolution).

## External authentication

OIDC federation is supported (Keycloak, authentik and others): `docs/auth-federation.md`.
The scheme: discovery + a JWKS cache (key rotation without restart),
server-side sessions (`KERNEL_SESSION_TTL` 12h / `KERNEL_SESSION_ABSOLUTE_TTL`
7d), JIT provisioning with a default role and the `deny` role ("login
succeeds, access denied" until a manual mapping), claim mappings to roles and
projects. Every IdP config change writes an audit event.
