---
order: 26
section: Learning
title: Learning and the mentor
description: How Temporality teaches people: contextual «?» hints, the learning center, interactive lessons and capability mastery verified against the factual configuration.
---

# Learning and the mentor

Temporality observes how agents work and returns verified experience to later runs — and it helps people learn the system itself the same way. The mentor (the same supervisor) explains the interface in the context of your organization, walks you through short interactive lessons and tracks which capabilities you have already mastered. Two principles: learning happens **on your real configuration**, no sandbox or demo data, and mastery is verified against facts, not clicks.

## The «?» icon in the interface

A quiet «?» indicator sits next to teachable elements. It does not blink or demand attention — it just knows there is material nearby.

Hover it for a short explanation. Click it and the mentor opens a card explaining how the element works **in your installation**: not an abstract description of delegation, but «you have N agents configured, delegation is enabled here». You can ask a follow-up question in the card — the mentor answers in the same context. `Esc` or the cross closes it.

## The learning center

The «Learning» section in the main menu is the mentor's workplace: program, suggestions, capability mastery and the lesson catalog.

**Learning program.** «Build my program» starts the planner: the mentor reads the factual organization configuration (how many agents, whether a reviewer exists, whether approval rounds happened) and assembles the lessons that apply here. Lessons irrelevant to your configuration stay out. If the mentor model is unavailable, the program is built deterministically — lessons by priority.

**Mentor suggestions.** When the organization configuration shows a capability is available but not yet mastered, the mentor offers a short lesson. «Not now» is a first-class answer: the suggestion will not keep coming back. Suggestions (capped at 20 per pass, at most one pass per 45 minutes) are not notifications — they are quiet cards in the learning center.

**Capabilities and mastery.** Every capability carries a mastery badge:

- **Not started** — the mentor has not checked anything yet;
- **Introduced** — the lesson was completed;
- **Practiced** — the mentor sees actual use in the configuration or the work;
- **Verified** — confirmed by factual outcomes (for example, an approval round was completed).

A capability that does not apply to the current configuration (for example, artifact approval with no artifacts around) is marked as not applicable and stays out of the way.

**Lesson catalog** — every shipped lesson is always available, program or not.

## A lesson

«Take the lesson» opens a floating guide panel. It survives page transitions — the lesson leads you across the interface instead of locking you into one section.

A typical lesson takes a few minutes:

1. **Explanation** — why the capability matters in the organizational model.
2. **Navigation** — a button opens the right page.
3. **Highlight** — the relevant element is outlined with an explanation.
4. **Practice** — you do the action yourself (enable delegation and save, submit an artifact for approval) and press «Done».
5. **Verification** — the mentor checks the factual configuration: is delegation really enabled? does the reviewer agent exist? is an approval round open?
6. **Summary** — a short wrap-up.

The panel can be minimized (`—`), paused (`⏸`) or aborted (`✕`). The lesson session lives on the server: close the browser, reboot the machine — the lesson resumes from the same step via «Resume».

## Mastery is counted from facts

Completing a lesson and mastering a capability are different things. A completed lesson gives «Introduced». «Practiced» and «Verified» are only awarded when the configuration or the organization's actual work shows it — and an earned level is never downgraded: finishing the lesson again will not undo verified delegation practice.

## For administrators

New system capabilities appear in the learning center automatically: lessons and descriptions ship with the feature — there is no separate content to maintain. Change the mentor's background pass cadence (configuration vs mastery comparison, suggestion generation) with `KERNEL_LEARNING_NEEDS_INTERVAL` (45 minutes by default). Everything the mentor does — hints opened, lessons started, suggestion verdicts — lands in the access audit.
