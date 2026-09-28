# Experiment 9 — experience evolution comprehension

Purpose: validate that a **human operator** can understand the evolution of agent
experience through the Temporality UI alone, without reading events or internal
state.

This experiment does NOT test the event model (that's experiments 4–8). It
tests whether the UI makes experience evolution **legible** to someone who
doesn't know the internals.

## Scenario

A small Go workspace (`webapp/`) with a configuration system. Three competing
hypotheses about how to set up authentication:

- **Hypothesis A** (env vars): `DATABASE_URL` env var is the correct way
- **Hypothesis B** (config file): `config.yaml` is the correct way
- **Hypothesis C** (command flag): `--db-url` flag overrides everything

The workspace evolves across 5 runs:

| Run | What happens | Knowledge lifecycle |
|-----|--------------|---------------------|
| 1 | Agent tests env vars — works | A proposed → confirmed |
| 2 | Agent tests config file — also works | B proposed → confirmed |
| 3 | Agent tests flag — works, concludes flag wins | C proposed, A and B challenged |
| 4 | New environment: env vars broken, flag broken, config file works | A invalidated, C invalidated, B validated |
| 5 | Environment restored: env vars work again | A resurrected, B stale |

## Questions the operator must answer

After viewing the Experience Timeline, the operator should be able to answer:

1. Which hypothesis appeared first? (A: env vars)
2. Which was confirmed first? (A)
3. Which died? (A and C in run 4)
4. Which resurrected? (A in run 5)
5. Why did the agent use stale knowledge? (B confirmed in run 2, used in run 3 even though C was newer)
6. What evidence caused the contradiction? (run 4: env vars broken, flag broken)
7. What is currently considered valid? (B: config file, because it works in all environments)
8. Which knowledge was actually used in subsequent runs? (hint_used events)

## Fixtures

- `webapp/` — a small Go project with a main.go that reads config from env/file/flag.
  Run 1–2: env and file both work. Run 3: all three work. Run 4: only file works.
  Run 5: env and file work again.

## Protocol

Project: `exp9-webconfig`. Runs: `exp9-20260928-01` through `exp9-20260928-05`.

Reset workspace between runs:

```sh
rm -rf .sandbox/webapp
cp -R examples/experiment9/webapp .sandbox/webapp
```

### Run 1 — form hypothesis A

Prompt:

> Investigate how this webapp authenticates to the database.
> Test each configuration mechanism you find (env var, config file, command flag).
> Report which one works.

Expected: agent tests DATABASE_URL → works → stores via `remember`.

### Run 2 — form hypothesis B

Same workspace (no reset).

Prompt:

> Test if config.yaml is also a valid way to configure the database connection.

Expected: agent tests config.yaml → works → stores via `remember`.

### Run 3 — form hypothesis C, challenge A and B

Prompt:

> Test if the --db-url flag overrides the other config mechanisms.

Expected: agent tests flag → works → concludes flag is authoritative. A and B challenged.

### Run 4 — environment flip (invalidate A and C)

New workspace where only config.yaml works.

Prompt:

> Run the webapp and check which configuration mechanisms still work.

Expected: env vars fail, flag fails, config.yaml works. A and C invalidated, B validated.

### Run 5 — environment restore (resurrect A)

Original workspace restored.

Prompt:

> Run the webapp again. Has the situation changed?

Expected: env vars work again. A resurrected, B still valid.

## Acceptance

The operator must answer all 8 questions above **from the UI alone** (no
reading events, no console). The UI must make visible:

- Knowledge lifecycle (proposed → confirmed → challenged → invalidated → resurrected)
- Contradiction evidence (what run/environment caused the flip)
- Stale knowledge (B used in run 3 even after C was newer)
- Supersession (B replacing A and C as most validated)
- Current state (what's considered valid now)

If any question requires reading raw events or knowing internals, the UI fails.