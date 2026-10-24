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

An artifact goes through four statuses: **draft**, **in_review** (sent for
review), **rejected** (rejected with a verdict) and **approved**. Only
approved artifacts are referenced from skills and prompts: "see policy X"
means a human-verified version.

The review cycle: the author submits the artifact for review (idempotent per
version, the `ARTIFACT_SUBMIT_PER_DAY` quota per run) → reviewers get a
notification → the decision is made by **a human only** (self-approval is
forbidden at the API level). Reviewers are configured with the `all` mode
(everyone must decide) or `any` (one is enough). A rejection returns the
artifact to the author with a reason.

Decide in the web UI (the Artifacts page) or right in your messenger: reply
to the notification — the first word decides (`approve`/`reject`, the rest
becomes the comment). If a round sits unresolved past
`ARTIFACT_REVIEW_TIMEOUT` (72 hours by default), reviewers get one reminder;
after another silent timeout the escalation reaches the artifact org unit's
manager (or an administrator).

A typical cycle: an analyst agent drafts the document → sends it for review →
the human edits it themselves or with the agent → approves → the document
becomes available to all executor agents as a link from a skill.

## Relations

- **Saga** — an artifact can belong to a long-running effort (requirements for a feature inside a saga).
- **Skill** — a skill's manifest references artifacts as reference material.
- **Project / department / organization** — visibility scope: a company-wide policy is visible everywhere, a project's only inside the project.

## Tool support

Agents search artifacts semantically (`artifact_search`) and read them in
chunks with auditing (`artifact_read`): every read is journaled — who read
what, in which run.
