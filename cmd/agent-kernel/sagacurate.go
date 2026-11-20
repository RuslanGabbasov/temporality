package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

// Saga curation (docs/sagas.md §8.1): the supervisor judge walks the recent
// tasks of a project and re-points them at the right sagas. The assignment
// agents produce is noisy — near-duplicate sagas, tasks bound to the wrong
// effort — and the operator ends up merging by hand. The model returns a
// plan (task → existing saga, tasks → new saga, near-duplicates → merge);
// the endpoint validates every id against the projection, applies it through
// the same saga.* journal events the UI uses, and reports what it did.
// Dry-run by default: the plan comes back unapplied.

const (
	// Cap the prompt: 120 freshest tasks and 80 live sagas keep the model
	// call inside a sane completion budget.
	curateTaskCap = 120
	curateSagaCap = 80
	curateDays    = 14
	curateMaxOps  = 200
)

type sagaCurateRoutesDeps struct {
	mux         *http.ServeMux
	gate        *controlplane.Gate
	events      sagaLifecycleOutbox
	costAPI     *cost.CostAPI
	ws          *workspace.Store
	modelClient func(ctx context.Context) (*llm.Client, error)
	log         *slog.Logger
}

type sagaCurateAssignment struct {
	TaskID string `json:"task_id"`
	SagaID string `json:"saga_id"`
}

type sagaCurateNewSaga struct {
	Title   string   `json:"title"`
	Goal    string   `json:"goal,omitempty"`
	TaskIDs []string `json:"task_ids"`
}

type sagaCurateMerge struct {
	Into    string   `json:"into"`
	Sources []string `json:"sources"`
}

type sagaCuratePlan struct {
	Assignments []sagaCurateAssignment `json:"assignments"`
	NewSagas    []sagaCurateNewSaga    `json:"new_sagas"`
	Merges      []sagaCurateMerge      `json:"merges"`
	Notes       string                 `json:"notes,omitempty"`
}

type sagaCurateReport struct {
	Applied     bool     `json:"applied"`
	Assignments int      `json:"assignments"`
	NewSagas    []string `json:"new_sagas"`
	Merges      int      `json:"merges"`
}

// RegisterSagaCurateRoutes mounts the supervisor saga-curation endpoint.
// Writer-gated: the apply path rebinds tasks and merges sagas.
func RegisterSagaCurateRoutes(deps sagaCurateRoutesDeps) {
	deps.mux.HandleFunc("POST /v1/workspace/projects/{project}/sagas/curate", func(w http.ResponseWriter, r *http.Request) {
		project := r.PathValue("project")
		if !deps.gate.Allow(w, r, controlplane.RoleWriter, project) {
			return
		}
		var input struct {
			Days  int  `json:"days"`
			Apply bool `json:"apply"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeError(w, 400, err)
			return
		}
		days := input.Days
		if days <= 0 {
			days = curateDays
		}
		if days > 90 {
			days = 90
		}

		budget, err := deps.costAPI.ProjectBudgetData(r.Context(), project)
		if err != nil {
			writeError(w, 502, fmt.Errorf("loading the project budget failed: %w", err))
			return
		}

		// Freshest tasks first, capped: the mentor curates the live tail, not
		// the archive.
		tasks := make([]cost.TaskRollup, len(budget.Tasks))
		copy(tasks, budget.Tasks)
		sort.SliceStable(tasks, func(i, j int) bool {
			return taskStamp(tasks[i]).After(taskStamp(tasks[j]))
		})
		cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
		recent := make([]cost.TaskRollup, 0, len(tasks))
		for _, task := range tasks {
			if taskStamp(task).Before(cutoff) {
				break
			}
			recent = append(recent, task)
			if len(recent) >= curateTaskCap {
				break
			}
		}
		if len(recent) == 0 {
			writeJSON(w, 200, map[string]any{"plan": sagaCuratePlan{}, "report": sagaCurateReport{}, "note": "no recent tasks in the window"})
			return
		}
		liveSagas := make([]cost.SagaRollup, 0, len(budget.Sagas))
		for _, saga := range budget.Sagas {
			if saga.MergedInto == "" {
				liveSagas = append(liveSagas, saga)
				if len(liveSagas) >= curateSagaCap {
					break
				}
			}
		}

		// Spend attribution: the usage hook reads the purpose off the ctx.
		curateCtx := withSupervisorPurpose(r.Context(), "saga_curation")
		client, mErr := deps.modelClient(curateCtx)
		if mErr != nil {
			writeError(w, 503, fmt.Errorf("the judge model is not configured — curation needs it: %w", mErr))
			return
		}
		plan, perr := buildSagaCuratePlan(curateCtx, client, recent, liveSagas)
		if perr != nil {
			deps.log.Warn("saga curate: model", "project", project, "error", perr)
			writeError(w, 502, fmt.Errorf("the curation model call failed: %w", perr))
			return
		}

		// Validate every id against the projection: unknown task/saga ids from
		// the model are dropped, assignments equal to the current binding are
		// skipped, merges of unknown sagas are dropped, tasks bound to a saga
		// being merged away re-point at the merge target.
		taskIDs := map[string]bool{}
		taskSaga := map[string]string{}
		for _, task := range recent {
			taskIDs[task.TaskID] = true
			taskSaga[task.TaskID] = task.SagaID
		}
		sagaKnown := map[string]bool{}
		for _, saga := range liveSagas {
			sagaKnown[saga.SagaID] = true
		}
		plan.Assignments = keepCurateAssignments(plan.Assignments, curateMaxOps, func(a sagaCurateAssignment) (string, bool) {
			return a.TaskID, taskIDs[a.TaskID] && sagaKnown[a.SagaID] && taskSaga[a.TaskID] != a.SagaID
		})
		mergedInto := map[string]string{}
		merges := make([]sagaCurateMerge, 0, len(plan.Merges))
		for _, m := range plan.Merges {
			if !sagaKnown[m.Into] {
				continue
			}
			sources := make([]string, 0, len(m.Sources))
			for _, source := range m.Sources {
				if source == m.Into || !sagaKnown[source] || mergedInto[source] != "" {
					continue
				}
				sources = append(sources, source)
			}
			if len(sources) == 0 {
				continue
			}
			for _, source := range sources {
				mergedInto[source] = m.Into
			}
			merges = append(merges, sagaCurateMerge{Into: m.Into, Sources: sources})
		}
		plan.Merges = merges
		for i := range plan.Assignments {
			if target, ok := mergedInto[plan.Assignments[i].SagaID]; ok {
				plan.Assignments[i].SagaID = target
			}
		}
		plan.NewSagas = keepCurateNewSagas(plan.NewSagas, func(ns sagaCurateNewSaga) bool {
			return strings.TrimSpace(ns.Title) != "" && len(ns.TaskIDs) > 0
		})

		if !input.Apply {
			writeJSON(w, 200, map[string]any{"plan": plan, "report": sagaCurateReport{Applied: false}})
			return
		}

		actor := requestUserID(r)
		report := applySagaCuratePlan(r.Context(), deps.events, project, plan, actor)
		_ = deps.ws.RecordAccessAudit(r.Context(), actor, "saga.curated", "project", project,
			map[string]any{"assignments": report.Assignments, "new_sagas": len(report.NewSagas), "merges": report.Merges})
		writeJSON(w, 200, map[string]any{"plan": plan, "report": report})
	})
}

// recentSagaTasks: the freshest window of tasks, capped.
func recentSagaTasks(budget cost.ProjectBudget, days, cap int) []cost.TaskRollup {
	tasks := make([]cost.TaskRollup, len(budget.Tasks))
	copy(tasks, budget.Tasks)
	sort.SliceStable(tasks, func(i, j int) bool {
		return taskStamp(tasks[i]).After(taskStamp(tasks[j]))
	})
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	recent := make([]cost.TaskRollup, 0, len(tasks))
	for _, task := range tasks {
		if taskStamp(task).Before(cutoff) {
			break
		}
		recent = append(recent, task)
		if len(recent) >= cap {
			break
		}
	}
	return recent
}

func liveSagaList(budget cost.ProjectBudget, cap int) []cost.SagaRollup {
	out := make([]cost.SagaRollup, 0, len(budget.Sagas))
	for _, saga := range budget.Sagas {
		if saga.MergedInto == "" {
			out = append(out, saga)
			if len(out) >= cap {
				break
			}
		}
	}
	return out
}

// validateSagaCuratePlan checks every model id against the projection:
// unknown ids dropped, no-op bindings skipped, tasks of a merged-away saga
// re-pointed at the merge target.
func validateSagaCuratePlan(plan sagaCuratePlan, recent []cost.TaskRollup, liveSagas []cost.SagaRollup) sagaCuratePlan {
	taskIDs := map[string]bool{}
	taskSaga := map[string]string{}
	for _, task := range recent {
		taskIDs[task.TaskID] = true
		taskSaga[task.TaskID] = task.SagaID
	}
	sagaKnown := map[string]bool{}
	for _, saga := range liveSagas {
		sagaKnown[saga.SagaID] = true
	}
	plan.Assignments = keepCurateAssignments(plan.Assignments, curateMaxOps, func(a sagaCurateAssignment) (string, bool) {
		return a.TaskID, taskIDs[a.TaskID] && sagaKnown[a.SagaID] && taskSaga[a.TaskID] != a.SagaID
	})
	mergedInto := map[string]string{}
	merges := make([]sagaCurateMerge, 0, len(plan.Merges))
	for _, m := range plan.Merges {
		if !sagaKnown[m.Into] {
			continue
		}
		sources := make([]string, 0, len(m.Sources))
		for _, source := range m.Sources {
			if source == m.Into || !sagaKnown[source] || mergedInto[source] != "" {
				continue
			}
			sources = append(sources, source)
		}
		if len(sources) == 0 {
			continue
		}
		for _, source := range sources {
			mergedInto[source] = m.Into
		}
		merges = append(merges, sagaCurateMerge{Into: m.Into, Sources: sources})
	}
	plan.Merges = merges
	for i := range plan.Assignments {
		if target, ok := mergedInto[plan.Assignments[i].SagaID]; ok {
			plan.Assignments[i].SagaID = target
		}
	}
	plan.NewSagas = keepCurateNewSagas(plan.NewSagas, func(ns sagaCurateNewSaga) bool {
		return strings.TrimSpace(ns.Title) != "" && len(ns.TaskIDs) > 0
	})
	return plan
}

// applySagaCuratePlan emits the plan through the journal: register new sagas
// → attach/re-bind tasks → merge duplicates.
func applySagaCuratePlan(ctx context.Context, events sagaLifecycleOutbox, project string, plan sagaCuratePlan, actor string) sagaCurateReport {
	report := sagaCurateReport{Applied: true, NewSagas: []string{}}
	for _, ns := range plan.NewSagas {
		sagaID := agent.SagaID(ns.Title, ns.TaskIDs[0])
		data := map[string]any{"saga_id": sagaID, "title": ns.Title, "origin": "mentor-curation", "actor_id": actor}
		if ns.Goal != "" {
			data["goal"] = ns.Goal
		}
		if err := emitSagaLifecycleEvent(ctx, events, "saga.registered", sagaID, project, ns.TaskIDs[0], actor, data); err != nil {
			break
		}
		report.NewSagas = append(report.NewSagas, ns.Title)
		for _, taskID := range ns.TaskIDs {
			_ = emitSagaLifecycleEvent(ctx, events, "saga.attached", sagaID, project, taskID, actor, map[string]any{
				"saga_id": sagaID, "task_id": taskID, "origin": "mentor-curation", "actor_id": actor,
			})
			report.Assignments++
		}
	}
	for _, a := range plan.Assignments {
		if err := emitSagaLifecycleEvent(ctx, events, "saga.attached", a.SagaID, project, a.TaskID, actor, map[string]any{
			"saga_id": a.SagaID, "task_id": a.TaskID, "origin": "mentor-curation", "actor_id": actor,
		}); err != nil {
			break
		}
		report.Assignments++
	}
	for _, m := range plan.Merges {
		if err := emitSagaLifecycleEvent(ctx, events, "saga.merged", m.Into, project, "", actor, map[string]any{
			"saga_id": m.Into, "source_saga_ids": m.Sources, "origin": "mentor-curation", "actor_id": actor,
		}); err != nil {
			break
		}
		report.Merges++
	}
	return report
}

// keepCurateAssignments keeps the first cap valid, deduplicated assignments.
func keepCurateAssignments(items []sagaCurateAssignment, cap int, valid func(sagaCurateAssignment) (string, bool)) []sagaCurateAssignment {
	out := make([]sagaCurateAssignment, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		id, ok := valid(item)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, item)
		if len(out) >= cap {
			break
		}
	}
	return out
}

// keepCurateNewSagas drops empty-titled or task-less new sagas and strips
// task ids that are no longer unbound.
func keepCurateNewSagas(items []sagaCurateNewSaga, valid func(sagaCurateNewSaga) bool) []sagaCurateNewSaga {
	out := make([]sagaCurateNewSaga, 0, len(items))
	for _, ns := range items {
		if valid(ns) {
			out = append(out, ns)
		}
	}
	return out
}

func taskStamp(task cost.TaskRollup) time.Time {
	if task.FirstStartedAt != nil {
		return *task.FirstStartedAt
	}
	return time.Time{}
}

// buildSagaCuratePlan asks the judge to re-point recent tasks at the right
// sagas. The prompt carries ids verbatim: the reply may only reference what
// it was given (the validator re-checks anyway).
func buildSagaCuratePlan(ctx context.Context, model *llm.Client, tasks []cost.TaskRollup, sagas []cost.SagaRollup) (sagaCuratePlan, error) {
	var tasksDesc, sagasDesc strings.Builder
	for _, task := range tasks {
		bound := task.SagaID
		if bound == "" {
			bound = "—"
		}
		fmt.Fprintf(&tasksDesc, "- %q: %s (сейчас: %s)\n", task.TaskID, task.Title, bound)
	}
	for _, saga := range sagas {
		goal := ""
		if saga.Goal != "" {
			goal = " — " + saga.Goal
		}
		fmt.Fprintf(&sagasDesc, "- %q: %q%s\n", saga.SagaID, saga.Title, goal)
	}
	prompt := fmt.Sprintf(`Разложи последние задачи проекта по сагам (тематическим группам работы).

ЗАДАЧИ (id, название, текущая сага):
%s

СУЩЕСТВУЮЩИЕ САГИ (id, название — цель):
%s

Правила:
- задача попадает в сагу с подходящей темой; если ни одна существующая не подходит — отнеси её к НОВОЙ саге с коротким осмысленным названием (не id!);
- саги, которые очевидно об одном и том же (варианты названия одной работы), объединяй: источники складываются в целевую с самим полным названием;
- не трогай задачи, уже привязанные корректно;
- используй только id из списков выше;
- если всё уже разложено правильно — верни пустые списки.

Ответ одним JSON-объектом:
{"assignments": [{"task_id": "...", "saga_id": "..."}], "new_sagas": [{"title": "...", "task_ids": ["..."]}], "merges": [{"into": "...", "sources": ["..."]}]}`,
		tasksDesc.String(), sagasDesc.String())

	var plan sagaCuratePlan
	_, err := agent.CompleteParsed(ctx, model, []llm.Message{
		{Role: "system", Content: "You curate Temporality sagas: you group recent tasks into the right thematic sagas. You answer with JSON only."},
		{Role: "user", Content: prompt},
	}, func(content string) (sagaCuratePlan, error) {
		if err := json.Unmarshal([]byte(content), &plan); err != nil {
			return plan, err
		}
		return plan, nil
	})
	return plan, err
}

// ── Scheduled curation (§8.1, supervisor-driven) ───────────────────────────
// The model pass runs on a schedule — nobody clicks a button. Every cycle
// the judge re-points the recent tasks of every project that saw activity:
// assignments and new sagas apply silently, merges apply only for dormant
// sagas (no running tasks — regrouping live work under a human is rude).
// A human-created grouping is never touched without running work: the pass
// only fixes what the AGENTS scattered.

const (
	sagaCurateFirstDelay = 45 * time.Minute
	sagaCurateEvery      = 6 * time.Hour
	sagaCurateQuietDays  = 7 // activity within this window keeps a saga «active»
)

// StartSagaCurationLoop runs the scheduled curation pass for every project
// with recent runs.
func StartSagaCurationLoop(ctx context.Context, log *slog.Logger, ws *workspace.Store, events sagaLifecycleOutbox, costAPI *cost.CostAPI, modelClient func(ctx context.Context) (*llm.Client, error)) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(sagaCurateFirstDelay):
		}
		ticker := time.NewTicker(sagaCurateEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runPassGuarded(log, "saga-curation", ctx, func(pctx context.Context) {
					runSagaCurationPass(pctx, log, ws, events, costAPI, modelClient)
				})
			}
		}
	}()
}

func runSagaCurationPass(ctx context.Context, log *slog.Logger, ws *workspace.Store, events sagaLifecycleOutbox, costAPI *cost.CostAPI, modelClient func(ctx context.Context) (*llm.Client, error)) {
	ctx = withSupervisorPurpose(ctx, "saga_curation")
	if _, err := modelClient(ctx); err != nil {
		log.Info("saga curation: judge model not configured — skipping the cycle")
		return
	}
	projects, err := ws.ListProjects(ctx)
	if err != nil {
		log.Warn("saga curation: projects", "error", err)
		return
	}
	for _, project := range projects {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second): // pace the model calls between projects
		}
		budget, err := costAPI.ProjectBudgetData(ctx, project.ID)
		if err != nil {
			log.Warn("saga curation: budget", "project", project.ID, "error", err)
			continue
		}
		recent := recentSagaTasks(budget, curateDays, curateTaskCap)
		if len(recent) == 0 {
			continue
		}
		liveSagas := liveSagaList(budget, curateSagaCap)
		client, mErr := modelClient(ctx)
		if mErr != nil {
			log.Info("saga curation: judge model lost mid-cycle — pausing", "error", mErr)
			return
		}
		plan, perr := buildSagaCuratePlan(ctx, client, recent, liveSagas)
		if perr != nil {
			log.Warn("saga curation: model", "project", project.ID, "error", perr)
			continue
		}
		plan = validateSagaCuratePlan(plan, recent, liveSagas)
		if len(plan.Assignments) == 0 && len(plan.NewSagas) == 0 && len(plan.Merges) == 0 {
			continue
		}
		// A merge of a LIVE saga regroups running work — the scheduled pass
		// merges only dormant duplicates; live ones stay for the human.
		running := map[string]bool{}
		for _, saga := range liveSagas {
			running[saga.SagaID] = saga.Running > 0
		}
		merges := plan.Merges[:0]
		for _, m := range plan.Merges {
			dormant := !running[m.Into]
			for _, source := range m.Sources {
				if running[source] {
					dormant = false
					break
				}
			}
			if dormant {
				merges = append(merges, m)
			}
		}
		plan.Merges = merges
		actor := "supervisor"
		for _, ns := range plan.NewSagas {
			sagaID := agent.SagaID(ns.Title, ns.TaskIDs[0])
			data := map[string]any{"saga_id": sagaID, "title": ns.Title, "origin": "mentor-schedule", "actor_id": actor}
			if ns.Goal != "" {
				data["goal"] = ns.Goal
			}
			if err := emitSagaLifecycleEvent(ctx, events, "saga.registered", sagaID, project.ID, ns.TaskIDs[0], actor, data); err != nil {
				log.Warn("saga curation: register", "project", project.ID, "error", err)
				break
			}
			for _, taskID := range ns.TaskIDs {
				_ = emitSagaLifecycleEvent(ctx, events, "saga.attached", sagaID, project.ID, taskID, actor, map[string]any{
					"saga_id": sagaID, "task_id": taskID, "origin": "mentor-schedule", "actor_id": actor,
				})
			}
		}
		for _, a := range plan.Assignments {
			if err := emitSagaLifecycleEvent(ctx, events, "saga.attached", a.SagaID, project.ID, a.TaskID, actor, map[string]any{
				"saga_id": a.SagaID, "task_id": a.TaskID, "origin": "mentor-schedule", "actor_id": actor,
			}); err != nil {
				log.Warn("saga curation: attach", "project", project.ID, "error", err)
				break
			}
		}
		log.Info("saga curation pass", "project", project.ID,
			"assignments", len(plan.Assignments), "new_sagas", len(plan.NewSagas), "merges", len(plan.Merges))
	}
}
