---
order: 24
section: Working with the agent
title: Evolution & quality
description: How agents propose changes to their own configuration, how the evaluation gate verifies the effect, how to roll a version back — and where evaluation cases come from without hand-writing them.
---

# Evolution & quality

Agents observe their own friction and propose changes to their configuration (rules, completion checklists) via `agent_propose`. A proposal changes nothing by itself — a human reads it and decides. Around that decision there is a measurable loop: the **evaluation gate** verifies the change does not break behaviour, **versions** make every decision revertible, and **effort regressions** show whether the work got cheaper or more expensive.

## Proposals and the gate

Applying an agent's proposal requires a fresh green evaluation run on the proposal's base version. An evaluation run is a suite of cases: the model gets typical tasks and its answers are checked (exact substrings or an LLM judge with calibration).

![The evaluation gate](../assets/agent-evolution.webp)

Three honest outcomes:

- **Green** — a fresh green run: the proposal is applied, the version increments, a snapshot is stored.
- **Red / suite not ready** — applied **with an override**: you write a justification, it lands in the audit log next to the apply. You have read the proposal and take the decision; the system requires the trail.
- **Stale base** — the proposal needs rebasing (the agent will do it on request).

## Rolling versions back

Every definition change is a new immutable snapshot with the fully compiled prompt. Any previous version can be restored with one click: the snapshot returns **as a new version** — history is append-only, and the rollback itself is revertible the same way.

![Versions and rollback](../assets/agent-evolution.webp)

## Where cases come from

Nobody hand-writes cases:

1. **Production harvest.** Completed runs automatically become evaluation-case candidates (the task is the input, the answer or the human's correction is the reference). Candidates go through review: accept one by one or the whole batch with «Accept all».
2. **Supervisor warm-start.** If an agent's suite holds fewer cases than the gate's minimum, the supervisor drafts three representative cases from the agent's purpose and recent successful runs — accept them, edit them, or delete them.
3. **From proposal to case.** An evolution proposal ships with a draft regression case — if it is applied, the gate already checks the exact failure that motivated the proposal.

<!-- Screenshot: agents-candidates.webp — add once captured (the candidate queue with «Accept all») -->

## Effort is a quality signal too

An answer can get better while the work gets more expensive. Per definition version the system tracks average turns and tokens of completed runs; a meaningful growth is flagged as an effort regression («more turns / +N% tokens vs vN»). Evolution must justify the cost, not just the quality.

## Overrides are honest

Bypassing the gate is not a loophole but a documented decision: every apply without a green run is recorded in the audit with the author and justification, and the override share is visible in the quality report. The loop is fail-closed by design — a traceless bypass is technically impossible.
