# AML main experiment — 2026-09-19

Dump: `benchmarks/aml-secondmodel-run.json`

## Configuration

- model: `qwen/qwen3-235b-a22b-2507` (temperature 0, max_output_tokens 16384, reasoning "low")
- fixture: main; arms: D; sessions: 4; tasks per session: 5; flip at session: 0
- arm mapping: A = no memory; B = semantic ranking only; C = semantic+temporal ranking; D = C + pre-action activation + guardrail + adaptive feedback with cause-level attribution

## Per-arm totals

| arm | tasks | success | steps | calls | failed | repeated | tokens | hints | reused | validated | contradicted | unresolved |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| D | 20 | 13 | 61 | 41 | 10 | 0 | 41828 | 13 | 9 | 6 | 0 | 3 |

## Per-session detail

| arm | session | epoch | success | failed | repeated | steps | tokens | hints | reused | validated | contradicted |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| D | 1 | 1 | 4/5 | 4 | 0 | 19 | 12827 | 1 | 0 | 0 | 0 |
| D | 2 | 1 | 3/5 | 2 | 0 | 14 | 9685 | 4 | 3 | 2 | 0 |
| D | 3 | 1 | 3/5 | 2 | 0 | 14 | 9631 | 4 | 3 | 2 | 0 |
| D | 4 | 1 | 3/5 | 2 | 0 | 14 | 9685 | 4 | 3 | 2 | 0 |

## Per-task answers

| arm | session | task | env | ok | steps | failed | stopped | answer |
|---|---:|---|---|---|---:|---:|---|---|
| D | 1 | T1 | prod | yes | 4 | 1 | answer | The second invoice has id "INV-1002" and amount 3400. |
| D | 1 | T2 | prod | yes | 5 | 2 | answer | The refund with ID RFD-9002 has a status of "settled". |
| D | 1 | T3 | prod | yes | 3 | 0 | answer | The id of the email template named "welcome" is TMPL-77. |
| D | 1 | T4 | prod | yes | 5 | 0 | answer | The document titled "quarterly report" has the document ID DOC-3319. |
| D | 1 | T5 | prod | no | 2 | 1 | answer | The invoice with ID INV-2007 was not found in the system. Please verify the invoice ID and try again. |
| D | 2 | T1 | prod | yes | 2 | 0 | answer | The second invoice has the ID `INV-1002` and an amount of `3400`. |
| D | 2 | T2 | prod | no | 2 | 1 | answer | The refund with ID RFD-9002 was not found in the system. Please verify the refund ID and try again. |
| D | 2 | T3 | prod | yes | 3 | 0 | answer | The id of the email template named "welcome" is TMPL-77. |
| D | 2 | T4 | prod | yes | 5 | 0 | answer | The document titled "quarterly report" has the document ID DOC-3319. |
| D | 2 | T5 | prod | no | 2 | 1 | answer | The invoice with ID INV-2007 was not found in the system. Please verify the invoice ID and try again. |
| D | 3 | T1 | prod | yes | 2 | 0 | answer | The second invoice has the id "INV-1002" and an amount of 3400. |
| D | 3 | T2 | prod | no | 2 | 1 | answer | The refund with ID RFD-9002 was not found in the system. Please verify the refund ID and try again. |
| D | 3 | T3 | prod | yes | 3 | 0 | answer | The id of the email template named "welcome" is TMPL-77. |
| D | 3 | T4 | prod | yes | 5 | 0 | answer | The document titled "quarterly report" has the document ID DOC-3319. |
| D | 3 | T5 | prod | no | 2 | 1 | answer | The invoice with ID INV-2007 was not found in the system. Please verify the invoice ID and try again. |
| D | 4 | T1 | prod | yes | 2 | 0 | answer | The second invoice has the ID "INV-1002" and an amount of 3400. |
| D | 4 | T2 | prod | no | 2 | 1 | answer | The refund with ID RFD-9002 was not found in the system. Please verify the refund ID and try again. |
| D | 4 | T3 | prod | yes | 3 | 0 | answer | The id of the email template named "welcome" is TMPL-77. |
| D | 4 | T4 | prod | yes | 5 | 0 | answer | The document titled "quarterly report" has the document ID DOC-3319. |
| D | 4 | T5 | prod | no | 2 | 1 | answer | The invoice with ID INV-2007 was not found in the system. Please verify the invoice ID and try again. |

_Analysis to be filled after inspecting the run._
