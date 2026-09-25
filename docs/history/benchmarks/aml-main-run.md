# AML main experiment — 2026-09-19

Dump: `benchmarks/aml-main-run.json`

## Configuration

- model: `deepseek/deepseek-v4.1-flash` (temperature 0, max_output_tokens 16384, reasoning "low")
- fixture: main; arms: A, D; sessions: 10; tasks per session: 5; flip at session: 6
- arm mapping: A = no memory; B = semantic ranking only; C = semantic+temporal ranking; D = C + pre-action activation + guardrail + adaptive feedback with cause-level attribution

## Per-arm totals

| arm | tasks | success | steps | calls | failed | repeated | tokens | hints | reused | validated | contradicted | unresolved |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| A | 50 | 50 | 188 | 176 | 56 | 0 | 181482 | 0 | 0 | 0 | 0 | 0 |
| D | 50 | 50 | 152 | 111 | 16 | 0 | 146484 | 53 | 38 | 34 | 3 | 1 |

## Per-session detail

| arm | session | epoch | success | failed | repeated | steps | tokens | hints | reused | validated | contradicted |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| A | 1 | 1 | 5/5 | 7 | 0 | 20 | 19646 | 0 | 0 | 0 | 0 |
| A | 2 | 1 | 5/5 | 6 | 0 | 20 | 19364 | 0 | 0 | 0 | 0 |
| A | 3 | 1 | 5/5 | 6 | 0 | 19 | 18515 | 0 | 0 | 0 | 0 |
| A | 4 | 1 | 5/5 | 7 | 0 | 20 | 19788 | 0 | 0 | 0 | 0 |
| A | 5 | 1 | 5/5 | 5 | 0 | 19 | 18100 | 0 | 0 | 0 | 0 |
| A | 6 | 2 | 5/5 | 5 | 0 | 18 | 17249 | 0 | 0 | 0 | 0 |
| A | 7 | 2 | 5/5 | 5 | 0 | 18 | 17171 | 0 | 0 | 0 | 0 |
| A | 8 | 2 | 5/5 | 5 | 0 | 18 | 17141 | 0 | 0 | 0 | 0 |
| A | 9 | 2 | 5/5 | 5 | 0 | 18 | 17309 | 0 | 0 | 0 | 0 |
| A | 10 | 2 | 5/5 | 5 | 0 | 18 | 17199 | 0 | 0 | 0 | 0 |
| D | 1 | 1 | 5/5 | 5 | 0 | 19 | 18294 | 1 | 0 | 0 | 0 |
| D | 2 | 1 | 5/5 | 2 | 0 | 16 | 16003 | 6 | 4 | 3 | 1 |
| D | 3 | 1 | 5/5 | 1 | 0 | 14 | 13243 | 5 | 4 | 4 | 0 |
| D | 4 | 1 | 5/5 | 1 | 0 | 14 | 13384 | 5 | 4 | 4 | 0 |
| D | 5 | 1 | 5/5 | 1 | 0 | 14 | 13356 | 5 | 4 | 4 | 0 |
| D | 6 | 2 | 5/5 | 2 | 0 | 15 | 14616 | 5 | 4 | 3 | 1 |
| D | 7 | 2 | 5/5 | 1 | 0 | 15 | 14362 | 7 | 5 | 4 | 0 |
| D | 8 | 2 | 5/5 | 1 | 0 | 15 | 14516 | 7 | 5 | 4 | 1 |
| D | 9 | 2 | 5/5 | 1 | 0 | 15 | 14229 | 5 | 4 | 4 | 0 |
| D | 10 | 2 | 5/5 | 1 | 0 | 15 | 14481 | 7 | 4 | 4 | 0 |

## Post-flip window (from session 6)

| arm | tasks | success | failed | repeated | contradicted | unresolved | tokens |
|---|---:|---:|---:|---:|---:|---:|---:|
| A | 25 | 25 | 25 | 0 | 0 | 0 | 86069 |
| D | 25 | 25 | 6 | 0 | 2 | 1 | 72204 |

## Per-task answers

| arm | session | task | env | ok | steps | failed | stopped | answer |
|---|---:|---|---|---|---:|---:|---|---|
| A | 1 | T1 | prod | yes | 4 | 2 | answer | The second invoice in the billing-api service is: |
| A | 1 | T2 | prod | yes | 4 | 3 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| A | 1 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 1 | T4 | prod | yes | 5 | 0 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of search results for search-index `documents.search`). |
| A | 1 | T5 | prod | yes | 4 | 2 | answer | Invoice **INV-2007** details: |
| A | 2 | T1 | prod | yes | 4 | 2 | answer | The second invoice in the billing-api service is: |
| A | 2 | T2 | prod | yes | 4 | 2 | answer | The refund was retrieved successfully using `service_token` auth (pat and oauth both returned 403). |
| A | 2 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 2 | T4 | prod | yes | 5 | 0 | answer | Found it. The document titled **"quarterly report"** in the search-index service has document id **DOC-3319**. |
| A | 2 | T5 | prod | yes | 4 | 2 | answer | Invoice **INV-2007** details: |
| A | 3 | T1 | prod | yes | 4 | 2 | answer | The second invoice in the billing-api service is: |
| A | 3 | T2 | prod | yes | 4 | 3 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| A | 3 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 3 | T4 | prod | yes | 5 | 0 | answer | The document titled **"quarterly report"** has document id **DOC-3319**. |
| A | 3 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| A | 4 | T1 | prod | yes | 4 | 2 | answer | The second invoice in the billing-api service is: |
| A | 4 | T2 | prod | yes | 4 | 3 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| A | 4 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 4 | T4 | prod | yes | 5 | 0 | answer | The document titled **"quarterly report"** has document id **DOC-3319**. |
| A | 4 | T5 | prod | yes | 4 | 2 | answer | Invoice **INV-2007** details: |
| A | 5 | T1 | prod | yes | 4 | 2 | answer | The second invoice in the billing-api service is: |
| A | 5 | T2 | prod | yes | 4 | 2 | answer | The refund was fetched successfully using `service_token` auth (pat and oauth both returned 403). |
| A | 5 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 5 | T4 | prod | yes | 5 | 0 | answer | The document titled "quarterly report" has **document id `DOC-3319`**. |
| A | 5 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| A | 6 | T1 | prod | yes | 3 | 1 | answer | The invoices in billing-api are: |
| A | 6 | T2 | prod | yes | 4 | 3 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| A | 6 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 6 | T4 | prod | yes | 5 | 0 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319**. |
| A | 6 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| A | 7 | T1 | prod | yes | 3 | 1 | answer | The second invoice in the billing-api service is: |
| A | 7 | T2 | prod | yes | 4 | 3 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| A | 7 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 7 | T4 | prod | yes | 5 | 0 | answer | The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of search results). |
| A | 7 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| A | 8 | T1 | prod | yes | 3 | 1 | answer | The second invoice in the billing-api service is: |
| A | 8 | T2 | prod | yes | 4 | 3 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| A | 8 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 8 | T4 | prod | yes | 5 | 0 | answer | The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of search results). |
| A | 8 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| A | 9 | T1 | prod | yes | 3 | 1 | answer | The second invoice in the billing-api service is: |
| A | 9 | T2 | prod | yes | 4 | 3 | answer | The refund was retrieved successfully using `service_token` auth. |
| A | 9 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 9 | T4 | prod | yes | 5 | 0 | answer | The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of search results). |
| A | 9 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| A | 10 | T1 | prod | yes | 3 | 1 | answer | The second invoice in the billing-api service is: |
| A | 10 | T2 | prod | yes | 4 | 3 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| A | 10 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| A | 10 | T4 | prod | yes | 5 | 0 | answer | The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of search results). |
| A | 10 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| D | 1 | T1 | prod | yes | 4 | 2 | answer | The second invoice in the billing-api service is: |
| D | 1 | T2 | prod | yes | 4 | 2 | answer | The refund status was retrieved successfully using the `service_token` auth method (pat and oauth were both rejected with 403). |
| D | 1 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 1 | T4 | prod | yes | 5 | 0 | answer | Found it. The document titled **"quarterly report"** in the search-index service has document id **DOC-3319** (returned on the third page of the paginated search results, after following the cursors `... |
| D | 1 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| D | 2 | T1 | prod | yes | 2 | 0 | answer | The second invoice in the billing-api service is: |
| D | 2 | T2 | prod | yes | 2 | 0 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| D | 2 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 2 | T4 | prod | yes | 5 | 1 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319**. |
| D | 2 | T5 | prod | yes | 4 | 1 | answer | Invoice **INV-2007** details: |
| D | 3 | T1 | prod | yes | 2 | 0 | answer | The second invoice in the billing-api service is: |
| D | 3 | T2 | prod | yes | 2 | 0 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| D | 3 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 3 | T4 | prod | yes | 4 | 0 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of results, after iterating cursors c2 and c3). |
| D | 3 | T5 | prod | yes | 3 | 1 | answer | Invoice INV-2007 details: |
| D | 4 | T1 | prod | yes | 2 | 0 | answer | The second invoice in the billing-api service is: |
| D | 4 | T2 | prod | yes | 2 | 0 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| D | 4 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 4 | T4 | prod | yes | 4 | 0 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of results). |
| D | 4 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| D | 5 | T1 | prod | yes | 2 | 0 | answer | The second invoice in the billing-api service is: |
| D | 5 | T2 | prod | yes | 2 | 0 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| D | 5 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 5 | T4 | prod | yes | 4 | 0 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of results). |
| D | 5 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| D | 6 | T1 | prod | yes | 3 | 1 | answer | The second invoice in the billing-api service is: |
| D | 6 | T2 | prod | yes | 2 | 0 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| D | 6 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 6 | T4 | prod | yes | 4 | 0 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of results). |
| D | 6 | T5 | prod | yes | 3 | 1 | answer | Invoice **INV-2007** details: |
| D | 7 | T1 | prod | yes | 2 | 0 | answer | The second invoice in the billing-api service is: |
| D | 7 | T2 | prod | yes | 2 | 0 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| D | 7 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 7 | T4 | prod | yes | 4 | 0 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of results, after iterating cursors c2 and c3). |
| D | 7 | T5 | prod | yes | 4 | 1 | answer | Invoice **INV-2007** details: |
| D | 8 | T1 | prod | yes | 2 | 0 | answer | The second invoice in the billing-api service is: |
| D | 8 | T2 | prod | yes | 2 | 0 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| D | 8 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 8 | T4 | prod | yes | 4 | 0 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of results). |
| D | 8 | T5 | prod | yes | 4 | 1 | answer | Invoice **INV-2007** details: |
| D | 9 | T1 | prod | yes | 2 | 0 | answer | The second invoice in the billing-api service is: |
| D | 9 | T2 | prod | yes | 2 | 0 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| D | 9 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 9 | T4 | prod | yes | 4 | 0 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of results). |
| D | 9 | T5 | prod | yes | 4 | 1 | answer | Invoice **INV-2007** details: |
| D | 10 | T1 | prod | yes | 2 | 0 | answer | The second invoice in the billing-api service is: |
| D | 10 | T2 | prod | yes | 2 | 0 | answer | Refund **RFD-9002** — status: **settled** (amount: 150). |
| D | 10 | T3 | prod | yes | 3 | 0 | answer | The email template named **"welcome"** has id **TMPL-77**. |
| D | 10 | T4 | prod | yes | 4 | 0 | answer | Found it. The document titled **"quarterly report"** has document id **DOC-3319** (found on the third page of search results). |
| D | 10 | T5 | prod | yes | 4 | 1 | answer | Invoice **INV-2007** details: |

_Analysis to be filled after inspecting the run._
