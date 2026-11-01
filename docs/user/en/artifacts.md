---
order: 8
section: Knowledge and experience
title: Artifacts
description: A registry of documents with human approval — coding policies, requirements and instructions agents can trust.
---

# Artifacts

An artifact is a document in a registry: a coding policy, feature
requirements, a release runbook, an estimate template, an audit checklist.
Only the reference enters the agent's prompt; the agent loads the body on
demand (`artifact_search` / `artifact_read` with read auditing). A large
policy does not eat the context yet is always there when needed.

The real value is trust. Every artifact carries a status, a version and a
review round, so "see policy X" means a human-verified document, not a
random draft: the agent proposes, the human approves.

![The Artifacts page](../assets/artifacts-page.webp)

The **Artifacts** page (`/artifacts`); details — `docs/artifact-registry.md`
and `docs/artifact-lifecycle.md`.

## Types and purpose

| Type | Where the body lives |
|---|---|
| Text document | Stored inside Temporality (markdown) |
| External URL | Proxied and cached by the kernel (corporate wiki, Confluence) |

The `kind` field splits one registry into two consumption patterns:

- **reference** — coding policies, instructions, checklists: what skills
  point at;
- **work** — requirements, specs, design decisions: work products authored
  and approved inside tasks and sagas.

A shipped requirement can later become the reference document of a new
skill — the same record with a different `kind`, after a new approval round.

## Statuses and versions

The lifecycle is tracked per **version**:

| Status | Meaning |
|---|---|
| **draft** | A draft: created by a human or an agent, editable. Readable (reuse between agents is half the value), but flagged as unverified material |
| **in_review** | Under review: edits are locked |
| **approved** | Approved by a human: immutable, available for skill references |
| **rejected** | Returned with comments: rework and resubmit |
| **superseded** | Replaced by a newer approved version |

Editing an approved document always means a **new version and a new review
round**. The approved version keeps serving agents and skills while the
revision goes through review. Skills reference the artifact id and always
resolve to the latest approved version.

## Approval flow

- **A human approves — nobody else.** Self-approval is forbidden at the API
  level: the decision belongs to a named person, not to an agent or a "sum
  of signals".
- **The review round.** The author submits the artifact for review
  (idempotent per version; the `ARTIFACT_SUBMIT_PER_DAY` quota per run keeps
  a flooded run out of the reviewers' queue) and names the reviewers: a
  person, an org unit, a role or the project owner. A unit expands to its
  active people at submit time; the unit's head is a reviewer too.
- **Modes.** `all` — the round is approved when everyone votes yes (the
  first "no" closes it as rejected); `any` — the first response decides.
- **Decide from your messenger.** Each reviewer gets a notification in their
  channel with a link to the artifact card and can decide right in the chat:
  the first word decides (`approve`/`lgtm` — yes, `reject` — no), the rest
  becomes the comment. An ambiguous reply keeps the round open — the bot
  asks for the proper form.
- **Rework in a loop.** A rejection returns the artifact to the author with
  the reason: the human edits it themselves (in the UI) or the agent applies
  the fixes (`artifact_update`) and resubmits. Rounds are unlimited and each
  one leaves a trace in the review history.
- **Reminders and escalation.** A round silent past `ARTIFACT_REVIEW_TIMEOUT`
  (72 hours by default) sends one reminder to the reviewers; after another
  timeout the escalation reaches the artifact org unit's manager (or an
  administrator). A value of `0` disables the sweep.
- **Idempotency.** A repeated approve/reject of the same round is a 409 with
  no state change; a decision is recorded exactly once. The requester can
  always decide their own round.

## The approvals inbox

The Artifacts page opens on the **My approvals** tab — the queue of open
rounds addressed to you: the queue leads, the catalog is the browsing
surface. While there is something to decide, the Artifacts menu item carries
a badge with the count. Catalog filters cover status, age, tags and the
author (a person or an org unit in one field); an `?id=…` link opens a
specific artifact.

## Example: a heavily agreed document

How agents and humans build a document that survives several review rounds
(a real scenario — requirements for a UI rework):

1. **The task.** The user asks in chat: "Draft the requirements for the
   trace panel rework and agree them with the team." The agent registers a
   saga (`saga_register`) that ties the whole story together: requirements →
   approval → implementation runs.
2. **The draft.** An analyst agent creates a `kind=work` artifact
   (`artifact_create`, with `saga_id`) and fills it over iterations. Other
   agents read the draft in their own runs (`artifact_read` honestly reports
   `draft`) and extend it with comments — reuse works before approval, but
   the draft is always flagged as unverified.
3. **First round.** The agent submits the document for review
   (`artifact_submit`): reviewers are `org:backend` and `role:security`,
   mode `all`, the note carries the context and what to check. Reviewers get
   notifications in their messengers.
4. **A rejection with substance.** The security reviewer replies in the
   thread `reject no secrets section` — the round closes as rejected and the
   document returns to the author with the reason.
5. **Rework.** The agent applies the fixes (`artifact_update`) and resubmits
   — a new round; both rounds stay in the review history with verdicts and
   comments.
6. **Approval.** Every reviewer replies "approve" — version 1 becomes
   `approved` and immutable.
7. **Working off the document.** A "Feature delivery" skill links the
   artifact (the skill editor's Artifacts tab, or `artifact_refs` in
   `skill_propose`); executor agents load the approved version on demand —
   every read is journaled, so you can see who relied on the document in
   which run.
8. **A new revision.** The requirements change: an approved document can
   only be edited as a new revision (`artifact_update` with
   `new_version: true`) — a version-2 draft lives next to approved version
   1; the repeated approval promotes it to `approved` and marks version 1
   `superseded`. Agents keep working on the live version without pauses.

This is how a requirement survives several refinement iterations while
keeping its history: who proposed it, who rejected it and why, what changed
between versions.

## Relations

- **Saga** — an artifact can belong to a long-running effort (requirements
  for a feature inside a saga): the link gives end-to-end tracing from the
  requirement to the implementation runs.
- **Skill** — a skill's manifest references artifacts as reference material;
  only approved artifacts make it into skills.
- **Project / department / organization** — visibility scope: a
  company-wide policy is visible everywhere, a project's only inside the
  project.

## Agent tools

| Tool | What it does |
|---|---|
| `artifact_search` | Semantic search; status and kind filters |
| `artifact_read` | Chunked reads with auditing: who read what, in which run |
| `artifact_create` | Creates a draft (`kind=work` for work products) |
| `artifact_update` | Edits a draft or a rejected version; with `new_version: true` opens a revision of an approved one |
| `artifact_submit` | Sends for review: reviewer list, `all`/`any` mode, note |

An approved document is locked for direct edits: the tool reports that a new
revision is needed. An agent can never make the approval decision — only a
human can.
