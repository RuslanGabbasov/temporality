package main

// Organizational Operations view assembly (docs/temporality-organizational-
// operations-spec.md §10-§13): the Work Board kanban derived from the
// projection, the aggregated plan graph with CONFIRMED links only, and the
// run-details levels of request_related_run_details.
//
// Column derivation never invents a status: it folds the existing run
// lifecycle (org_active_run statuses + the urgent latch that only a
// human.requested / approval.requested / run.failed event can raise) into
// the five board states. «Блокировка» is shown only from a confirming event
// — duration alone never produces it (spec §13).

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/workspace"
)

const (
	orgWorkViewRunCap      = 1000
	orgWorkViewRunsPerCard = 10
	orgStaleThreshold      = 24 * time.Hour
	orgPlanGraphEventCap   = 300
	orgPlanGraphRunNodeCap = 500
)

// Work board columns (§10.3).
const (
	orgColumnPlanned    = "planned"
	orgColumnInProgress = "in_progress"
	orgColumnWaiting    = "waiting"
	orgColumnReview     = "review"
	orgColumnCompleted  = "completed"
)

// buildOrgWorkView assembles the сводный канбан: tasks with derived columns,
// run summaries, awareness counters, the §10.5 metrics and the §13 problem
// list.
func buildOrgWorkView(ctx context.Context, ws *workspace.Store, scope []string) (map[string]any, error) {
	runs, _, err := ws.ListOrgActiveRuns(ctx, workspace.OrgActiveRunFilter{
		OrgNodeIDs: scope, Limit: orgWorkViewRunCap,
	})
	if err != nil {
		return nil, err
	}
	runIDs := make([]string, 0, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.RunID)
	}
	counts, err := ws.OrgAwarenessCountsForRuns(ctx, runIDs)
	if err != nil {
		return nil, err
	}
	// Task verdicts a human recorded on review-column cards: 'dismissed'
	// (analyzed, no action — the card leaves the board) and 'accepted' (the
	// work is accepted — the card presents as «Завершено» until the activity
	// window washes it out). A NEW run for the task clears either marker
	// (UpsertOrgActiveRun).
	verdicts, err := ws.OrgWorkTaskVerdicts(ctx)
	if err != nil {
		return nil, err
	}

	// Group runs per task; runs without a task land in the ungrouped lane.
	byTask := map[string][]workspace.OrgActiveRun{}
	ungrouped := []workspace.OrgActiveRun{}
	for _, run := range runs {
		if run.TaskID == "" {
			ungrouped = append(ungrouped, run)
			continue
		}
		if verdicts[run.TaskID] == "dismissed" {
			continue
		}
		byTask[run.TaskID] = append(byTask[run.TaskID], run)
	}
	taskIDs := make([]string, 0, len(byTask))
	for taskID := range byTask {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)

	now := time.Now().UTC()
	cards := make([]map[string]any, 0, len(byTask))
	metrics := map[string]int{
		"active": 0, "waiting": 0, "review": 0, "completed": 0, "planned": 0,
		"failed_runs": 0, "completed_runs_24h": 0,
	}
	problems := []map[string]any{}
	for _, taskID := range taskIDs {
		taskRuns := byTask[taskID]
		sort.Slice(taskRuns, func(i, j int) bool {
			return orgActiveRunStamp(taskRuns[i]).After(orgActiveRunStamp(taskRuns[j]))
		})
		title := ""
		project := ""
		if task, err := ws.GetTask(ctx, taskID); err == nil {
			title = task.Title
			project = task.ProjectID
		}
		column, waitingReason := orgTaskColumn(taskRuns, ws, ctx, verdicts[taskID] == "accepted")
		card := map[string]any{
			"task_id":        taskID,
			"title":          title,
			"project_id":     project,
			"column":         column,
			"actors":         orgRunActors(taskRuns),
			"org_node_ids":   orgRunNodes(taskRuns),
			"run_count":      len(taskRuns),
			"waiting_reason": waitingReason,
			"last_event_at":  orgTimeOr(orgActiveRunStamp(taskRuns[0]), now),
		}
		flags := []string{}
		for _, run := range taskRuns {
			if run.Status == "failed" {
				flags = append(flags, "failed")
				metrics["failed_runs"]++
				break
			}
		}
		card["flags"] = flags
		// Awareness counters folded across the task's runs (§12 card badges).
		selected, delivered, consumed, details := 0, 0, 0, 0
		for _, run := range taskRuns {
			if count, ok := counts[run.RunID]; ok {
				selected += count.Selected
				delivered += count.Delivered
				consumed += count.Consumed
				details += count.DetailsRequested
			}
		}
		card["awareness"] = map[string]int{
			"selected": selected, "delivered": delivered,
			"consumed": consumed, "details": details,
		}
		runSummaries := make([]map[string]any, 0, len(taskRuns))
		for _, run := range taskRuns {
			metrics["failed_runs"] += 0 // counted above via flags
			if run.Status == "completed" && run.FinishedAt != nil && now.Sub(*run.FinishedAt) <= 24*time.Hour {
				metrics["completed_runs_24h"]++
			}
			if len(runSummaries) < orgWorkViewRunsPerCard {
				// Per-run project (the trace deep-link needs it) and per-run
				// awareness counters (the flow entry shows only where signals
				// actually exist — a bare button on every row was noise).
				runAwareness := counts[run.RunID]
				summary := map[string]any{
					"run_id": run.RunID, "status": run.Status, "title": run.Title,
					"urgent": run.Urgent, "actor_id": run.ActorID,
					"last_event_at": orgTimePtrOr(run.LastEventAt, run.StartedAt),
					"project_id":    run.ProjectID,
					"awareness": map[string]int{
						"selected": runAwareness.Selected, "delivered": runAwareness.Delivered,
						"consumed": runAwareness.Consumed, "details": runAwareness.DetailsRequested,
					},
				}
				runSummaries = append(runSummaries, summary)
			}
			// §13 problems: live runs with no movement past the threshold —
			// informational, never a blocking verdict.
			if run.Status == "running" && run.LastEventAt != nil && now.Sub(*run.LastEventAt) > orgStaleThreshold {
				problems = append(problems, map[string]any{
					"kind": "no_movement", "task_id": taskID, "run_id": run.RunID,
					"title": run.Title, "since": *run.LastEventAt,
				})
			}
			if run.Status == "failed" {
				problems = append(problems, map[string]any{
					"kind": "failed_run", "task_id": taskID, "run_id": run.RunID,
					"title": run.Title,
				})
			}
		}
		card["runs"] = runSummaries
		cards = append(cards, card)
		switch column {
		case orgColumnInProgress:
			metrics["active"]++
		case orgColumnWaiting:
			metrics["waiting"]++
		case orgColumnReview:
			metrics["review"]++
		case orgColumnCompleted:
			metrics["completed"]++
		case orgColumnPlanned:
			metrics["planned"]++
		}
		// §13: waiting on a human — only from the confirming urgent latch.
		if column == orgColumnWaiting {
			problems = append(problems, map[string]any{
				"kind": "waiting_on_human", "task_id": taskID, "reason": waitingReason,
			})
		}
	}

	// Ungrouped runs travel as the same per-run summaries the cards carry
	// (the old payload was a bare count — the inspector could not render a
	// single row of them).
	ungroupedSummaries := make([]map[string]any, 0, len(ungrouped))
	for _, run := range ungrouped {
		runAwareness := counts[run.RunID]
		ungroupedSummaries = append(ungroupedSummaries, map[string]any{
			"run_id": run.RunID, "status": run.Status, "title": run.Title,
			"urgent": run.Urgent, "actor_id": run.ActorID,
			"last_event_at": orgTimePtrOr(run.LastEventAt, run.StartedAt),
			"project_id":    run.ProjectID,
			"awareness": map[string]int{
				"selected": runAwareness.Selected, "delivered": runAwareness.Delivered,
				"consumed": runAwareness.Consumed, "details": runAwareness.DetailsRequested,
			},
		})
	}

	return map[string]any{
		"columns":        []string{orgColumnPlanned, orgColumnInProgress, orgColumnWaiting, orgColumnReview, orgColumnCompleted},
		"cards":          cards,
		"ungrouped_runs": ungroupedSummaries,
		"metrics":        metrics,
		"problems":       problems,
	}, nil
}

// orgTaskColumn derives the board column from the run lifecycle: the urgent
// latch (raised ONLY by a human/approval request or a run failure) means
// waiting; live runs mean progress; terminal-only history lands by its
// outcome. No free-floating status is created (spec §10.3).
// orgTaskColumn derives the board column from the task's run statuses, in
// attention priority: an urgent run waits on its human, a live run is in
// progress, failed work sits at the quality gate («Ревью») — UNLESS a human
// already accepted it (accepted=true: the reviewed card presents as
// «Завершено» and the activity window washes it out later), then completed.
func orgTaskColumn(taskRuns []workspace.OrgActiveRun, ws *workspace.Store, ctx context.Context, accepted bool) (string, string) {
	live, urgentRun, failed, completed := 0, (*workspace.OrgActiveRun)(nil), 0, 0
	for i := range taskRuns {
		switch taskRuns[i].Status {
		case "running":
			live++
			if taskRuns[i].Urgent && urgentRun == nil {
				run := taskRuns[i]
				urgentRun = &run
			}
		case "failed":
			failed++
		case "completed":
			completed++
		}
	}
	if len(taskRuns) == 0 {
		return orgColumnPlanned, ""
	}
	if urgentRun != nil {
		reason := orgWaitingReason(ctx, ws, *urgentRun)
		return orgColumnWaiting, reason
	}
	if live > 0 {
		return orgColumnInProgress, ""
	}
	if failed > 0 && !accepted {
		// Failed work sits at the quality gate for attention — the outcome is
		// visible in the card flags, the history is untouched. A human verdict
		// (accepted) moves it out: the work was reviewed, the board shows it as
		// completed until the window empties the column.
		return orgColumnReview, ""
	}
	if completed > 0 || accepted {
		return orgColumnCompleted, ""
	}
	return orgColumnInProgress, ""
}

// orgWaitingReason resolves the §13 blocking reason from the latest
// confirming event of the run — never from duration.
func orgWaitingReason(ctx context.Context, ws *workspace.Store, run workspace.OrgActiveRun) string {
	events, _, err := ws.ListOrgActivity(ctx, workspace.OrgActivityFilter{
		RunID: run.RunID, Limit: 5,
	})
	if err != nil {
		return ""
	}
	for _, event := range events {
		switch event.EventType {
		case "human.requested":
			return "waiting for human input"
		case "approval.requested":
			return "waiting for approval"
		case "human.answered", "approval.granted", "approval.rejected", "approval.auto_granted":
			return ""
		}
	}
	return ""
}

func orgActiveRunStamp(run workspace.OrgActiveRun) time.Time {
	if run.LastEventAt != nil && !run.LastEventAt.IsZero() {
		return *run.LastEventAt
	}
	if run.StartedAt != nil && !run.StartedAt.IsZero() {
		return *run.StartedAt
	}
	return time.Time{}
}

func orgRunActors(runs []workspace.OrgActiveRun) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, run := range runs {
		if run.ActorID != "" && !seen[run.ActorID] {
			seen[run.ActorID] = true
			out = append(out, run.ActorID)
		}
	}
	return out
}

func orgRunNodes(runs []workspace.OrgActiveRun) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, run := range runs {
		if run.OrgNodeID != "" && !seen[run.OrgNodeID] {
			seen[run.OrgNodeID] = true
			out = append(out, run.OrgNodeID)
		}
	}
	return out
}

func orgTimeOr(at time.Time, fallback time.Time) time.Time {
	if at.IsZero() {
		return fallback
	}
	return at
}

func orgTimePtrOr(at *time.Time, fallback *time.Time) *time.Time {
	if at != nil && !at.IsZero() {
		return at
	}
	return fallback
}

// buildOrgPlanGraph assembles the aggregated §11 graph from the projection.
// Every edge carries its model ground: task binding, delegation event,
// shared artifact ref — name similarity never creates a link (spec §11.1).
func buildOrgPlanGraph(ctx context.Context, ws *workspace.Store, scope []string) (map[string]any, error) {
	runs, _, err := ws.ListOrgActiveRuns(ctx, workspace.OrgActiveRunFilter{
		OrgNodeIDs: scope, Limit: orgPlanGraphRunNodeCap,
	})
	if err != nil {
		return nil, err
	}
	runByID := map[string]workspace.OrgActiveRun{}
	runIDs := []string{}
	for _, run := range runs {
		runByID[run.RunID] = run
		runIDs = append(runIDs, run.RunID)
	}

	nodes := []map[string]any{}
	edges := []map[string]any{}
	seenNode := map[string]bool{}
	addNode := func(id, kind, label, status string, orgNodeID string) {
		if id == "" || seenNode[id] {
			return
		}
		seenNode[id] = true
		nodes = append(nodes, map[string]any{
			"id": id, "kind": kind, "label": label, "status": status,
			"org_node_id": orgNodeID,
		})
	}
	addEdge := func(source, target, kind, evidence string) {
		if source == "" || target == "" || source == target {
			return
		}
		edges = append(edges, map[string]any{
			"source": source, "target": target, "kind": kind, "evidence": evidence,
		})
	}

	// Task nodes bind their runs (workspace_run.task_id → workspace_task).
	tasks := map[string]bool{}
	for _, run := range runs {
		runLabel := run.Title
		if runLabel == "" {
			runLabel = run.RunID
		}
		addNode("run:"+run.RunID, "run", runLabel, run.Status, run.OrgNodeID)
		if run.TaskID != "" {
			tasks[run.TaskID] = true
			addEdge("task:"+run.TaskID, "run:"+run.RunID, "executes", "task binding")
		}
	}
	for taskID := range tasks {
		title := taskID
		if task, err := ws.GetTask(ctx, taskID); err == nil && task.Title != "" {
			title = task.Title
		}
		addNode("task:"+taskID, "task", title, "", "")
	}

	// Delegation edges: parent → child, grounded in delegation.started.
	delegations, _, err := ws.ListOrgActivity(ctx, workspace.OrgActivityFilter{
		OrgNodeIDs: scope, EventTypes: []string{"delegation.started"}, Limit: orgPlanGraphEventCap,
	})
	if err != nil {
		return nil, err
	}
	for _, event := range delegations {
		childID := orgRefString(event.Refs, "child_run_id")
		if childID == "" {
			continue
		}
		parentID := event.RunID
		addNode("run:"+childID, "run", childID, statusOfRun(runByID, childID), event.OrgNodeID)
		addEdge("run:"+parentID, "run:"+childID, "delegates", "delegation.started")
	}

	// Shared-artifact edges: the same artifact ref touched by ≥2 runs — a
	// confirmed §11.1 connection («общий Artifact»).
	artifactEvents, _, err := ws.ListOrgActivity(ctx, workspace.OrgActivityFilter{
		OrgNodeIDs: scope, Category: "artifact", Limit: orgPlanGraphEventCap,
	})
	if err != nil {
		return nil, err
	}
	artifactRuns := map[string][]string{}
	artifactTitles := map[string]string{}
	for _, event := range artifactEvents {
		artifactID := orgRefString(event.Refs, "artifact_id")
		if artifactID == "" || event.RunID == "" {
			continue
		}
		if !containsString(artifactRuns[artifactID], event.RunID) {
			artifactRuns[artifactID] = append(artifactRuns[artifactID], event.RunID)
		}
		if artifactTitles[artifactID] == "" && event.Summary != "" {
			artifactTitles[artifactID] = orgSummaryAfterPrefix(event.Summary)
		}
	}
	for artifactID, linkedRuns := range artifactRuns {
		if len(linkedRuns) < 2 {
			continue // a single-run artifact is not an inter-plan link
		}
		addNode("artifact:"+artifactID, "artifact", artifactTitles[artifactID], "", "")
		for _, runID := range linkedRuns {
			addEdge("run:"+runID, "artifact:"+artifactID, "produces", "artifact ref")
		}
		// The confirmed inter-run link runs through the shared artifact.
		for i := 1; i < len(linkedRuns); i++ {
			addEdge("run:"+linkedRuns[i-1], "run:"+linkedRuns[i], "related", "shared artifact "+artifactID)
		}
	}

	return map[string]any{"nodes": nodes, "edges": edges, "count": len(nodes)}, nil
}

func orgRefString(refs json.RawMessage, key string) string {
	if len(refs) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(refs, &parsed); err != nil {
		return ""
	}
	value, _ := parsed[key].(string)
	return value
}

func statusOfRun(byID map[string]workspace.OrgActiveRun, runID string) string {
	if run, ok := byID[runID]; ok {
		return run.Status
	}
	return "unknown"
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// orgSummaryAfterPrefix strips the event-type label prefix the fold adds to
// artifact summaries ("artifact proposed: Title").
func orgSummaryAfterPrefix(summary string) string {
	if idx := strings.Index(summary, ": "); idx >= 0 {
		return strings.TrimSpace(summary[idx+2:])
	}
	return summary
}

// orgRunDetails serves request_related_run_details (§7.1): every level reads
// the projection only — the trace level returns a POINTER to the existing
// journal API (the caller's own RBAC applies there), never a copy.
func orgRunDetails(ctx context.Context, ws *workspace.Store, runID, level string) map[string]any {
	switch level {
	case "summary":
		details := map[string]any{}
		if run, err := ws.GetOrgActiveRun(ctx, "", runID); err == nil {
			details["run"] = run
		} else {
			// project may differ: fall back to a scoped lookup by run id
			if runs, _, err := ws.ListOrgActiveRuns(ctx, workspace.OrgActiveRunFilter{Limit: orgWorkViewRunCap}); err == nil {
				for _, run := range runs {
					if run.RunID == runID {
						details["run"] = run
						break
					}
				}
			}
		}
		events, _, _ := ws.ListOrgActivity(ctx, workspace.OrgActivityFilter{RunID: runID, Limit: 5})
		summaries := make([]map[string]any, 0, len(events))
		for _, event := range events {
			summaries = append(summaries, map[string]any{
				"type": event.EventType, "summary": event.Summary, "occurred_at": event.OccurredAt,
			})
		}
		details["recent"] = summaries
		return details
	case "findings":
		events, _, _ := ws.ListOrgActivity(ctx, workspace.OrgActivityFilter{
			RunID: runID, EventTypes: []string{
				"plan.task.completed", "plan.task.failed", "supervisor.verdict",
				"verification.reviewed", "behavior.reviewed",
			}, Limit: 20,
		})
		return map[string]any{"findings": orgActivityDigest(events)}
	case "decisions":
		events, _, _ := ws.ListOrgActivity(ctx, workspace.OrgActivityFilter{
			RunID: runID, EventTypes: []string{
				"plan.started", "plan.completed", "plan.aborted", "supervisor.verdict",
			}, Limit: 20,
		})
		return map[string]any{"decisions": orgActivityDigest(events)}
	case "artifacts":
		events, _, _ := ws.ListOrgActivity(ctx, workspace.OrgActivityFilter{
			RunID: runID, Category: "artifact", Limit: 50,
		})
		artifacts := []map[string]any{}
		seen := map[string]bool{}
		for _, event := range events {
			artifactID := orgRefString(event.Refs, "artifact_id")
			if artifactID == "" || seen[artifactID] {
				continue
			}
			seen[artifactID] = true
			artifacts = append(artifacts, map[string]any{
				"artifact_id": artifactID, "title": orgSummaryAfterPrefix(event.Summary),
				"last_event": event.EventType,
			})
		}
		return map[string]any{"artifacts": artifacts}
	case "trace":
		return map[string]any{
			"journal_endpoint": "/v1/observations/events?run=" + runID,
			"note":             "Full trace stays behind the existing journal API and its own authorization.",
		}
	}
	return map[string]any{}
}

func orgActivityDigest(events []workspace.OrgActivityEvent) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		out = append(out, map[string]any{
			"type": event.EventType, "summary": event.Summary,
			"occurred_at": event.OccurredAt, "run_id": event.RunID,
		})
	}
	return out
}
