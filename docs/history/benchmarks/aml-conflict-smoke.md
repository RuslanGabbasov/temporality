# AML conflict experiment — 2026-09-19

Dump: `benchmarks/aml-conflict-smoke.json`

## Configuration

- model: `deepseek/deepseek-v4.1-flash` (temperature 0, max_output_tokens 16384, reasoning "low")
- fixture: conflict; arms: B, D; sessions: 2; tasks per session: 2; flip at session: 1
- arm mapping: A = no memory; B = semantic ranking only; C = semantic+temporal ranking; D = C + pre-action activation + guardrail + adaptive feedback with cause-level attribution

## Per-arm totals

| arm | tasks | success | steps | calls | failed | repeated | tokens | hints | reused | validated | contradicted | unresolved |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| B | 4 | 4 | 10 | 9 | 3 | 0 | 9821 | 5 | 3 | 2 | 1 | 0 |
| D | 4 | 4 | 12 | 11 | 4 | 0 | 11519 | 1 | 1 | 1 | 0 | 0 |

## Per-session detail

| arm | session | epoch | success | failed | repeated | steps | tokens | hints | reused | validated | contradicted |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| B | 1 | 2 | 2/2 | 3 | 0 | 6 | 6137 | 1 | 1 | 0 | 1 |
| B | 2 | 2 | 2/2 | 0 | 0 | 4 | 3684 | 4 | 2 | 2 | 0 |
| D | 1 | 2 | 2/2 | 2 | 0 | 7 | 6782 | 0 | 0 | 0 | 0 |
| D | 2 | 2 | 2/2 | 2 | 0 | 5 | 4737 | 1 | 1 | 1 | 0 |

## Post-flip window (from session 1)

| arm | tasks | success | failed | repeated | contradicted | unresolved | tokens |
|---|---:|---:|---:|---:|---:|---:|---:|
| B | 4 | 4 | 3 | 0 | 1 | 0 | 9821 |
| D | 4 | 4 | 4 | 0 | 0 | 0 | 11519 |

## Per-task answers

| arm | session | task | env | ok | steps | failed | stopped | answer |
|---|---:|---|---|---|---:|---:|---|---|
| B | 1 | P1 | prod | yes | 3 | 2 | answer | The `pat` auth method succeeded. Here are the results: |
| B | 1 | S1 | staging | yes | 3 | 1 | answer | The second invoice in the billing-api staging environment is: |
| B | 2 | P1 | prod | yes | 2 | 0 | answer | The first invoice in the billing-api production environment: |
| B | 2 | S1 | staging | yes | 2 | 0 | answer | Second invoice: **id INV-5002, amount 4100**. |
| D | 1 | P1 | prod | yes | 3 | 0 | answer | The first invoice in the billing-api production environment is: |
| D | 1 | S1 | staging | yes | 4 | 2 | answer | The second invoice in the billing-api staging environment is: |
| D | 2 | P1 | prod | yes | 3 | 2 | answer | The first invoice in the billing-api production environment: |
| D | 2 | S1 | staging | yes | 2 | 0 | answer | Second invoice: **id INV-5002, amount 4100**. |

_Analysis to be filled after inspecting the run._
