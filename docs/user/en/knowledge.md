---
order: 5
section: Knowledge and experience
title: Knowledge
description: How the agent accumulates experience — the knowledge lifecycle, the area map, dossiers, contradictions, bitemporality.
---

# Knowledge

Knowledge is the verified layer of experience: statements about the
environment, tools and solutions, extracted from real trajectories. Unlike a
"memory dump", every piece of knowledge carries an author, provenance, a
status and a change history. The **Knowledge** page (`/knowledge`) serves both
humans and agents (the `knowledge_explore` tool).

## Lifecycle

```
proposed → confirmed → (challenged → corrected | superseded | invalidated)
```

- **Proposed** — extracted from a run, not yet verified.
- **Confirmed** — applied and worked (or confirmed by a human).
- **Challenged** — a contradiction was found; under review.
- **Corrected / Superseded / Invalidated** — terminal outcomes with the
  history preserved: who, when and why.

"Alive" knowledge is what the system is ready to hint to the agent. Stale
("at risk") items are shown with a marker and re-evaluated.

## The area map

A treemap groups knowledge by topics and entities: tile size — the amount of
knowledge, color — the dominant state, a red badge — contradictions. Clicking
an area applies a sticky filter to the knowledge list.

The **Tidy up** button runs the judge model (the same one the quality loop
uses): it merges duplicates and variants ("playwright", "playwright e2e") into
canonical areas and hides garbage (file names and the like). Knowledge records
are not modified — only the display "lens" is.

## Search and filters

- **Compact filters**: state, visibility scope, type (claim/observation), origin, topic, entity.
- **Semantic search** — a question in plain words; results explain *why* they matched.
- **Bitemporal filters** — look at the base "as it was": at an event date or as of a moment of knowledge (who knew what when). Essential for incident retrospectives.

## The knowledge dossier

Clicking a knowledge item opens its dossier: the proposition, status and
transition history, primary sources (links to specific run events), relations
to other knowledge (supports/contradicts/derived from), usage statistics.

From the dossier: **challenge**, **correct**, **invalidate** — with a reason
and evidence. You can also challenge or invalidate straight from the run
trace — the ⚑ button on the event that references the knowledge: the doubt is
captured right where you noticed it. These are human actions; the agent flags
doubts, but you move the lifecycle.

## Contradictions

The **Contradictions** tab collects pairs of knowledge items that state
different things about the same subject: a side-by-side comparison and a
resolution (keep one, correct, both are stale).

## Research sessions

The current question, filters and selected area are saved as a **session** you
can return to later. The agent explores the base too, through the
`knowledge_explore` tool (orient/search/expand/trace/compare/synthesize), and
its research runs appear in a separate tab.

The mechanics in depth — `docs/knowledge-model.md`, `docs/knowledge-evolution.md`,
`docs/human-knowledge-explorer.md`.
