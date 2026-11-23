# Usage aggregates and monthly reports

Self-monitoring data for humans and agents: what consumption looks like and
who (or what) it belongs to. One fold attributes journal events to six
dimensions and prices every model call with the kernel's own price config —
the same numbers the Sagas tab and the org usage stats show.

## Dimensions

`org_unit` (the boards' attribution snapshot), `employee` (the run starter —
an approver answering inside the run never re-attributes it), `project`,
`saga` (task→saga bindings; unbound runs land in an explicit `unbound`
bucket), `model`, `provider` (call-level cuts: tokens and cost only — a
model call has no approval of its own).

Every row carries: runs, model calls, prompt/cached/completion/total tokens,
cost USD, and the human-attention rollup for run-level cuts — decisions,
answers, HAB seconds (attention), HWT seconds (waiting), flow efficiency %.
Rows sort by cost descending. The `total` row is the window's grand total.

The fold reuses the budget fold's machinery (`sagaBindings`,
`approvalAccumulator`, `humanStats` in kernel/cost), so every cut of the
same window agrees with the others by construction. Usage events are fetched
with the window's `since`; saga lifecycle events are fetched unbounded — a
binding registered long before the window still attributes the window's
runs.

## Agent tool: `usage_report`

```
{ "group_by": "employee", "months": 3, "project": "p-1", "limit": 50 }
```

`group_by` (required) is one of the six dimensions; `months` (1–12, default
1 = month to date) walks back month-aligned; `project` defaults to the
run's own project. Read-only — no approval round. The answer is compact JSON
(`UsageReport`), capped at 50 rows.

## HTTP API

- `GET /v1/agent/usage?group_by=…&project=…&months=…&since=…&until=…&limit=…` —
  the same aggregate over HTTP (reader role). Explicit `since`/`until`
  (RFC3339) override the month derivation.
- `GET /v1/agent/usage/monthly` — the published monthly reports, newest
  first.

## Automatic monthly report

`usage.monthly_report` journal events, one per closed calendar month,
written by an hourly kernel loop (`StartUsageReportLoop`): it reads the
newest report event as the watermark and folds every still-uncovered closed
month through all six dimensions (top 50 rows each). Deterministic event id
(`usage-report/YYYY-MM`), so a retried pass cannot fork a month into two
reports. Backfill caps at 6 months — older gaps are still reachable through
the live tool with an explicit window. Before this loop the monthly report
was a manual copy out of the UI.

Payload: `month`, `generated_at`, `total_tokens`, `total_cost_usd`,
`hab_seconds`, `hwt_seconds`, `rows` — per-dimension `UsageRow` arrays.
