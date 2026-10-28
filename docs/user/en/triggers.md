---
order: 10
section: Automation
title: Triggers
description: Start agents on a schedule, by webhook or on system events.
---

# Triggers

Triggers start agents without a human: on a schedule, by an inbound webhook or
on a system event. The **Triggers** page (`/triggers`); the mechanics —
`docs/triggers-and-escalations.md`.

## Cron trigger

A crontab-format schedule + project, agent and prompt. The classics: "every
morning at 09:00 — sweep monitoring and summarize incidents", "every Friday —
a sprint report draft". Every fire is a normal run: with a trace, a budget and
approvals.

## Webhook trigger

An external system POSTs a task in: GitLab/GitHub CI failed, a request
arrived, a ticket moved. Each trigger gets its **own token** (the generate
button in the form) — no shared keys; input over HTTPS only, signatures are
verified.

## Event triggers

A Journal event listener: "when knowledge goes challenged", "when a run
fails", "when an artifact draft is created" — start a triage agent. The event
constructor: type, source, data filters.

## Execution identity

On whose behalf the trigger starts tasks: a trigger is bound to an execution
identity — run quotas and project visibility are counted against it. Channel
tasks keep counting against the sender.

## Observing

![Triggers](../assets/trigger.png)

Every fire is visible in "Runs" as a normal run (with a source marker), and
the firing history is in the trigger's card. A failed trigger run is still a
run: open it, understand the cause, restart it.
