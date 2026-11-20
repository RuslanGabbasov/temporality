package main

// Per-user period dossier for the dashboard's Users tab (docs/system-dashboard.md):
// GET /v1/workspace/users/{id}/stats?window=… answers «who is this person in
// this window» — runs and their model spend, sagas and knowledge they
// contributed to, the asks on their plate, and the problems tied to their
// work (failed runs, expired asks, open supervisor cases about their runs).
// The row list stays cheap; this endpoint is fetched once per expansion.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// userStatsWindows are the period presets the tab offers. The values are the
// `window` query params the endpoint understands ("all" drops the bound).
var userStatsWindows = map[string]time.Duration{
	"1h":   time.Hour,
	"24h":  24 * time.Hour,
	"168h": 7 * 24 * time.Hour,
	"720h": 30 * 24 * time.Hour,
}

// userStatsFrom resolves the window param into (normalized key, lower time
// bound). Empty defaults to the week; "all" returns the zero bound (the page
// caps below bound the walk instead).
func userStatsFrom(raw string) (string, time.Time, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		key = "168h"
	}
	if key == "all" {
		return key, time.Time{}, nil
	}
	duration, ok := userStatsWindows[key]
	if !ok {
		return "", time.Time{}, fmt.Errorf("window must be one of 1h, 24h, 168h, 720h, all")
	}
	return key, time.Now().UTC().Add(-duration), nil
}

// UserStats is the expanded-row dossier of one user for one window.
type UserStats struct {
	UserID    string             `json:"user_id"`
	Window    string             `json:"window"`
	From      *time.Time         `json:"from,omitempty"`
	Runs      UserRunsStats      `json:"runs"`
	ByProject []UserProjectStat  `json:"by_project"`
	ByAgent   []UserAgentStat    `json:"by_agent"`
	Sagas     []UserSagaStat     `json:"sagas"`
	Knowledge UserKnowledgeStats `json:"knowledge"`
	Asks      UserAsksStats      `json:"asks"`
	Problems  UserProblemsStats  `json:"problems"`
}

// UserRunsStats is the run rollup plus the model spend attributed to them.
type UserRunsStats struct {
	Total      int     `json:"total"`
	Succeeded  int     `json:"succeeded"`
	Failed     int     `json:"failed"`
	Cancelled  int     `json:"cancelled"`
	TurnLimit  int     `json:"turn_limit"`
	Other      int     `json:"other"`
	Tokens     int64   `json:"tokens"`
	CostUSD    float64 `json:"cost_usd"`
	ModelCalls int     `json:"model_calls"`
	Projects   int     `json:"projects"`
	Agents     int     `json:"agents"`
}

// UserProjectStat is one project row of the by-project spend breakdown.
type UserProjectStat struct {
	ProjectID string  `json:"project_id"`
	Runs      int     `json:"runs"`
	Failed    int     `json:"failed"`
	Tokens    int64   `json:"tokens"`
	CostUSD   float64 `json:"cost_usd"`
}

// UserAgentStat is one agent row of the by-agent breakdown.
type UserAgentStat struct {
	AgentID string  `json:"agent_id"`
	Runs    int     `json:"runs"`
	Failed  int     `json:"failed"`
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// UserSagaStat is one saga the user touched: registered/attached it, or ran
// bound tasks inside it (role: registered > attached > work).
type UserSagaStat struct {
	SagaID  string `json:"saga_id"`
	Title   string `json:"title,omitempty"`
	Project string `json:"project,omitempty"`
	Runs    int    `json:"runs"`
	Role    string `json:"role"`
}

// UserKnowledgeStat is one knowledge proposal the user authored in-window.
type UserKnowledgeStat struct {
	KnowledgeID string    `json:"knowledge_id"`
	Proposition string    `json:"proposition,omitempty"`
	Project     string    `json:"project,omitempty"`
	At          time.Time `json:"at"`
}

// UserKnowledgeStats is the knowledge-contribution block.
type UserKnowledgeStats struct {
	Proposed int                 `json:"proposed"`
	Items    []UserKnowledgeStat `json:"items"`
}

// UserAskRecent is one ask row of the recent-questions list.
type UserAskRecent struct {
	Question string    `json:"question"`
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
	RunID    string    `json:"run_id,omitempty"`
}

// UserAsksStats is the ask_human block: what waits on the user and what they
// already decided inside the window (open asks are current state, not windowed).
type UserAsksStats struct {
	Open        int             `json:"open"`
	Answered    int             `json:"answered"`
	TimedOut    int             `json:"timed_out"`
	WaitSeconds int64           `json:"wait_seconds"`
	HABSeconds  int64           `json:"hab_seconds"`
	Recent      []UserAskRecent `json:"recent"`
}

// UserFailedRun is one failed run of the window, ready to deep-link.
type UserFailedRun struct {
	RunID   string    `json:"run_id"`
	Title   string    `json:"title,omitempty"`
	Project string    `json:"project,omitempty"`
	Status  string    `json:"status"`
	Error   string    `json:"error,omitempty"`
	At      time.Time `json:"at"`
}

// UserSupervisorProblem is an open supervisor case about the user's work.
type UserSupervisorProblem struct {
	Family   string    `json:"family"`
	Cause    string    `json:"cause,omitempty"`
	Subject  string    `json:"subject"`
	Summary  string    `json:"summary,omitempty"`
	Status   string    `json:"status"`
	Project  string    `json:"project,omitempty"`
	LastSeen time.Time `json:"last_seen"`
}

// UserProblemsStats is the «what is wrong around this person» block.
type UserProblemsStats struct {
	FailedRuns    []UserFailedRun         `json:"failed_runs"`
	TimedOutAsks  []UserAskRecent         `json:"timed_out_asks"`
	Supervisor    []UserSupervisorProblem `json:"supervisor"`
	SupervisorAll int                     `json:"supervisor_all"`
}

// registerUserStatsRoutes mounts the expanded-row dossier next to the row
// metrics. Reader gate only: it reads the same tables and journal the row
// metrics already expose, just folded per period.
func registerUserStatsRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store, factors cost.HumanFactors, observationURL, journalToken string, prices cost.Config) {
	mux.HandleFunc("GET /v1/workspace/users/{userID}/stats", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		windowKey, from, err := userStatsFrom(r.URL.Query().Get("window"))
		if err != nil {
			writeError(w, 400, err)
			return
		}
		userID := r.PathValue("userID")
		stats, err := buildUserStats(r.Context(), ws, userID, from, observationURL, journalToken, prices, factors)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		stats.UserID = userID
		stats.Window = windowKey
		if !from.IsZero() {
			stats.From = &from
		}
		writeJSON(w, 200, stats)
	})
}

// userRunAgg accumulates one run's model spend; keyed by both workspace_run
// ids (id and run_id) because the journal's context.run matches run_id while
// some legacy paths pass the row id.
type userRunAgg struct {
	project string
	agent   string
	tokens  int64
	cost    float64
	calls   int
}

// buildUserStats folds the window's truth for one user. Journal folds are
// best-effort per section: a journal outage must degrade a section to its
// DB-only numbers, not 500 the whole dossier — only the DB reads (runs,
// asks) are fatal.
func buildUserStats(ctx context.Context, ws *workspace.Store, userID string, from time.Time, observationURL, journalToken string, prices cost.Config, factors cost.HumanFactors) (UserStats, error) {
	stats := UserStats{}
	runs, err := ws.ListRunsByActorSince(ctx, userID, from, 2000)
	if err != nil {
		return stats, err
	}

	runIndex := map[string]*userRunAgg{}
	runAggs := make([]*userRunAgg, 0, len(runs))
	taskRuns := map[string]int{}
	runIDs := map[string]bool{}
	taskIDs := map[string]bool{}
	projectAgg := map[string]*UserProjectStat{}
	agentAgg := map[string]*UserAgentStat{}
	for i := range runs {
		run := &runs[i]
		agg := &userRunAgg{project: run.ProjectID, agent: run.AgentID}
		runAggs = append(runAggs, agg)
		runIndex[run.ID] = agg
		if run.RunID != "" && run.RunID != run.ID {
			runIndex[run.RunID] = agg
		}
		runIDs[run.ID] = true
		if run.RunID != "" {
			runIDs[run.RunID] = true
		}
		if run.TaskID != "" {
			taskRuns[run.TaskID]++
			taskIDs[run.TaskID] = true
		}
		switch run.Status {
		case "succeeded", "completed":
			stats.Runs.Succeeded++
		case "failed":
			stats.Runs.Failed++
		case "cancelled":
			stats.Runs.Cancelled++
		case "turn_limit":
			stats.Runs.TurnLimit++
		default:
			stats.Runs.Other++
		}
		if p, ok := projectAgg[run.ProjectID]; ok {
			p.Runs++
			if run.Status == "failed" {
				p.Failed++
			}
		} else {
			projectAgg[run.ProjectID] = &UserProjectStat{ProjectID: run.ProjectID, Runs: 1}
			if run.Status == "failed" {
				projectAgg[run.ProjectID].Failed = 1
			}
		}
		if run.AgentID != "" {
			if a, ok := agentAgg[run.AgentID]; ok {
				a.Runs++
				if run.Status == "failed" {
					a.Failed++
				}
			} else {
				agentAgg[run.AgentID] = &UserAgentStat{AgentID: run.AgentID, Runs: 1}
				if run.Status == "failed" {
					agentAgg[run.AgentID].Failed = 1
				}
			}
		}
	}
	stats.Runs.Total = len(runs)
	stats.Runs.Projects = len(projectAgg)
	stats.Runs.Agents = len(agentAgg)

	// Model spend: price the window's model.completed events onto this
	// user's runs (the journal has no per-user attribution — the run does).
	usageEvents, usageErr := userStatsJournalPages(ctx, observationURL, journalToken, "model.completed", from, "", 20)
	if usageErr == nil {
		tokens, usd, calls := foldUserUsage(usageEvents, runIndex, prices)
		stats.Runs.Tokens, stats.Runs.CostUSD, stats.Runs.ModelCalls = tokens, usd, calls
		// The same per-run spend feeds the breakdowns below (runAggs holds
		// one entry per run — runIndex deliberately double-keys id/run_id).
		for _, agg := range runAggs {
			if p, ok := projectAgg[agg.project]; ok {
				p.Tokens += agg.tokens
				p.CostUSD += agg.cost
			}
			if agg.agent != "" {
				if a, ok := agentAgg[agg.agent]; ok {
					a.Tokens += agg.tokens
					a.CostUSD += agg.cost
				}
			}
		}
	}

	// Failed-run deep links: resolve the human task title once per failure.
	for i := range runs {
		run := &runs[i]
		if run.Status != "failed" {
			continue
		}
		if len(stats.Problems.FailedRuns) >= 10 {
			break
		}
		failed := UserFailedRun{
			RunID:   firstNonEmpty(run.RunID, run.ID),
			Project: run.ProjectID,
			Status:  run.Status,
			Error:   run.Error,
			At:      run.CreatedAt,
		}
		if run.TaskID != "" {
			if task, tErr := ws.GetTask(ctx, run.TaskID); tErr == nil {
				failed.Title = task.Title
			}
		}
		stats.Problems.FailedRuns = append(stats.Problems.FailedRuns, failed)
	}

	// Asks addressed to the user: open is current state, everything else is
	// windowed. HAB/HWT reuse the row metrics' coefficients.
	asks, err := ws.ListHumanRequests(ctx, "", "", userID, false)
	if err != nil {
		return stats, err
	}
	recentAsks := []UserAskRecent{}
	for i := range asks {
		ask := &asks[i]
		switch ask.Status {
		case workspace.HumanStatusPending, workspace.HumanStatusDelivered:
			stats.Asks.Open++
			continue
		case workspace.HumanStatusExpired:
			if !from.IsZero() && ask.CreatedAt.Before(from) {
				continue
			}
			stats.Asks.TimedOut++
			stats.Problems.TimedOutAsks = append(stats.Problems.TimedOutAsks, UserAskRecent{
				Question: ask.Question, Status: ask.Status, At: ask.CreatedAt, RunID: ask.RunID,
			})
		case workspace.HumanStatusAnswered:
			if ask.AnsweredAt != nil && !from.IsZero() && ask.AnsweredAt.Before(from) {
				continue
			}
			stats.Asks.Answered++
			if ask.AnsweredAt != nil {
				stats.Asks.WaitSeconds += int64(ask.AnsweredAt.Sub(ask.CreatedAt).Seconds())
			}
			stats.Asks.HABSeconds += int64(factors.ApprovalSeconds)
			if runes := len([]rune(ask.Response)); runes > 0 {
				stats.Asks.HABSeconds += int64(runes/5) * 60 / int64(factors.ReadingWPM)
			}
		default:
			// cancelled etc. — not a decision, not a timeout.
		}
		if !from.IsZero() && ask.CreatedAt.Before(from) {
			continue
		}
		recentAsks = append(recentAsks, UserAskRecent{
			Question: ask.Question, Status: ask.Status, At: ask.CreatedAt, RunID: ask.RunID,
		})
	}
	sort.Slice(recentAsks, func(i, j int) bool { return recentAsks[i].At.After(recentAsks[j].At) })
	if len(recentAsks) > 5 {
		recentAsks = recentAsks[:5]
	}
	stats.Asks.Recent = recentAsks
	sort.Slice(stats.Problems.TimedOutAsks, func(i, j int) bool {
		return stats.Problems.TimedOutAsks[i].At.After(stats.Problems.TimedOutAsks[j].At)
	})
	if len(stats.Problems.TimedOutAsks) > 5 {
		stats.Problems.TimedOutAsks = stats.Problems.TimedOutAsks[:5]
	}

	// Sagas: replay the lifecycle (docs/sagas.md §8) — registrations and
	// attachments the user performed plus every saga whose bound tasks their
	// runs executed. Full history (bindings outlive the window), page-capped.
	if sagaEvents, sErr := userStatsJournalPages(ctx, observationURL, journalToken,
		"saga.registered,saga.attached,saga.detached,saga.renamed,saga.merged,saga.deleted", time.Time{}, "", 40); sErr == nil {
		stats.Sagas = foldUserSagas(sagaEvents, userID, taskRuns)
	}

	// Knowledge contribution: proposals authored by this user in-window. The
	// journal's actor filter is context.actor.id — the same id the runs tab
	// attributes work to.
	if knowledgeEvents, kErr := userStatsJournalPages(ctx, observationURL, journalToken,
		"knowledge.proposed", from, userID, 10); kErr == nil {
		for i := range knowledgeEvents {
			ev := &knowledgeEvents[i]
			if ev.Context.Actor.ID != userID {
				continue
			}
			item := UserKnowledgeStat{Project: ev.Context.Project, At: ev.OccurredAt}
			item.KnowledgeID, _ = ev.Data["knowledge_id"].(string)
			if proposition, ok := ev.Data["proposition"].(string); ok {
				item.Proposition = proposition
			}
			stats.Knowledge.Proposed++
			if len(stats.Knowledge.Items) < 10 {
				stats.Knowledge.Items = append(stats.Knowledge.Items, item)
			}
		}
	}

	// Supervisor attention about this person's work: open cases whose subject
	// or payload names the user, their runs or their tasks.
	if queue, qErr := ws.ListSupervisorQueue(ctx, workspace.SupervisorQueueFilter{
		Status: workspace.SupervisorCaseOpen, Limit: 500,
	}); qErr == nil {
		ids := map[string]bool{userID: true}
		for id := range runIDs {
			ids[id] = true
		}
		for id := range taskIDs {
			ids[id] = true
		}
		for i := range queue.Cases {
			c := &queue.Cases[i]
			if !userStatsCaseMatches(c.Subject, c.Payload, ids) {
				continue
			}
			stats.Problems.SupervisorAll++
			if len(stats.Problems.Supervisor) >= 10 {
				continue
			}
			stats.Problems.Supervisor = append(stats.Problems.Supervisor, UserSupervisorProblem{
				Family: c.Family, Cause: c.Cause, Subject: c.Subject, Summary: c.Summary,
				Status: c.Status, Project: c.Project, LastSeen: c.LastSeen,
			})
		}
		sort.Slice(stats.Problems.Supervisor, func(i, j int) bool {
			return stats.Problems.Supervisor[i].LastSeen.After(stats.Problems.Supervisor[j].LastSeen)
		})
	}

	for _, p := range projectAgg {
		if p.ProjectID == "" {
			continue
		}
		stats.ByProject = append(stats.ByProject, *p)
	}
	sort.Slice(stats.ByProject, func(i, j int) bool {
		if stats.ByProject[i].Runs != stats.ByProject[j].Runs {
			return stats.ByProject[i].Runs > stats.ByProject[j].Runs
		}
		return stats.ByProject[i].ProjectID < stats.ByProject[j].ProjectID
	})
	if len(stats.ByProject) > 12 {
		stats.ByProject = stats.ByProject[:12]
	}
	for _, a := range agentAgg {
		stats.ByAgent = append(stats.ByAgent, *a)
	}
	sort.Slice(stats.ByAgent, func(i, j int) bool {
		if stats.ByAgent[i].Runs != stats.ByAgent[j].Runs {
			return stats.ByAgent[i].Runs > stats.ByAgent[j].Runs
		}
		return stats.ByAgent[i].AgentID < stats.ByAgent[j].AgentID
	})
	if len(stats.ByAgent) > 12 {
		stats.ByAgent = stats.ByAgent[:12]
	}
	return stats, nil
}

// userStatsJournalPages walks up to maxPages typed journal pages for one
// event-type query. A zero `since` omits the bound; `actor` narrows to one
// context actor (the journal's actor_id column).
func userStatsJournalPages(ctx context.Context, observationURL, token, types string, since time.Time, actor string, maxPages int) ([]observation.Event, error) {
	params := url.Values{"types": {types}, "limit": {"500"}, "order": {"asc"}}
	if !since.IsZero() {
		params.Set("since", since.Format(time.RFC3339Nano))
	}
	if actor != "" {
		params.Set("actor", actor)
	}
	out := []observation.Event{}
	cursor := ""
	for page := 0; page < maxPages; page++ {
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		events, next, err := supervisorTypedEventPage(ctx, observationURL, token, params)
		if err != nil {
			return out, err
		}
		out = append(out, events...)
		if next == "" || len(events) == 0 {
			break
		}
		cursor = next
	}
	return out, nil
}

// foldUserUsage prices model.completed events onto the user's own runs and
// returns the (tokens, cost, calls) totals; per-run aggregates accumulate
// into runIndex in place so the by-project/by-agent breakdowns can read them.
func foldUserUsage(events []observation.Event, runIndex map[string]*userRunAgg, prices cost.Config) (int64, float64, int) {
	var tokens int64
	var usd float64
	var calls int
	for i := range events {
		ev := &events[i]
		if ev.Context.Run == "" {
			continue
		}
		agg, ok := runIndex[ev.Context.Run]
		if !ok {
			continue // someone else's run
		}
		model, _ := ev.Data["model"].(string)
		if model == "" {
			model, _ = ev.Data["provider"].(string)
		}
		prompt := usageInt(ev.Data["prompt_tokens"])
		cached := usageInt(ev.Data["cached_tokens"])
		completion := usageInt(ev.Data["completion_tokens"])
		callTokens := prompt + completion
		callUSD := prices.ModelCost(model, int(prompt), int(cached), int(completion))
		tokens += callTokens
		usd += callUSD
		calls++
		agg.tokens += callTokens
		agg.cost += callUSD
		agg.calls++
	}
	return tokens, usd, calls
}

// foldUserSagas replays saga lifecycle events into the user's saga list.
// Journal order matters: the latest binding/tombstone/merge wins, exactly
// like the cost projection (docs/sagas.md §8).
func foldUserSagas(events []observation.Event, userID string, taskRuns map[string]int) []UserSagaStat {
	type sagaFold struct {
		title      string
		project    string
		role       string
		deleted    bool
		mergedInto string
	}
	sagas := map[string]*sagaFold{}
	taskToSaga := map[string]string{}
	get := func(id string) *sagaFold {
		if f, ok := sagas[id]; ok {
			return f
		}
		f := &sagaFold{}
		sagas[id] = f
		return f
	}
	roleRank := func(role string) int {
		switch role {
		case "registered":
			return 2
		case "attached":
			return 1
		}
		return 0
	}
	setRole := func(f *sagaFold, role string) {
		if roleRank(role) > roleRank(f.role) {
			f.role = role
		}
	}
	for i := range events {
		ev := &events[i]
		sagaID, _ := ev.Data["saga_id"].(string)
		if sagaID == "" {
			continue
		}
		f := get(sagaID)
		if ev.Context.Project != "" {
			f.project = ev.Context.Project
		}
		actor, _ := ev.Data["actor_id"].(string)
		if actor == "" {
			actor = ev.Context.Actor.ID
		}
		taskID, _ := ev.Data["task_id"].(string)
		switch ev.Type {
		case "saga.registered":
			f.deleted = false // a re-registration resurrects the id
			if title, ok := ev.Data["title"].(string); ok && title != "" {
				f.title = title
			}
			if actor == userID {
				setRole(f, "registered")
			}
			if taskID != "" {
				taskToSaga[taskID] = sagaID
			}
		case "saga.attached":
			if actor == userID {
				setRole(f, "attached")
			}
			if taskID != "" {
				taskToSaga[taskID] = sagaID
			}
		case "saga.detached":
			if taskID != "" {
				delete(taskToSaga, taskID)
			}
		case "saga.renamed":
			if title, ok := ev.Data["title"].(string); ok && title != "" {
				f.title = title
			}
		case "saga.merged":
			if sources, ok := ev.Data["source_saga_ids"].([]any); ok {
				for _, raw := range sources {
					if source, ok := raw.(string); ok && source != sagaID {
						get(source).mergedInto = sagaID
					}
				}
			}
		case "saga.deleted":
			f.deleted = true
		}
	}
	resolve := func(id string) string {
		seen := map[string]bool{}
		for {
			f, ok := sagas[id]
			if !ok || f.mergedInto == "" || seen[id] {
				return id
			}
			seen[id] = true
			id = f.mergedInto
		}
	}
	// Merge sources' titles/roles onto their targets, then drop tombstones.
	for _, f := range sagas {
		if f.mergedInto == "" {
			continue
		}
		target := get(resolve(f.mergedInto))
		if target.title == "" {
			target.title = f.title
		}
		if target.project == "" {
			target.project = f.project
		}
		setRole(target, f.role)
	}
	type userSagaAgg struct {
		title   string
		project string
		role    string
		runs    int
	}
	agg := map[string]*userSagaAgg{}
	ensure := func(resolved string) *userSagaAgg {
		if a, ok := agg[resolved]; ok {
			return a
		}
		f := sagas[resolved]
		a := &userSagaAgg{}
		if f != nil {
			a.title = f.title
			a.project = f.project
			a.role = f.role
		}
		agg[resolved] = a
		return a
	}
	for taskID := range taskRuns {
		bound, ok := taskToSaga[taskID]
		if !ok {
			continue
		}
		resolved := resolve(bound)
		if f := sagas[resolved]; f == nil || f.deleted {
			continue
		}
		a := ensure(resolved)
		a.runs += taskRuns[taskID]
	}
	out := []UserSagaStat{}
	for id, a := range agg {
		f := sagas[id]
		if f == nil || f.deleted || (a.runs == 0 && a.role == "") {
			continue
		}
		role := a.role
		if role == "" && a.runs > 0 {
			role = "work" // bound tasks the user executed, no personal action
		}
		out = append(out, UserSagaStat{SagaID: id, Title: a.title, Project: a.project, Runs: a.runs, Role: role})
	}
	// A saga the user registered/attached but with no windowed runs still
	// belongs in the list: ensure() only saw task-bound sagas above, so fold
	// the role-only sagas in here.
	for id, f := range sagas {
		if f.deleted || f.role == "" {
			continue
		}
		resolved := resolve(id)
		if resolved != id {
			continue // the target carries the merged role
		}
		if _, ok := agg[id]; ok {
			continue
		}
		out = append(out, UserSagaStat{SagaID: id, Title: f.title, Project: f.project, Role: f.role})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].Title < out[j].Title
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

// userStatsCaseMatches reports whether an attention case is about this user's
// work: the subject names the user or one of their runs/tasks, or the verdict
// payload quotes one of those ids as a JSON value.
func userStatsCaseMatches(subject string, payload []byte, ids map[string]bool) bool {
	if subject != "" && ids[subject] {
		return true
	}
	if len(payload) == 0 {
		return false
	}
	for id := range ids {
		if len(id) < 4 {
			continue // too short to quote-match honestly
		}
		if bytes.Contains(payload, []byte(`"`+id+`"`)) {
			return true
		}
	}
	return false
}
