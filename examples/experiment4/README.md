# Experiment 4 — rejected-path memory

Purpose: demonstrate cross-run experience formation around a **rejected
hypothesis**, not around a successful fix.

Scenario: a small `gatekeeper` CLI where stale docs lure the agent toward the
`AUTH_TOKEN` environment variable, while the actual v2 build only accepts a
`.token-file`. Run 1 must empirically test the env-var hypothesis, reject it,
discover the real mechanism and store the conclusion via `remember`.
Run 2 re-instantiates a clean workspace with the same trap; the knowledge
hint from run 1 should be recalled and the paid path should not be repeated.
Run 3 flips the environment (v3 build: env var only, no token file), so the
stored knowledge becomes wrong and must be challenged/invalidated.

## Fixtures

- `gatekeeper/` — v2 workspace used for runs 1 and 2 (docs/legacy.md is the lure).
- `gatekeeper-v3/` — v3 workspace used for run 3 (mechanism flipped).

## Protocol

Project: `gatekeeper-auth`. Runs: `gatekeeper-20260925-01/02/03`, max_turns 10.

Reset workspace between runs:

	rm -rf .sandbox/gatekeeper
	cp -R examples/experiment4/gatekeeper .sandbox/gatekeeper

For run 3:

	rm -rf .sandbox/gatekeeper
	cp -R examples/experiment4/gatekeeper-v3/. .sandbox/gatekeeper/

### Run 1 — form the experience

Prompt:

> Investigate how the gatekeeper CLI in this workspace authenticates access.
> The README and docs/legacy.md disagree about whether the AUTH_TOKEN
> environment variable is supported. Determine which mechanism the current
> build actually uses: test the AUTH_TOKEN hypothesis empirically with
> run_command, then verify the alternative. When done, record your conclusion
> with the remember tool as one proposition, including the rejected approach
> and the evidence operation ids.

Expected observable history: tool attempts with `AUTH_TOKEN` (failure), the
token-file discovery (success), then `knowledge.proposed` via `remember`.

### Run 2 — recall it

Same fixture, clean reset. Prompt:

> The nightly job needs authenticated gatekeeper access. Set up gatekeeper
> token authentication and verify access is granted end to end. Record your
> final conclusion with the remember tool.

Expected: `hint.query` / `hint.offered` / `knowledge.used(hint_id)` at run
start; no env-var attempt in the trajectory; direct token-file setup.

### Run 3 — environment flips

Workspace replaced with the v3 build. Prompt:

> The gatekeeper was upgraded to v3. Re-enable authenticated access for the
> nightly job and verify it works. Record your conclusion with remember; if
> prior assumptions no longer hold, say so explicitly.

Expected: the recalled knowledge fails, the agent re-empiricizes, records a
corrected proposition. Afterwards the operator invalidates the stale
knowledge via `POST /v1/observations/knowledge/invalidate`.

The full narrative with observed events is recorded in
`docs/experiment4-rejected-path.md`.
