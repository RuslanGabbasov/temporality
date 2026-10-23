---
order: 6
section: Knowledge and experience
title: Observability and storage
description: The dashboard, run traces, the experience timeline and the sandbox file storage.
---

# Observability and storage

## The dashboard

The **Dashboard** (`/dashboard`) is a health summary over a chosen time window
(hour/day/week):

- the health strip: runs, in flight, model failures, pending approvals;
- providers and models: calls, p50/p95, retries, tokens, cost;
- projects grouped by org unit;
- the **quality loop**: gates, blocks, the judge and its agreement;
- **sagas and tasks**: budget, tokens, time, human approvals (HAB/HWT —
  hover a metric for the explanation);
- **human factors**: attention (HAB), idle wait (HWT), flow efficiency —
  on the same row as storage and server;
- the **sandboxes** widget: live containers, integral load (CPU/memory/network), hover — who and which run, click — jump to the trace.

Fault tolerance and scale bounds (replay, workflow history,
Continue-As-New, quotas) — see `docs/resilience-and-scale.md`.

### The Supervisor tab and the /supervisor page

The supervisor model's monitoring surface (`docs/supervisor-model.md`):
failed-run diagnoses collapsed into same-root failure clusters, health
anomaly explanations, and the wave-B/C cases — knowledge contradiction
triage and hygiene, evolution proposals, skill candidates, user friction,
intake audit.

The queue works like this:

- the **tab badge** shows how many items need attention;
- **knowledge conflicts** expand into a side-by-side comparison of the two
  formulations (differences highlighted, cosine quoted); actions live on
  each side: **Invalidate** retires that knowledge; **Skip** closes the
  item unchanged; **Generalize…** replaces both with one broader statement
  (editable before it applies);
- **bulk actions**: row checkboxes, select-all, then 'Skip selected' or
  (when every selected row is a conflict) bulk invalidations;
- filter by case and project; resolved rows toggle;
- HAB coefficients and models are configured in Agents → Quality settings.

The run trace (Trajectory tab) shows the session's human factors next to
cost: ⏳ human wait (HWT, measured), 👁 human attention (HAB, estimate),
✋ human decisions.

## The run trace

The trace is a **tree of turns**, newest first. An expanded turn contains
everything that happened inside it: model calls, tool calls with arguments
and results, approvals, knowledge events (knowledge/recall/hints collapse
into a knowledge group with the aggregate "N learned · M extracted · K hints").
The window is 50 events; older ones load as you scroll down. Uncertain
operations are flagged and resolved through reconcile; cross-agent knowledge
disputes are shown as a banner.

A long model call is the first line of the trace: elapsed, tokens, speed and
the channel state ("the token channel did not connect" — a transport problem
up to the provider, not the model; "the provider does not stream (a single
chunk)" — a gateway is buffering the response). It can be nudged with the `↻`
button (the request is cancelled and the turn restarted) or cancelled. A
running `run_command` shows a live console tail right in its line; after
completion the output is available in the row details.

A stuck run is stopped with the **■ Stop** action in the list (click twice to
confirm): a forced workflow terminate that journals the terminal event — it
lands even when a graceful cancel cannot reach the stuck workflow.

## The experience timeline

The **Timeline** (`/experience`) is a temporal unfolding of knowledge and
runs: which knowledge appeared/changed, in which runs it was applied, where it
worked and where it misled. Grouped by scopes, with retrospective audit: "what
did the system know at the moment of that decision". An incident
retrospective and repeat-mistake tool.

## Sandbox storage

The **Storage** page (`/storage`) is a browser for the project's persistent
filesystem: the workspace (`/workspace` of the project — what agents see and
what survives runs) and the toolchain cache. Capabilities:

- a lazy tree with sizes and modification dates;
- preview: markdown with formatting, code with syntax highlighting, inline images;
- download, upload, delete of files and folders (deleting root areas is left to the background sweeper).

Project quotas and disk usage live on the dashboard, the "Storage" widget.
The full storage model — `docs/sandbox-storage.md`.

## Export and audit

All Journal events are append-only with idempotent ids; they reconstruct the
full picture of any run. Access audit (who read secrets, who changed
resources) lives in the corresponding administration sections.
