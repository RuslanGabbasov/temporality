# AML main experiment — 2026-09-19

Dump: `benchmarks/aml-smoke.json`

## Configuration

- model: `deepseek/deepseek-v4.1-flash` (temperature 0, max_output_tokens 16384, reasoning "low")
- fixture: main; arms: A, D; sessions: 2; tasks per session: 2; flip at session: 0
- arm mapping: A = no memory; B = semantic ranking only; C = semantic+temporal ranking; D = C + pre-action activation + guardrail + adaptive feedback with cause-level attribution

## Per-arm totals

| arm | tasks | success | steps | calls | failed | repeated | tokens | hints | reused | validated | contradicted | unresolved |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| A | 4 | 4 | 14 | 12 | 4 | 0 | 12820 | 0 | 0 | 0 | 0 | 0 |
| D | 4 | 4 | 12 | 9 | 2 | 0 | 10589 | 1 | 1 | 1 | 0 | 0 |

## Per-session detail

| arm | session | epoch | success | failed | repeated | steps | tokens | hints | reused | validated | contradicted |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| A | 1 | 1 | 2/2 | 2 | 0 | 7 | 6408 | 0 | 0 | 0 | 0 |
| A | 2 | 1 | 2/2 | 2 | 0 | 7 | 6412 | 0 | 0 | 0 | 0 |
| D | 1 | 1 | 2/2 | 2 | 0 | 7 | 6450 | 0 | 0 | 0 | 0 |
| D | 2 | 1 | 2/2 | 0 | 0 | 5 | 4139 | 1 | 1 | 1 | 0 |

## Per-task answers

| arm | session | task | env | ok | steps | failed | stopped | answer |
|---|---:|---|---|---|---:|---:|---|---|
| A | 1 | T1 | prod | yes | 4 | 2 | answer | The second invoice in the billing-api service is: |
| A | 1 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 2 | T1 | prod | yes | 4 | 2 | answer | The second invoice in the billing-api service is: |
| A | 2 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 1 | T1 | prod | yes | 4 | 2 | answer | The second invoice in the billing-api service is: |
| D | 1 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 2 | T1 | prod | yes | 2 | 0 | answer | The second invoice in the billing-api service is: |
| D | 2 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |

_Analysis to be filled after inspecting the run._
