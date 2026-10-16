package cost

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// Projects overview (docs/system-dashboard.md §3.2): per-project window
// aggregation served by the kernel so the dashboard renders without paging
// the journal client-side.

// overviewMaxWindow caps the requested window: longer scans would page the
// journal beyond any dashboard need (the journal itself caps stats at 31d).
const overviewMaxWindow = 7 * 24 * time.Hour

// overviewApprovalLookback is the separate scan bound for approval events:
// a pending approval may be older than the window (the human never answered),
// so requests are looked up over a fixed 7-day horizon like the doc's gate
// window instead of the dashboard window.
const overviewApprovalLookback = 7 * 24 * time.Hour

// ProjectOverviewRow is one project's activity over the requested window.
type ProjectOverviewRow struct {
	Project          string     `json:"project"`
	OrgUnits         []string   `json:"org_units"`
	RunsStarted      int        `json:"runs_started"`
	RunsRunning      int        `json:"runs_running"`
	RunsFailed       int        `json:"runs_failed"`
	ModelCalls       int        `json:"model_calls"`
	TotalTokens      int64      `json:"total_tokens"`
	TotalCostUSD     float64    `json:"total_cost_usd"`
	PendingApprovals int        `json:"pending_approvals"`
	LastActivityAt   *time.Time `json:"last_activity_at,omitempty"`
}

// ProjectsOverview is the whole dashboard answer: the window it covers and a
// row per (visible) project, most recently active first.
type ProjectsOverview struct {
	Window      string               `json:"window"`
	GeneratedAt time.Time            `json:"generated_at"`
	Projects    []ProjectOverviewRow `json:"projects"`
}

// overviewRun tracks one run of one project while folding events: whether a
// start was seen and the latest terminal event (retry/cancel races may emit
// several — the latest one wins, like the saga accumulator).
type overviewRun struct {
	started    bool
	terminal   string
	terminalAt time.Time
}

// overviewProject accumulates one project's rows and run/approval state.
type overviewProject struct {
	row       ProjectOverviewRow
	runs      map[string]*overviewRun
	approvals approvalAccumulator
}

// aggregateProjectsOverview folds journal events into per-project overview
// rows. Lifecycle and model events older than now-window are ignored (defense
// in depth — the fetch already bounds them); approval events are consumed
// regardless of age because they arrive from the longer approval lookback and
// a pending request older than the window must still be counted. Cost math is
// the same as ingestSagaEvent/aggregateRun: model (provider fallback) priced
// through cfg with prompt/cached/completion tokens.
func aggregateProjectsOverview(events []observation.Event, window time.Duration, now time.Time, cfg Config) map[string]*ProjectOverviewRow {
	cutoff := now.Add(-window)
	projects := map[string]*overviewProject{}
	project := func(id string) *overviewProject {
		p := projects[id]
		if p == nil {
			p = &overviewProject{runs: map[string]*overviewRun{}}
			p.row.Project = id
			projects[id] = p
		}
		return p
	}
	run := func(p *overviewProject, runID string) *overviewRun {
		r := p.runs[runID]
		if r == nil {
			r = &overviewRun{}
			p.runs[runID] = r
		}
		return r
	}
	for _, ev := range events {
		if ev.Context.Project == "" {
			continue
		}
		switch ev.Type {
		case "run.started", "run.completed", "run.failed", "run.cancelled", "model.completed":
			if ev.OccurredAt.Before(cutoff) {
				continue
			}
		}
		p := project(ev.Context.Project)
		switch ev.Type {
		case "run.started":
			if ev.Context.Run == "" {
				continue
			}
			run(p, ev.Context.Run).started = true
			p.row.RunsStarted++
		case "run.completed", "run.failed", "run.cancelled":
			if ev.Context.Run == "" {
				continue
			}
			r := run(p, ev.Context.Run)
			// Retries or a cancelled-then-completed race may emit several
			// terminal events; the latest one wins.
			if !ev.OccurredAt.After(r.terminalAt) {
				continue
			}
			r.terminalAt = ev.OccurredAt
			r.terminal = map[string]string{
				"run.completed": "completed", "run.failed": "failed", "run.cancelled": "cancelled",
			}[ev.Type]
			if r.terminal == "failed" {
				p.row.RunsFailed++
			}
		case "model.completed":
			model, _ := ev.Data["model"].(string)
			if model == "" {
				model, _ = ev.Data["provider"].(string)
			}
			prompt := toInt(ev.Data["prompt_tokens"])
			cached := toInt(ev.Data["cached_tokens"])
			comp := toInt(ev.Data["completion_tokens"])
			p.row.ModelCalls++
			p.row.TotalTokens += int64(toInt(ev.Data["total_tokens"]))
			p.row.TotalCostUSD += cfg.ModelCost(model, prompt, cached, comp)
		case "approval.requested", "approval.granted", "approval.rejected", "approval.timed_out":
			p.approvals.ingest(ev.Context.Run, ev)
		default:
			continue
		}
		if p.row.LastActivityAt == nil || ev.OccurredAt.After(*p.row.LastActivityAt) {
			at := ev.OccurredAt
			p.row.LastActivityAt = &at
		}
	}
	rows := map[string]*ProjectOverviewRow{}
	for id, p := range projects {
		for _, r := range p.runs {
			if r.started && r.terminal == "" {
				p.row.RunsRunning++
			}
		}
		p.row.PendingApprovals = len(p.approvals.pending)
		rows[id] = &p.row
	}
	return rows
}

// filterOverviewRows applies the visibility map to aggregated rows and sorts
// them for the answer: most recently active first, projects without activity
// last, ties broken by project id. A nil map means no restriction (every row
// passes, org units stay unset); a non-nil map — including an empty one —
// restricts the answer to its keys, so a principal with no visible projects
// gets an empty overview, never everything.
func filterOverviewRows(rows map[string]*ProjectOverviewRow, visible map[string][]string) []ProjectOverviewRow {
	out := make([]ProjectOverviewRow, 0, len(rows))
	for id, row := range rows {
		if visible != nil {
			units, ok := visible[id]
			if !ok {
				continue
			}
			row.OrgUnits = units
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].LastActivityAt, out[j].LastActivityAt
		switch {
		case left == nil && right == nil:
		case left == nil:
			return false
		case right == nil:
			return true
		case !left.Equal(*right):
			return left.After(*right)
		}
		return out[i].Project < out[j].Project
	})
	return out
}

// ProjectsOverview aggregates the journal over the window (docs/
// system-dashboard.md §3.2). Lifecycle and model events are paged with
// since=now-window; approval events get their own paged fetch with the fixed
// 7-day lookback because pending approvals may predate the window. visible
// constrains the answer to the caller's projects (nil = no restriction).
func (a *CostAPI) ProjectsOverview(ctx context.Context, window time.Duration, visible map[string][]string) (ProjectsOverview, error) {
	now := a.now()
	windowSince := now.Add(-window)
	approvalSince := now.Add(-overviewApprovalLookback)
	lifecycle, err := a.queryEventsFilteredSince(ctx, "", "", "", &windowSince,
		"run.started", "run.completed", "run.failed", "run.cancelled", "model.completed")
	if err != nil {
		return ProjectsOverview{}, err
	}
	approvals, err := a.queryEventsFilteredSince(ctx, "", "", "", &approvalSince,
		"approval.requested", "approval.granted", "approval.rejected", "approval.timed_out")
	if err != nil {
		return ProjectsOverview{}, err
	}
	events := append(lifecycle, approvals...)
	rows := aggregateProjectsOverview(events, window, now, a.config)
	return ProjectsOverview{
		Window:      window.String(),
		GeneratedAt: now,
		Projects:    filterOverviewRows(rows, visible),
	}, nil
}

// ProjectsOverviewHandler serves the overview without a visibility filter;
// callers that resolve principal visibility use
// ProjectsOverviewHandlerWithVisible.
func (a *CostAPI) ProjectsOverviewHandler(w http.ResponseWriter, r *http.Request) {
	a.ProjectsOverviewHandlerWithVisible(w, r, nil)
}

// ProjectsOverviewHandlerWithVisible serves the overview constrained to the
// visible map (project id → org units); nil means no restriction.
func (a *CostAPI) ProjectsOverviewHandlerWithVisible(w http.ResponseWriter, r *http.Request, visible map[string][]string) {
	window := 24 * time.Hour
	if raw := r.URL.Query().Get("window"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 || parsed > overviewMaxWindow {
			writeError(w, 400, "window must be a positive duration of at most 168h, e.g. 24h")
			return
		}
		window = parsed
	}
	overview, err := a.ProjectsOverview(r.Context(), window, visible)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, overview)
}
