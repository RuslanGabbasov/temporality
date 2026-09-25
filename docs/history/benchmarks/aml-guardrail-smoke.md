# AML guardrail experiment — 2026-09-19

Dump: `benchmarks/aml-guardrail-smoke.json`

## Configuration

- model: `deepseek/deepseek-v4.1-flash` (temperature 0, max_output_tokens 16384, reasoning "low")
- fixture: guardrail; arms: A, D; sessions: 2; tasks per session: 1; flip at session: 0
- arm mapping: A = no memory; B = semantic ranking only; C = semantic+temporal ranking; D = C + pre-action activation + guardrail + adaptive feedback with cause-level attribution

## Per-arm totals

| arm | tasks | success | steps | calls | failed | repeated | tokens | hints | reused | validated | contradicted | unresolved |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| A | 2 | 2 | 10 | 8 | 2 | 0 | 10095 | 0 | 0 | 0 | 0 | 0 |
| D | 2 | 2 | 9 | 7 | 2 | 0 | 8775 | 1 | 1 | 0 | 0 | 1 |

## Per-session detail

| arm | session | epoch | success | failed | repeated | steps | tokens | hints | reused | validated | contradicted |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| A | 1 | 1 | 1/1 | 1 | 0 | 5 | 5047 | 0 | 0 | 0 | 0 |
| A | 2 | 1 | 1/1 | 1 | 0 | 5 | 5048 | 0 | 0 | 0 | 0 |
| D | 1 | 1 | 1/1 | 1 | 0 | 5 | 4879 | 0 | 0 | 0 | 0 |
| D | 2 | 1 | 1/1 | 1 | 0 | 4 | 3896 | 1 | 1 | 0 | 0 |

## Per-task answers

| arm | session | task | env | ok | steps | failed | stopped | answer |
|---|---:|---|---|---|---:|---:|---|---|
| A | 1 | G1 | prod | yes | 5 | 1 | answer | The monthly operations report was fetched successfully. |
| A | 2 | G1 | prod | yes | 5 | 1 | answer | The monthly operations report was fetched successfully. |
| D | 1 | G1 | prod | yes | 5 | 1 | answer | The monthly operations report was served successfully via the documented mitigation. |
| D | 2 | G1 | prod | yes | 4 | 1 | answer | The monthly operations report was fetched successfully. |

_Analysis to be filled after inspecting the run._
