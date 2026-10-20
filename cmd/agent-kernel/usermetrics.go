package main

import (
	"net/http"
	"sort"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/workspace"
)

// UserMetricsRow is one user's activity rollup for the dashboard's Users tab:
// what they run (runs, recency, problems), what waits on them (open asks),
// what they already decided, and the human-time both sides took — HAB spent
// on decisions and reading, HWT waiting (docs/supervisor-model.md §5).
type UserMetricsRow struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Role      string     `json:"role,omitempty"`
	Active    bool       `json:"active"`
	OrgUnitID string     `json:"org_unit_id,omitempty"`
	RunsTotal int        `json:"runs_total"`
	RunsWeek  int        `json:"runs_week"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	// LastRun: what the user was last doing (project + status + task title).
	LastRun *UserLastRun `json:"last_run,omitempty"`
	// Problems: failed runs and asks that expired without an answer.
	FailedRuns int `json:"failed_runs"`
	TimedOut   int `json:"timed_out_asks"`
	// Asks addressed to this user: open right now and answered overall.
	AsksOpen        int        `json:"asks_open"`
	AsksTotal       int        `json:"asks_total"`
	WaitSeconds     int64      `json:"wait_seconds"` // human wait on ANSWERED asks (HWT slice)
	LastAskAt       *time.Time `json:"last_ask_at,omitempty"`
	LastAskQuestion string     `json:"last_ask_question,omitempty"`
	// HABSeconds: the attention the user spent — decisions (approval-weighted)
	// plus reading their answers' responses. An approximation of the §5 HAB,
	// sliced per user from the ask rows.
	HABSeconds int64 `json:"hab_seconds"`
}

// UserLastRun is the human-readable tail of the user's activity.
type UserLastRun struct {
	Project string `json:"project"`
	Status  string `json:"status"`
	Title   string `json:"title,omitempty"`
	RunID   string `json:"run_id"`
}

// registerUserMetricsRoutes serves the dashboard Users tab: one row per
// workspace user with runs/asks/latency metrics. Runs are attributed through
// exec_context actor (the same match ListRunsByActor uses — API, trigger and
// intake entrypoints all stamp it); asks through resolved_user.
func registerUserMetricsRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store, factors cost.HumanFactors) {
	mux.HandleFunc("GET /v1/workspace/users/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		users, err := ws.ListUsers(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		asks, err := ws.ListHumanRequests(r.Context(), "", "", "", false)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		askStats := map[string]*userAskStats{}
		for i := range asks {
			ask := &asks[i]
			if ask.ResolvedUser == "" {
				continue // team fan-outs and unresolved recipients stay aggregate
			}
			stats, ok := askStats[ask.ResolvedUser]
			if !ok {
				stats = &userAskStats{}
				askStats[ask.ResolvedUser] = stats
			}
			stats.total++
			switch ask.Status {
			case workspace.HumanStatusPending, workspace.HumanStatusDelivered:
				stats.open++
			case workspace.HumanStatusExpired:
				stats.timedOut++
			case workspace.HumanStatusAnswered:
				stats.decisions++
				if ask.AnsweredAt != nil {
					stats.waitSeconds += int64(ask.AnsweredAt.Sub(ask.CreatedAt).Seconds())
				}
				// HAB slice: the decision (approval-weighted) plus reading the
				// response they typed — same coefficients the §5 rollup uses.
				stats.habSeconds += int64(factors.ApprovalSeconds)
				if runes := len([]rune(ask.Response)); runes > 0 {
					stats.habSeconds += int64(runes/5) * 60 / int64(factors.ReadingWPM)
				}
			}
			if stats.lastAsk == nil || ask.CreatedAt.After(*stats.lastAsk) {
				stats.lastAsk = &ask.CreatedAt
				stats.lastQuestion = ask.Question
			}
		}
		weekAgo := time.Now().UTC().Add(-7 * 24 * time.Hour)
		rows := make([]UserMetricsRow, 0, len(users))
		for _, user := range users {
			// Guarantee a non-nil stats entry: the row reads its fields
			// directly, and a nil dereference here 502s the whole tab.
			stats := askStats[user.ID]
			if stats == nil {
				stats = &userAskStats{}
				askStats[user.ID] = stats
			}
			row := UserMetricsRow{
				ID: user.ID, Name: user.Name, Role: user.Role, Active: user.Active, OrgUnitID: user.OrgUnitID,
				AsksOpen:        stats.open,
				AsksTotal:       stats.total,
				WaitSeconds:     stats.waitSeconds,
				LastAskAt:       stats.lastAsk,
				LastAskQuestion: stats.lastQuestion,
				HABSeconds:      stats.habSeconds,
				TimedOut:        stats.timedOut,
			}
			runs, err := ws.ListRunsByActor(r.Context(), user.ID, 200)
			if err == nil {
				row.RunsTotal = len(runs)
				for _, run := range runs {
					if !run.CreatedAt.Before(weekAgo) {
						row.RunsWeek++
					}
					if run.Status == "failed" {
						row.FailedRuns++
					}
					if row.LastRunAt == nil || run.CreatedAt.After(*row.LastRunAt) {
						last := run.CreatedAt
						row.LastRunAt = &last
						lastRun := &UserLastRun{Project: run.ProjectID, Status: run.Status, RunID: run.ID}
						if run.TaskID != "" {
							if task, err := ws.GetTask(r.Context(), run.TaskID); err == nil {
								lastRun.Title = task.Title
							}
						}
						row.LastRun = lastRun
					}
				}
			}
			rows = append(rows, row)
		}
		// The live tail first: most recent activity, then name for stability.
		sort.Slice(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			switch {
			case (a.LastRunAt == nil) != (b.LastRunAt == nil):
				return a.LastRunAt != nil
			case a.LastRunAt != nil && b.LastRunAt != nil && !a.LastRunAt.Equal(*b.LastRunAt):
				return a.LastRunAt.After(*b.LastRunAt)
			default:
				return a.Name < b.Name
			}
		})
		writeJSON(w, 200, map[string]any{"users": rows})
	})
}

// userAskStats folds one user's human requests into the numbers the tab
// renders; every user gets a non-nil entry before the rows are built.
type userAskStats struct {
	total        int
	open         int
	timedOut     int
	decisions    int
	waitSeconds  int64
	habSeconds   int64
	lastAsk      *time.Time
	lastQuestion string
}
