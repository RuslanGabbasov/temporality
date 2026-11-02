---
order: 12
section: Administration
title: Org structure, users and policies
description: The department tree, roles and grants, projects and visibility. Org Policies — every dimension, inheritance and the run's effective policy.
---

# Org structure, users and policies

Three access layers work together in Temporality: **who the person is**
(user, role, job title), **where they stand** (org structure, units,
projects) and **what they are allowed to do** (policies on units). Managed
on the **Organization** page (`/org`, admin) and **Users** (`/users`, admin).

## Org structure

The department tree expresses how the company is organized — and configures
access through it: by structure, not by scattered lists.

- **Unit** (`OrgUnit`) — a department: a root or a child of another unit,
  with a stable id and a `kind` (department, team, direction).
- **Unit head** — the explicit manager edge: the escalation target and the
  default addressee for team requests. Heads resolve bottom-up: a unit
  without its own head inherits the nearest ancestor's.
- **A project binds to one unit** — inheriting its policies and visibility
  scope. One project, one binding; the unit's policies act through it.
- **A user belongs to one unit** (`org_unit_id`). An empty unit = "outside
  the structure": sees more (transition semantics while you roll out —
  assign units to narrow down).

## Users and roles

The **Users** page (`/users`): creation, role, job title, unit, bearer
tokens, password, notification channels, the active toggle.

| Field | Meaning |
|---|---|
| **Role** | Platform permissions: `reader < writer < operator < admin` (alias `viewer` = `reader`) |
| **Job title** | The person's function ("QA engineer") — the natural address for group questions, see [Job titles and groups](job-titles.md) |
| **Unit** | Position in the org structure — leads to the unit's policies |
| **Projects** | Visibility scope: only allowed projects (or all with `*`) |

| Role | Can |
|---|---|
| reader | Read: dashboard, traces, knowledge, timeline |
| writer | Start tasks, create project resources |
| operator | Approvals, reconcile undecided operations, knowledge invalidation |
| admin | Users, providers, channels, org structure, everything else |

**Org-unit role grants** (`org_unit_role`) upgrade a person's role locally:
a grant of `operator` on a unit applies to the unit and its whole subtree
without changing the global role — "senior engineer of their department"
gets operator rights only in their part of the structure.

Authentication is password-first (the "Password | API token" form); tokens
serve service accounts and scripts; external SSO (OIDC) with JIT
provisioning — [Administration → External authentication](administration.md).

## Policies (Org Policies)

A policy is a set of restrictions attached to an org unit (or to the whole
installation when no unit is set). Inheritance flows top-down: a run is
governed by the **merge of every policy** in its position — global, the
project's units and the initiator's unit — by the rule "can only narrow".
A child policy can never loosen an ancestor's restriction.

| Dimension | Restricts |
|---|---|
| `allowed_models` | Which models the agent may use (name list; empty = any) |
| `allowed_mcp` | Which MCP servers are available |
| `max_tokens`, `max_budget_usd`, `timeout_seconds` | Per-run ceilings: tokens, dollars, time |
| `network_mode: deny` | Disables the sandbox network entirely |
| `sandbox_mode: read_only` | Read-only sandbox |
| `approval_mode: tools` | Consequential tools require human approval (+ behavior monitoring) |
| `egress_allowlist` | The external-destination baseline (unioned with the agent's own list) |
| `intake_mode: deny` | Bans channel-initiated tasks for the subtree |
| `filesystem: read / write / deny` | Filesystem areas: read, write (write is always inside read), absolute deny — see below |

### File rules

`filesystem` uses glob masks over project paths:

```json
{
  "filesystem": {
    "read":  ["**"],
    "write": ["src/**", "tests/**", "docs/**"],
    "deny":  ["secrets/**", "deploy/production/**"]
  }
}
```

- `read` / `write` — allowed areas; **write never exists without read**:
  the writable area is the intersection of write and read.
- `deny` — an absolute ban on reading and writing; unions across merge
  levels (a child policy cannot un-deny an ancestor's ban).
- Empty/unset means no restriction. Masks: `**`, `a/b/**`, `*`, `?`, `[...]`
  over normalized relative paths with `/`.
- Enforcement happens at execution: the kernel's built-in file tools and
  sandbox commands (Landlock) get the same policy — `run_command` cannot
  bypass it.

### The run's effective policy

The policy is **frozen at run start** and never changes mid-run: policy
changes do not raise a running process's privileges (new runs take the
current one). The journal keeps the snapshot version (`policy_version`) —
it shows which rules authorized the run.

**Delegation never widens access**: child runs inherit the initiator's
security context — the whole delegation tree works with the permissions of
the human who started the work, regardless of the agents' Lead/Coder/
Reviewer roles.

**Automated runs** (webhooks, schedules, triggers) act as an **execution
identity** — a service identity with its own allowed lists and
human_targets re-checked at every critical step. Policies resolve from the
identity's org position.

### In the interface

- **Organization** (`/org`): the tree, users on units, policies on units,
  role grants. A policy card shows its sources — which level restricted
  what.
- **Organization → Effective policy**: the merged result for a unit with
  every ancestor's contribution.

See also: [Personalization](personalization.md) — how agent knowledge
accounts for people; internals — `docs/org-structure.md`.
