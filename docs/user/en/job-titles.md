---
order: 25
section: Working with the agent
title: Job titles & groups
description: A job title is the natural group address — invite every QA engineer to a call, or send the new instructions to all developers.
---

# Job titles & groups

Every user profile carries a **job title** — the person's function in the company («QA engineer», «Developer»). The platform role (admin/operator/viewer) is about system permissions; the job title is what the person does. That is the natural address when you need a function, not a named individual.

## Group asks

Fill the titles in the profiles (Users → the person's card → «Job title») — and the agent can:

- «Invite all QA engineers to a call» → everyone with «QA» in the title gets the question;
- «Send the new instructions to the developers» → it reaches everyone with «developer» in theirs;
- «Have someone from QA approve the rollout» → whoever answers first.

## How it works

The agent addresses the question to `title:<substring>` — delivery fans out to every active user whose job title matches (case-insensitive). Each person gets their own copy of the question in their own channel; **the first answer closes the question** — the rest see that it has been answered. Delivery order is predictable: seniority first, then name. If an execution identity forbids addressing someone, that copy is skipped with a note while the rest of the group still gets asked.

## Finding people

The agent finds people with `human_contacts`: by name, role, unit and **job title** — the contact cards show who would get the question and how (channel addresses are never exposed, delivery is automatic).

## Relation to the org tree

The job title complements units, not replaces them: `org:<unit>` addresses a team (every member of the unit), `title:<job title>` addresses a function across the whole org scope. Testers usually cut across teams — that is exactly what the title is for.

## Filling it in

Users → the person's card → «Job title». A single free-form field — the important part is naming the same function the same way for different people: «QA engineer» and «tester» are two different groups.
