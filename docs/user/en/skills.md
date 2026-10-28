---
order: 7
section: Knowledge and experience
title: Skills
description: Living skills — reusable agent procedures with a contract, versions and quality evaluations.
---

# Skills

A living skill is a reusable agent procedure: what it does, when it applies,
which tools it needs, how to verify the result. Unlike a prompt, a skill is
versioned, has a contract and a suite of quality evaluations. The **Skills**
page (`/skills`); details — `docs/living-skills.md`.

## The skill contract

The skill switch is the toggle in the bottom-right corner of the mini-card:
a disabled skill is never served to agents (like an unapplied draft), its
content stays untouched. Every flip is recorded in the audit log.

![The skill editor](../assets/skill-edit.webp)

The manifest (`SKILL.md` + metadata) describes:

- **the procedure** — the steps the agent performs;
- **preconditions** — when to apply it, what must be on input;
- **required tools and rights** — a skill cannot get more than the agent has;
- **postconditions and checks** — how to verify completion;
- **artifacts** — reference documents (policies, instructions) the agent loads
  on demand via `artifact_read`, not from the prompt.

## How one is created

1. **From a description** — describe the repeating procedure in plain words;
   the builder drafts the skill referencing only tools that actually exist.
2. **Import** — a folder or archive in the [agentskills.io](https://agentskills.io) format.
3. **Manually** — the manifest editor.
4. **Mining** — the supervisor finds repeated tool-call trigrams across runs
   (a deterministic gate filters out read-only sequences and back-to-back
   repeats), the judge issues a `worth_skill` verdict with a rationale; the
   candidate appears in the queue on the Skills page as a pre-filled draft —
   all that is left is to read and approve or reject it.

A new skill is a draft (0.x). It reaches production after human review.
Badges in the skill list show the unfinished work: `draft` — drafts,
"↺ N" — pending draft revisions; applying or rejecting a version is a
human decision.

## Quality evaluations

![Quality evaluations](../assets/skill-evaluation.webp)

Golden cases attach to a skill: input → expected answer. An evaluation run
(manual or scheduled) checks answers with patterns and an LLM judge with
agreement calibration. The result is visible in the skill's card and on the
quality dashboard.

## Evolution

The skill lives: the agent can propose a change (the `skill_propose` tool)
based on repeated friction. Versions accumulate; applying a new version goes
through the same quality loop. The tools `skill_history`, `skill_diff`,
`skill_executions` give the full picture: who changed what and when, where the
skill ran and what it "remembered" from executions (`skill_memory`).

## How the agent works with skills

In a run the agent has `skill_search` (find a match), `skill_inspect` (read
the contract), `skill_validate` (check applicability). The skill is loaded
into context on demand, not as a permanent prompt.
