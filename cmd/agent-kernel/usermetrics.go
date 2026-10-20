package main

import (
	"net/http"
	"sort"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// UserMetricsRow is one user's activity rollup for the dashboard's Users tab:
// what they run (runs, recency), what waits on them (open asks), and what
// they already decided (answered asks, with the human wait — the HWT side of
// docs/supervisor-model.md §5, attributed per resolved user).
type UserMetricsRow struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Role      string     `json:"role,omitempty"`
	Active    bool       `json:"active"`
	OrgUnitID string     `json:"org_unit_id,omitempty"`
	RunsTotal int        `json:"runs_total"`
	RunsWeek  int        `json:"runs_week"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	// Asks addressed to this user: open right now and answered overall.
	AsksOpen    int        `json:"asks_open"`
	AsksTotal   int        `json:"asks_total"`
	WaitSeconds int64      `json:"wait_seconds"` // human wait on ANSWERED asks (HWT slice)
	LastAskAt   *time.Time `json:"last_ask_at,omitempty"`
}

// registerUserMetricsRoutes serves the dashboard Users tab: one row per
// workspace user with runs/asks/latency metrics. Runs are attributed through
// exec_context actor (the same match ListRunsByActor uses — API, trigger and
// intake entrypoints all stamp it); asks through resolved_user.
func registerUserMetricsRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store) {
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
			if ask.Status == workspace.HumanStatusPending || ask.Status == workspace.HumanStatusDelivered {
				stats.open++
			}
			if ask.AnsweredAt != nil {
				stats.waitSeconds += int64(ask.AnsweredAt.Sub(ask.CreatedAt).Seconds())
			}
			if stats.lastAskAt == nil || ask.CreatedAt.After(*stats.lastAskAt) {
				stats.lastAskAt = &ask.CreatedAt
			}
		}
		weekAgo := time.Now().UTC().Add(-7 * 24 * time.Hour)
		rows := make([]UserMetricsRow, 0, len(users))
		for _, user := range users {
			row := UserMetricsRow{
				ID: user.ID, Name: user.Name, Role: user.Role, Active: user.Active, OrgUnitID: user.OrgUnitID,
				AsksOpen:    askStats[user.ID].askOpen(),
				AsksTotal:   askStats[user.ID].total,
				WaitSeconds: askStats[user.ID].waitSeconds,
				LastAskAt:   askStats[user.ID].lastAskAt,
			}
			runs, err := ws.ListRunsByActor(r.Context(), user.ID, 500)
			if err == nil {
				row.RunsTotal = len(runs)
				for _, run := range runs {
					if !run.CreatedAt.Before(weekAgo) {
						row.RunsWeek++
					}
					if row.LastRunAt == nil || run.CreatedAt.After(*row.LastRunAt) {
						last := run.CreatedAt
						row.LastRunAt = &last
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

// userAskStats folds one user's human requests into the three numbers the tab
// renders; the nil-safe helpers keep the map lookups terse.
type userAskStats struct {
	total       int
	open        int
	waitSeconds int64
	lastAskAt   *time.Time
}

func (s *userAskStats) askOpen() int {
	if s == nil {
		return 0
	}
	return s.open
}
