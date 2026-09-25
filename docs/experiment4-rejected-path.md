# Experiment 4 — rejected-path memory

Cross-run experience formation around a **rejected hypothesis**, on the live
stack (Temporal dev server, runtime, agent kernel, `mimo-v2.6-flash`).

The scenario: a `gatekeeper` CLI whose stale docs lure the agent toward the
`AUTH_TOKEN` environment variable while the actual v2 build only accepts a
`.token-file`. Run 1 must pay for that discovery empirically and store it.
Run 2 faces the same trap clean — the stored experience should be recalled
instead of re-derived. Run 3 flips the environment (v3: env var only) so the
stored experience becomes wrong and must be challenged and archived.

Fixtures: `examples/experiment4/` (gatekeeper v2, gatekeeper-v3, protocol).
Project: `gatekeeper-auth`.

## Runs

| run | workspace | outcome | turns / tool calls |
|---|---|---|---|
| `gatekeeper-20260925-01` | v2 | completed: AUTH_TOKEN rejected empirically, token-file verified, `remember` recorded | 9 / 15 |
| `gatekeeper-20260925-02` | v2 | **failed**: model endpoint EOF on first call (transient) — recall events still recorded | 0 / 0 |
| `gatekeeper-20260925-02b` | v2 (reset) | completed: knowledge recalled + injected, token-file set up directly, env-var tested only as a negative control | 11 / 16 |
| `gatekeeper-20260925-03` | v3 (flipped) | completed: both prior knowledges recalled, discovered obsolete, corrected conclusion recorded | 11 / 15 |

Runs 02b and 03 hit `max_turns=10` and finished via the kernel's forced
finale; the finale answers were complete.

## Knowledge

- `gatekeeper-20260925-01/knowledge/77` — "v2 authenticates solely via a
  non-empty `.token-file`; the AUTH_TOKEN hypothesis from docs/legacy.md is
  REJECTED…" with evidence refs to the failing and succeeding `run_command`
  operations. Later **invalidated** (environment changed).
- `gatekeeper-20260925-02b/knowledge/88` — end-to-end setup conclusion
  (working mechanism + rejected legacy hypothesis). Later **invalidated**.
- `gatekeeper-20260925-03/knowledge/86` — "v3 replaced token-file auth with
  AUTH_TOKEN env-var auth…" — the corrected, current truth (**proposed**).

Operator invalidation after run 3 (manual, human actor):

```
POST /v1/observations/knowledge/invalidate
  gatekeeper-20260925-01/knowledge/77  → knowledge.invalidated (method=manual)
  gatekeeper-20260925-02b/knowledge/88 → knowledge.invalidated (method=manual)
```

## Observed lifecycle (Experience Timeline fold)

One semantic cluster — `gatekeeper v2` (scope `gatekeeper`) — spans all four
runs; 4 activation links; every lifecycle moment below is a recorded event,
not narrative:

```
03:50:35  appeared    K77  (run 01)
03:51:56  recalled    K77  (run 02,  hint 70b5ca4b)   ┐ run 02 failed at the
03:51:56  injected    K77  (run 02)                    ┘ first model call
03:55:01  recalled    K77  (run 02b, hint 63f130d4)
03:55:01  injected    K77  (run 02b)
03:56:33  appeared    K88  (run 02b)
03:58:54  recalled    K77  (run 03, hint aa2fa754)
03:58:54  recalled    K88  (run 03, hint 36afeff7)
03:58:54  injected    K77  (run 03)
03:58:54  injected    K88  (run 03)
04:00:54  appeared    K86  (run 03)
04:02:54  archived    K77, K88  (operator invalidation)
```

Final knowledge state: K77 invalidated, K88 invalidated, K86 proposed.

## Findings

1. **Rejected-path memory works end to end.** Run 1 paid for the discovery
   (env-var hypothesis tested first, failed, alternative verified), stored it
   via `remember` with evidence operation refs. Run 2b opened with
   `hint.offered` + `knowledge.used` for K77 and never treated the env var as
   the primary path: access was granted at turn 7 via the token file; the
   single env-var execution (turn 8) was an explicit negative control after
   success, framed as evidence in the final answer.

2. **Turn count is not the metric.** Run 2b used 11 turns vs run 1's 9 — the
   flash model spent extra turns on negative controls (empty-file test,
   legacy check with backup/restore). The economy is epistemic, not
   mechanical: the dead-end hypothesis was never the plan of record. Future
   experiments should measure wasted *failing attempts before first success*
   rather than turns.

3. **Stale knowledge lies visibly.** In run 3 both K77 and K88 were recalled
   and injected — and then empirically broken by the v3 build. The agent
   re-verified, stated explicitly which assumptions no longer hold, and
   recorded the corrected mechanism (K86). The event stream shows the full
   `appeared → recalled → injected → contradicted → archived` arc, which is
   exactly the "how the agent's picture of the world changed" story the
   Experience Timeline is for.

4. **Failures are first-class history.** Run 02's transient model-endpoint
   EOF is permanently visible as `model.failed` + `run.failed` with its
   recall events intact — the timeline shows the failure lane without any
   special handling.

## Kernel / tooling notes

- `remember` reliably produced rich propositions when the prompt explicitly
  asked for "one proposition, including the rejected approach and the
  evidence operation ids".
- The lexical matcher (`lexical-entity-topic.v1`, ≥2 shared content tokens)
  recalled the right knowledge in every run: prompts shared
  {gatekeeper, token, access} with the propositions.
- Claim clustering in the Experience Timeline now groups by the same ≥2
  content-token overlap (previously all free-form claims collapsed into one
  `agent claims` bucket per role). The gatekeeper corpus folds into a single
  `gatekeeper v2` cluster across all runs and roles.

## Reproduction

See `examples/experiment4/README.md` for the fixture layout, run prompts and
workspace reset commands.
