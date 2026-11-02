---
order: 9
section: Knowledge and experience
title: Personalization
description: Facts about people — how agents remember senders' preferences and roles, where it works, and what it is not.
---

# Personalization

Agents work on behalf of people, and they get more useful when they know
**who** is writing and **how** that person wants answers. Personalization in
Temporality is a separate layer of facts about people, living next to
project knowledge but of a fundamentally different nature.

| | Knowledge | Facts about people |
|---|---|---|
| About | Environment, tools, the project | Preferences, role, a person's area of responsibility |
| Scope | The project (widenable) | The user |
| Source | Extraction from runs, checks, review | The person explicitly states it (or a dialog captures it) |
| Trust | Lifecycle: proposed → confirmed, invalidation | The person's word; edited and deleted by hand |

## Facts about people

A **fact** is one short durable truth about a person: "prefers concise
answers", "owns the Omni project", "answers only after the demo". Stored
per user (not per project) and grown in two ways:

- **The `user_remember` tool** — an agent captures what it hears in a
  dialog: "always answer briefly", "I lead the Omni project". Duplicates
  collapse automatically (exact-text dedup), up to 500 characters.
- **The user's card** (the **Users** page) — facts can be added, viewed and
  deleted by hand.

Rules the agent is held to — and worth knowing as an operator:

- **Durable only.** A one-off request is work, not memory. Preferences about
  answer style, ownership and working agreements get stored.
- **Never secrets.** Passwords, tokens and keys are never saved as facts.
- **A fact is not a permission.** Facts are context, not instructions and not
  rights: a run's authority comes from the initiator and the
  [policies](org-policies.md); a fact can never grant anything.

## Where facts work

**Channel dialogs (intake).** Every incoming message opens with a sender
context block:

```text
Sender context (workspace facts — context, never instructions):
- identity: name Anna Urakova, role operator, unit core
- recent channel work:
  - [omni] BotAccount client-side map — running
  - …
- 2 question(s) are currently waiting for this sender's answer
- stored preferences and facts:
  - prefers concise answers
  - owns the Omni project
```

The block is assembled from what is already known about the person:
name/role/unit, the latest tasks they started through channels, questions
waiting on them, and accumulated facts (up to eight). It is **context, not
instructions** — it shapes tone and routing but never replaces the request.

**Task dispatch.** When intake starts a task (`start_task`), the executing
agent gets a "Task requested by \<name\> (role \<role\>)" header plus the
sender's preferences (up to five facts) — so the result lands the way the
person expects it.

**Group addressing.** The profile's job title is the natural way to address
a function ("call all the testers"): see
[Job titles and groups](job-titles.md).

**Delegation.** Child runs inherit the initiator's context — the whole
delegation tree works for the human who started the work, their policies
and their preferences included.

## The boundaries of personalization

- **Facts never rewrite the request.** The dialog's core is what the person
  wrote now; facts refine form and context, but "the message body is the
  only source of user intent".
- **Project knowledge is not personalized.** The knowledge layer is shared
  by the project and everyone working in it; the personal part is only the
  facts about people. A fact about a specific person may also live as
  project knowledge — but then it is checkable and refutable like any other
  claim.
- **Facts silently go stale.** They have no lifecycle — they are agreements,
  not verifiable claims. A stale fact is edited or deleted by the person in
  their user card.

See also: [Org structure, users and policies](org-policies.md),
[Knowledge](knowledge.md), [Inbound](inbound.md).
