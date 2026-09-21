# AML conflict experiment — 2026-09-19

Dump: `benchmarks/aml-conflict-run.json`

## Configuration

- model: `deepseek/deepseek-v4.1-flash` (temperature 0, max_output_tokens 16384, reasoning "low")
- fixture: conflict; arms: B, D; sessions: 4; tasks per session: 2; flip at session: 1
- arm mapping: A = no memory; B = semantic ranking only; C = semantic+temporal ranking; D = C + pre-action activation + guardrail + adaptive feedback with cause-level attribution

## Per-arm totals

| arm | tasks | success | steps | calls | failed | repeated | tokens | hints | reused | validated | contradicted | unresolved |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| B | 8 | 8 | 20 | 14 | 3 | 0 | 19028 | 11 | 6 | 5 | 1 | 0 |
| D | 8 | 8 | 21 | 17 | 4 | 0 | 19385 | 3 | 3 | 3 | 0 | 0 |

## Per-session detail

| arm | session | epoch | success | failed | repeated | steps | tokens | hints | reused | validated | contradicted |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| B | 1 | 2 | 2/2 | 2 | 0 | 7 | 6824 | 0 | 0 | 0 | 0 |
| B | 2 | 2 | 2/2 | 1 | 0 | 5 | 4879 | 3 | 2 | 1 | 1 |
| B | 3 | 2 | 2/2 | 0 | 0 | 4 | 3673 | 4 | 2 | 2 | 0 |
| B | 4 | 2 | 2/2 | 0 | 0 | 4 | 3652 | 4 | 2 | 2 | 0 |
| D | 1 | 2 | 2/2 | 2 | 0 | 7 | 6803 | 0 | 0 | 0 | 0 |
| D | 2 | 2 | 2/2 | 0 | 0 | 5 | 4271 | 1 | 1 | 1 | 0 |
| D | 3 | 2 | 2/2 | 0 | 0 | 4 | 3485 | 1 | 1 | 1 | 0 |
| D | 4 | 2 | 2/2 | 2 | 0 | 5 | 4826 | 1 | 1 | 1 | 0 |

## Post-flip window (from session 1)

| arm | tasks | success | failed | repeated | contradicted | unresolved | tokens |
|---|---:|---:|---:|---:|---:|---:|---:|
| B | 8 | 8 | 3 | 0 | 1 | 0 | 19028 |
| D | 8 | 8 | 4 | 0 | 0 | 0 | 19385 |

## Per-task answers

| arm | session | task | env | ok | steps | failed | stopped | answer |
|---|---:|---|---|---|---:|---:|---|---|
| B | 1 | P1 | prod | yes | 3 | 0 | answer | The invoices in billing-api production: |
| B | 1 | S1 | staging | yes | 4 | 2 | answer | The second invoice in the billing-api staging environment: |
| B | 2 | P1 | prod | yes | 3 | 1 | answer | First invoice in billing-api (prod): **id INV-1001, amount 1200**. |
| B | 2 | S1 | staging | yes | 2 | 0 | answer | Second invoice: **id INV-5002, amount 4100**. |
| B | 3 | P1 | prod | yes | 2 | 0 | answer | The first invoice in the billing-api production environment is: |
| B | 3 | S1 | staging | yes | 2 | 0 | answer | Second invoice: **id INV-5002, amount 4100**. |
| B | 4 | P1 | prod | yes | 2 | 0 | answer | First invoice in billing-api (prod): **id: INV-1001, amount: 1200**. |
| B | 4 | S1 | staging | yes | 2 | 0 | answer | Second invoice: **id INV-5002, amount 4100**. |
| D | 1 | P1 | prod | yes | 3 | 0 | answer | I listed the invoices from the billing-api production environment. |
| D | 1 | S1 | staging | yes | 4 | 2 | answer | The second invoice in the billing-api staging environment is: |
| D | 2 | P1 | prod | yes | 3 | 0 | answer | The first invoice in the billing-api production environment: |
| D | 2 | S1 | staging | yes | 2 | 0 | answer | Second invoice: **id INV-5002, amount 4100**. |
| D | 3 | P1 | prod | yes | 2 | 0 | answer | The call succeeded. |
| D | 3 | S1 | staging | yes | 2 | 0 | answer | Second invoice: **id INV-5002, amount 4100**. |
| D | 4 | P1 | prod | yes | 3 | 2 | answer | Successfully retrieved the invoices using PAT auth in the production environment. |
| D | 4 | S1 | staging | yes | 2 | 0 | answer | Second invoice: **id INV-5002, amount 4100**. |

_Analysis to be filled after inspecting the run._
