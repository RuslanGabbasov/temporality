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
- **sagas and tasks**: budget, tokens, time, human approvals;
- the **sandboxes** widget: live containers, integral load (CPU/memory/network), hover — who and which run, click — jump to the trace.

## The run trace

Every run expands into an event feed: model calls (tokens, latency, stream),
tool calls with arguments and results, approval decisions, knowledge events.
Uncertain operations are flagged and resolved through reconcile. A long model
call shows signs of life (tokens/sec, first-token wait) and can be nudged or
cancelled.

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
