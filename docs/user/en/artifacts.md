---
order: 8
section: Knowledge and experience
title: Artifacts
description: A registry of reference documents — coding policies, runbooks and templates agents load on demand.
---

# Artifacts

An artifact is a reference document in a registry: a coding policy, a release
runbook, an estimate template, an audit checklist. Only the reference enters
the agent's prompt; the agent loads the body on demand (`artifact_search` /
`artifact_read` with read auditing). A large policy does not eat the context
yet is always there when needed.

The **Artifacts** page (`/artifacts`); details — `docs/artifact-registry.md`.

## Artifact types

| Type | Where the body lives |
|---|---|
| Text document | Stored inside Temporality (markdown) |
| External URL | Proxied and cached by the kernel (corporate wiki, Confluence) |

## Approval flow

An artifact exists in two statuses: **draft** (an agent created it — say,
requirements from an analysis) and **approved** (a human edited and confirmed
it). Only approved artifacts are referenced from skills and prompts: "see
policy X" means a human-verified version.

A typical cycle: an analyst agent drafts the document → sends it to a human
for approval (`ask_human` or an artifact notification) → the human edits it
themselves or with the agent → approves → the document becomes available to
all executor agents as a link from a skill.

## Relations

- **Saga** — an artifact can belong to a long-running effort (requirements for a feature inside a saga).
- **Skill** — a skill's manifest references artifacts as reference material.
- **Project / department / organization** — visibility scope: a company-wide policy is visible everywhere, a project's only inside the project.

## Tool support

Agents search artifacts semantically (`artifact_search`) and read them in
chunks with auditing (`artifact_read`): every read is journaled — who read
what, in which run.
