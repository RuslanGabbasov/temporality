---
order: 4
section: Working with the agent
title: Agents and teams
description: Creating and configuring agents — models, tools, sandboxes, evolution; agent teams as pipelines.
---

# Agents and teams

An agent is a named configuration: a model, a system prompt contract (purpose,
constraints, completion checks), tools, a sandbox, access grants. Managed on
the **Agents** page (`/agent-config`).

## Creating an agent

Three ways:

1. **From a template** — ready-made roles: Lead, Developer, Reviewer, QA, Analyst and more.
2. **From a description** — describe in plain words what the agent should do;
   the builder drafts the name, purpose, constraints and completion checks
   (as a draft until you confirm).
3. **Manually** — the full form: model, tools, MCP servers, sandbox.

## What is configurable

| Setting | What it gives |
|---|---|
| Model | The model name; if the default provider does not have it, the call is routed to a provider that declares it |
| Tools | Allowlist/denylist over the shared catalog (files, commands, web, knowledge, skills, artifacts, MCP) |
| Capabilities | Declarative permissions ("read files", "network", "modify files") — delegating agents and policies rely on them |
| Sandbox | The execution profile: image, memory, CPU, tmpfs limits, cache variables (see [Administration](administration.html#sandbox-profiles)) |
| Notification channels | Where `ask_human` questions are delivered |

Constraints and completion checks are part of the contract: they enter the
prompt and the result verification.

## Agent evolution

The agent can propose changes to its own configuration (the `agent_propose`
tool): the problem with evidence (run ids), the change and the expected
effect. The proposal lands in the agent's card as a draft.

Applying it goes through the **quality loop**: a proposal is applied only on
top of a fresh green evaluation run (golden cases from real runs, optionally a
calibrated LLM judge). A human decides; a proposal can never widen the agent's
powers. Details — `docs/agent-quality-loop.md`.

## Teams

A team is a slot manifest: roles (Developer, Reviewer, QA…), their bindings to
concrete agents, the execution protocol (pipeline/DAG), rework limits and a
budget. Built from a description ("A team with quality control: Developer
implements, Security and QA review, Reviewer accepts") and versioned.

Launching a team from chat or a trigger starts a DAG execution with review
gates — see `docs/agent-teams.md`.

## Proposals and versions

Every applied change creates a new version of the agent definition; the
version history with who and why is in the agent's card. Rollback is picking
a previous version.
