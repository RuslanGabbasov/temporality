package main

// Supervisor reconciliation pass (docs/failure-reconciliation.md, wave S1):
// a long run piles up dozens of uncertain operations, and a flat wall of
// per-op effect questions is unreviewable cognitive load — the operator will
// not check 40 side effects one by one. This pass hands the whole wall to the
// supervisor model and applies its ONE integrated conclusion per run:
//
//   - the model's per-operation effect verdicts are recorded as
//     supervisor-actor reconciliations (the same code path a human verdict
//     takes), so the wall closes without a human;
//   - the integrated conclusion (needs_attention + causes + remedies) is
//     journaled as supervisor.verdict kind=reconciliation and rendered in the
//     run overview as a single card instead of the raw wall;
//   - needs_attention=false means nothing requires action in an external
//     system — the pile closes quietly and nobody is bothered.
//
// Dedup: one reconciliation verdict per run, ever — the wall grows
// monotonically and the integrated conclusion covers the whole history of
// the run's uncertainty.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
)

// A run's wall qualifies for supervisor reconciliation when it holds at least
// this many unresolved operations — below that the per-op cards are still
// readable by a human.
const supervisorReconcileWallThreshold = 5

// Bounded per pass so a burst of walls cannot burn the model budget.
const supervisorReconcilePassCap = 2

func runSupervisorReconcilePass(ctx context.Context, log *slog.Logger, activities *agent.Activities, observationURL string, liveness agent.WorkflowLiveness, modelClient func(ctx context.Context) (*llm.Client, error)) {
	journalToken := os.Getenv("TEMPORALITY_API_TOKEN")
	// The operations endpoint is on THIS process; calling it through HTTP
	// would need a self-issued token — go straight to the activity, it is the
	// same code path the HTTP handler serves.
	ops, err := activities.UncertainOperations(ctx, "", liveness)
	if err != nil {
		log.Warn("supervisor reconcile: list uncertain operations", "error", err.Error())
		return
	}
	if len(ops) == 0 {
		return
	}
	// Group by run.
	walls := map[string][]agent.UncertainOperation{}
	runOrder := []string{}
	for _, op := range ops {
		if _, seen := walls[op.RunID]; !seen {
			runOrder = append(runOrder, op.RunID)
		}
		walls[op.RunID] = append(walls[op.RunID], op)
	}
	// Runs already carrying a reconciliation verdict are skipped.
	verdicts, err := fetchJournalTypedEvents(ctx, observationURL, journalToken, "supervisor.verdict", 100)
	if err != nil {
		log.Warn("supervisor reconcile: list verdicts", "error", err.Error())
		return
	}
	done := map[string]bool{}
	for _, event := range verdicts {
		if event.Data["kind"] == "reconciliation" {
			if runID, _ := event.Data["run_id"].(string); runID != "" {
				done[runID] = true
			}
		}
	}
	client, err := modelClient(ctx)
	if err != nil {
		return // judge off — stay quiet
	}
	analyzed := 0
	for _, runID := range runOrder {
		if analyzed >= supervisorReconcilePassCap {
			break
		}
		ops := walls[runID]
		if len(ops) < supervisorReconcileWallThreshold || done[runID] {
			continue
		}
		ids := make([]string, 0, len(ops))
		for _, op := range ops {
			ids = append(ids, op.OperationID)
		}
		verdict, err := agent.ReconcileFailedRunWall(ctx, client, agent.BuildWallDigest(runID, ops, ""), ids)
		if err != nil {
			log.Warn("supervisor reconcile: model call failed", "run", runID, "error", err.Error())
			continue
		}
		project := ""
		if len(ops) > 0 {
			project = ops[0].Project
		}
		// Record the per-op effects as supervisor reconciliations — the same
		// code path a human verdict takes, so the queue closes identically.
		applied := 0
		for _, v := range verdict.Verdicts {
			var matched *agent.UncertainOperation
			for i := range ops {
				if ops[i].OperationID == v.OperationID {
					matched = &ops[i]
					break
				}
			}
			if matched == nil {
				continue
			}
			note := strings.TrimSpace(fmt.Sprintf("supervisor: %s", v.Why))
			if _, rErr := activities.RecordReconciliation(ctx, agent.ReconcileRequest{
				Project:        matched.Project,
				RunID:          matched.RunID,
				OperationID:    matched.OperationID,
				Effect:         v.Effect,
				Note:           note,
				ActorID:        "supervisor",
				StartedEventID: matched.StartedEvent,
			}); rErr != nil {
				log.Warn("supervisor reconcile: record", "op", v.OperationID, "error", rErr.Error())
				continue
			}
			applied++
		}
		if err := activities.PublishSupervisorReconciliation(ctx, runID, project, verdict, len(ops), applied); err != nil {
			log.Warn("supervisor reconcile: publish", "run", runID, "error", err.Error())
			continue
		}
		analyzed++
		log.Info("supervisor reconciliation recorded", "run", runID, "operations", len(ops), "applied", applied, "needs_attention", verdict.NeedsAttention)
	}
}
