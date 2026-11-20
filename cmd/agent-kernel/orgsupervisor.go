package main

// Organizational Supervisor (docs/temporality-organizational-operations-spec.md
// §6, P2) + Awareness Delivery worker (§7-8, P3).
//
// The supervisor loop walks every org unit with agent_awareness_enabled on
// its own batch_interval_minutes schedule: it builds a compact batch (events
// past the node's window + active runs + recent delivery keys), asks the
// supervisor model to route it, validates every decision against the batch
// it built, persists decisions and creates awareness deliveries. An empty
// batch never reaches the model. A failed batch does not advance the node's
// window — the next tick reprocesses the same events idempotently (decision
// and delivery dedup keys make that safe).
//
// The delivery worker moves selected → queued → delivered through the
// existing steer mechanism (a kernel.user_note Temporal signal, delivered at
// the run's next turn boundary — the source tool result is never modified).
// A run that finished before delivery expires; transport errors retry with a
// bounded attempt budget.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

const (
	orgSupervisorTick          = time.Minute
	orgSupervisorLookback      = 24 * time.Hour
	orgSupervisorScopeLimit    = 200
	orgDeliveryWorkerTick      = 30 * time.Second
	orgDeliveryMaxAttempts     = 5
	orgDeliveryWindow          = time.Hour
	orgDeliveryRecipientWindow = 24 * time.Hour
	orgAwarenessActorID        = "org-awareness"
)

// orgNoteSignaler mirrors client.Client — production passes the Temporal
// client, tests record calls. TerminateWorkflow backs the executable steer
// (urgent_steer + action cancel_run/cancel_subtree).
type orgNoteSignaler interface {
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg any) error
	TerminateWorkflow(ctx context.Context, workflowID, runID string, reason string, args ...interface{}) error
}

// orgSupervisorDeps wires the loop; constructed in main.go where the store,
// outbox and model chain already exist.
type orgSupervisorDeps struct {
	ws          *workspace.Store
	log         *slog.Logger
	activities  *agent.Activities
	modelClient func(ctx context.Context) (*llm.Client, error)
}

// startOrgSupervisorLoop runs the per-node batch scheduler.
func startOrgSupervisorLoop(ctx context.Context, log *slog.Logger, ws *workspace.Store, activities *agent.Activities, observationURL string, modelClient func(ctx context.Context) (*llm.Client, error)) {
	deps := &orgSupervisorDeps{ws: ws, log: log, activities: activities, modelClient: modelClient}
	go func() {
		// First pass after boot: let the projection fold something first.
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Minute):
		}
		ticker := time.NewTicker(orgSupervisorTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runPassGuarded(log, "org-supervisor", ctx, func(pctx context.Context) {
					runOrgSupervisorPass(pctx, deps)
				})
			}
		}
	}()
}

// runOrgSupervisorPass processes every due node once.
func runOrgSupervisorPass(ctx context.Context, deps *orgSupervisorDeps) {
	ctx = withSupervisorPurpose(ctx, "org")
	units, err := deps.ws.ListOrgUnits(ctx)
	if err != nil {
		deps.log.Warn("org supervisor: units", "error", err)
		return
	}
	rows, err := deps.ws.AllOrgActivitySettingRows(ctx)
	if err != nil {
		deps.log.Warn("org supervisor: settings", "error", err)
		return
	}
	settingsByUnit := workspace.ResolveOrgActivitySettingsForUnits(units, rows)
	// Manual batches (process_activity_batch API) go first, whatever their
	// schedule says.
	manualBatchID, manualNode, err := deps.ws.DueOrgActivityBatchNode(ctx)
	if err != nil {
		deps.log.Warn("org supervisor: manual batch lookup", "error", err)
	}
	for _, unit := range units {
		result, ok := settingsByUnit[unit.ID]
		if !ok || !result.Settings.AgentAwarenessEnabled {
			continue
		}
		settings := result.Settings
		isManual := manualNode == unit.ID
		if !isManual {
			interval := time.Duration(settings.BatchIntervalMinutes) * time.Minute
			if interval <= 0 {
				interval = 10 * time.Minute
			}
			if !orgNodeDue(ctx, deps, unit.ID, interval) {
				continue
			}
		}
		trigger := "scheduled"
		if isManual {
			trigger = "manual"
		}
		runOrgNodeBatch(ctx, deps, unit.ID, settings, trigger, manualBatchID)
		if ctx.Err() != nil {
			return
		}
	}
}

// orgNodeDue reports whether the node's last completed batch is older than
// its interval — the batch state row's updated_at is the completion stamp.
func orgNodeDue(ctx context.Context, deps *orgSupervisorDeps, nodeID string, interval time.Duration) bool {
	completedAt, err := deps.ws.OrgNodeBatchCompletedAt(ctx, nodeID)
	if err != nil {
		return true // unknown state: process rather than starve
	}
	return completedAt.IsZero() || time.Since(completedAt) >= interval
}

// orgScopeUnits resolves the §5 awareness_scope to the unit id set whose
// events and runs the node's supervisor may see.
func orgScopeUnits(units []workspace.OrgUnit, nodeID string, settings workspace.OrgActivitySettings) []string {
	scope := []string{nodeID}
	switch settings.AwarenessScope {
	case workspace.OrgAwarenessScopeSubtree:
		selfPath := ""
		for _, unit := range units {
			if unit.ID == nodeID {
				selfPath = unit.SelfPath()
				break
			}
		}
		if selfPath != "" {
			for _, unit := range units {
				if unit.ID == nodeID || strings.HasPrefix(unit.Path, selfPath+".") || unit.Path == selfPath {
					scope = append(scope, unit.ID)
				}
			}
		}
	case workspace.OrgAwarenessScopeCustom:
		scope = append(scope, settings.AwarenessScopeUnits...)
	}
	if len(scope) > orgSupervisorScopeLimit {
		scope = scope[:orgSupervisorScopeLimit]
	}
	return scope
}

// runOrgNodeBatch builds, analyzes and closes one node batch (§6.2-6.4).
func runOrgNodeBatch(ctx context.Context, deps *orgSupervisorDeps, nodeID string, settings workspace.OrgActivitySettings, trigger, manualBatchID string) {
	log := deps.log
	units, err := deps.ws.ListOrgUnits(ctx)
	if err != nil {
		log.Warn("org supervisor: units", "node", nodeID, "error", err)
		return
	}
	scope := orgScopeUnits(units, nodeID, settings)

	now := time.Now().UTC()
	var windowFrom time.Time
	previousTo, _, err := deps.ws.OrgNodeBatchWindow(ctx, nodeID)
	if err != nil {
		log.Warn("org supervisor: window", "node", nodeID, "error", err)
		return
	}
	if previousTo.IsZero() {
		windowFrom = now.Add(-orgSupervisorLookback)
	} else {
		windowFrom = previousTo
	}
	windowTo := now

	batchID := manualBatchID
	if batchID == "" {
		// Scheduled passes mint their own batch row; a manual batch row was
		// already created by the POST /v1/org/activity-batches endpoint.
		batchID = fmt.Sprintf("org-batch-%d", now.UnixNano())
		batch := workspace.OrgActivityBatch{
			ID: batchID, OrgNodeID: nodeID, Status: workspace.OrgBatchPending,
			Trigger: trigger, WindowFrom: &windowFrom, WindowTo: &windowTo,
		}
		if err := deps.ws.CreateOrgActivityBatch(ctx, batch); err != nil {
			log.Warn("org supervisor: create batch", "node", nodeID, "error", err)
			return
		}
	}
	if err := deps.ws.StartOrgActivityBatch(ctx, batchID); err != nil {
		log.Warn("org supervisor: start batch", "node", nodeID, "error", err)
		return
	}

	events, _, err := deps.ws.ListOrgActivity(ctx, workspace.OrgActivityFilter{
		OrgNodeIDs: scope, From: &windowFrom, To: &windowTo,
		Limit: settings.MaxEventsPerBatch,
	})
	if err != nil {
		deps.failBatch(ctx, batchID, nodeID, "load events", err)
		return
	}
	activeRuns, _, err := deps.ws.ListOrgActiveRuns(ctx, workspace.OrgActiveRunFilter{
		OrgNodeIDs: scope, Status: "running", Limit: 500,
	})
	if err != nil {
		deps.failBatch(ctx, batchID, nodeID, "load active runs", err)
		return
	}
	// Empty batch: no LLM call (spec §6.2, §21). The window still advances.
	if len(events) == 0 {
		if err := deps.ws.CompleteOrgActivityBatch(ctx, workspace.OrgActivityBatch{
			ID: batchID, OrgNodeID: nodeID, WindowFrom: &windowFrom, WindowTo: &windowTo,
			Empty: true,
		}); err != nil {
			log.Warn("org supervisor: complete empty batch", "node", nodeID, "error", err)
		}
		return
	}
	recentCutoff := now.Add(-orgDeliveryRecipientWindow)
	recent, _, err := deps.ws.ListOrgAwarenessDeliveries(ctx, workspace.OrgDeliveryFilter{
		OrgNodeIDs: scope, From: &recentCutoff, Limit: 500,
	})
	if err != nil {
		deps.failBatch(ctx, batchID, nodeID, "load recent deliveries", err)
		return
	}

	if len(activeRuns) == 0 {
		// No live recipients at all — nothing to route; keep the decisions
		// empty without a model call.
		if err := deps.ws.CompleteOrgActivityBatch(ctx, workspace.OrgActivityBatch{
			ID: batchID, OrgNodeID: nodeID, WindowFrom: &windowFrom, WindowTo: &windowTo,
			EventCount: len(events), Empty: true,
		}); err != nil {
			log.Warn("org supervisor: complete no-target batch", "node", nodeID, "error", err)
		}
		return
	}

	model, err := deps.modelClient(ctx)
	if err != nil {
		// Judge model off — stay quiet, retry next tick without advancing
		// the window (the batch stays failed).
		deps.failBatch(ctx, batchID, nodeID, "model unavailable", err)
		return
	}
	input := buildOrgBatchInput(nodeID, now, events, activeRuns, recent, settings)
	verdict, err := agent.RunOrgAwarenessBatch(ctx, model, input)
	if err != nil {
		deps.failBatch(ctx, batchID, nodeID, "model call", err)
		return
	}

	decisions := validateOrgDecisions(deps.log, verdict, batchID, nodeID, model.Model(), events, activeRuns, recent, settings, now)
	// Source summaries ride onto the deliveries so the steer message and the
	// flow view carry the fact without re-reading the projection.
	summaries := make(map[string]string, len(events))
	for _, e := range events {
		summaries[e.SourceID+"/"+e.EventID] = e.Summary
	}
	created := 0
	for _, decision := range decisions {
		if err := deps.ws.CreateOrgAwarenessDecision(ctx, decision); err != nil {
			if errors.Is(err, workspace.ErrConflict) {
				continue // the dedup key already produced a decision
			}
			log.Warn("org supervisor: store decision", "node", nodeID, "error", err)
			deps.failBatch(ctx, batchID, nodeID, "store decision", err)
			return
		}
		if decision.Decision == workspace.OrgDecisionIgnore {
			continue
		}
		delivery, ok, err := deps.createOrgDelivery(ctx, decision, activeRuns, settings, now, summaries)
		if err != nil {
			if errors.Is(err, workspace.ErrConflict) {
				continue // duplicate delivery — suppression worked
			}
			log.Warn("org supervisor: create delivery", "node", nodeID, "error", err)
			continue
		}
		if ok {
			created++
		}
		_ = delivery
	}

	if err := deps.ws.CompleteOrgActivityBatch(ctx, workspace.OrgActivityBatch{
		ID: batchID, OrgNodeID: nodeID, WindowFrom: &windowFrom, WindowTo: &windowTo,
		EventCount: len(events), DecisionCount: len(decisions), DeliveryCount: created,
	}); err != nil {
		log.Warn("org supervisor: complete batch", "node", nodeID, "error", err)
		return
	}
	deps.publishBatchEvent(ctx, batchID, nodeID, len(events), len(decisions), created, model.Model())
	log.Info("org supervisor batch completed",
		"node", nodeID, "batch", batchID, "events", len(events),
		"decisions", len(decisions), "deliveries", created)
}

func (deps *orgSupervisorDeps) failBatch(ctx context.Context, batchID, nodeID, stage string, err error) {
	deps.log.Warn("org supervisor batch failed", "node", nodeID, "batch", batchID, "stage", stage, "error", err)
	if failErr := deps.ws.FailOrgActivityBatch(ctx, batchID, stage+": "+err.Error()); failErr != nil {
		deps.log.Warn("org supervisor: fail batch", "node", nodeID, "error", failErr)
	}
}

// buildOrgBatchInput maps the projection rows onto the compact model input.
func buildOrgBatchInput(nodeID string, now time.Time, events []workspace.OrgActivityEvent, activeRuns []workspace.OrgActiveRun, recent []workspace.OrgAwarenessDelivery, settings workspace.OrgActivitySettings) agent.OrgBatchInput {
	input := agent.OrgBatchInput{
		NodeID: nodeID, Now: now.Format(time.RFC3339),
		MaxRecipientsPerEvent: settings.MaxRecipientsPerEvent,
		MaxDeliveriesPerRun:   settings.MaxDeliveriesPerRunPerWindow,
	}
	for _, e := range events {
		input.Events = append(input.Events, agent.OrgBatchEvent{
			EventID: e.SourceID + "/" + e.EventID, Type: e.EventType, Summary: e.Summary,
			RunID: e.RunID, ProjectID: e.ProjectID, TaskID: e.TaskID, ActorID: e.ActorID,
			OccurredAt: e.OccurredAt.Format(time.RFC3339),
		})
	}
	for _, r := range activeRuns {
		input.ActiveRuns = append(input.ActiveRuns, agent.OrgBatchRun{
			RunID: r.RunID, ProjectID: r.ProjectID, Title: r.Title, TaskID: r.TaskID,
			ActorID: r.ActorID, Status: r.Status, Urgent: r.Urgent,
			LastEventAt: orgTimePtr(r.LastEventAt, now),
		})
	}
	for _, d := range recent {
		input.RecentDeliveries = append(input.RecentDeliveries, agent.OrgBatchDelivery{
			SourceEventID: d.SourceEventID, TargetRunID: d.TargetRunID,
			Decision: d.Decision, DeduplicationKey: d.DeduplicationKey,
			CreatedAt: d.CreatedAt.Format(time.RFC3339),
		})
	}
	return input
}

func orgTimePtr(at *time.Time, fallback time.Time) string {
	if at == nil || at.IsZero() {
		return fallback.Format(time.RFC3339)
	}
	return at.UTC().Format(time.RFC3339)
}

// validateOrgDecisions enforces the §6 obligations: closed decision enum,
// targets must be RUNNING runs of this batch, source events must exist in
// the batch, message/reason caps, dedup key defaults, recipient limits. The
// model suggests; this function decides what gets stored.
func validateOrgDecisions(
	log *slog.Logger,
	verdict agent.OrgBatchVerdict,
	batchID, nodeID, modelName string,
	events []workspace.OrgActivityEvent,
	activeRuns []workspace.OrgActiveRun,
	recent []workspace.OrgAwarenessDelivery,
	settings workspace.OrgActivitySettings,
	now time.Time,
) []workspace.OrgAwarenessDecision {
	eventByKey := make(map[string]workspace.OrgActivityEvent, len(events))
	for _, e := range events {
		eventByKey[e.SourceID+"/"+e.EventID] = e
		eventByKey[e.EventID] = e
	}
	runningByRun := make(map[string]workspace.OrgActiveRun, len(activeRuns))
	for _, r := range activeRuns {
		runningByRun[r.RunID] = r
	}
	recentByKey := make(map[string]bool, len(recent))
	for _, d := range recent {
		if d.DeduplicationKey != "" {
			recentByKey[d.DeduplicationKey+"|"+d.TargetRunID] = true
		}
	}
	out := []workspace.OrgAwarenessDecision{}
	for _, raw := range verdict.Decisions {
		if !workspace.ValidOrgDecisions[raw.Decision] {
			log.Warn("org supervisor: drop invalid decision kind", "decision", raw.Decision)
			continue
		}
		if raw.TargetRunID == "" && raw.Decision != workspace.OrgDecisionIgnore {
			continue
		}
		if raw.Decision != workspace.OrgDecisionIgnore {
			if _, ok := runningByRun[raw.TargetRunID]; !ok {
				// Not an active run of this scope: a completed/failed/cancelled
				// or foreign run — never message it (spec §6).
				continue
			}
			source := eventByKey[raw.SourceEventID]
			if raw.SourceEventID != "" && source.EventID == "" {
				continue // invented source: drop
			}
			relevance := strings.ToLower(strings.TrimSpace(raw.Relevance))
			if relevance != "low" && relevance != "medium" && relevance != "high" {
				relevance = "medium"
			}
			urgency := strings.ToLower(strings.TrimSpace(raw.Urgency))
			if urgency != "high" && urgency != "normal" {
				urgency = "normal"
			}
			dedupKey := strings.TrimSpace(raw.DeduplicationKey)
			if dedupKey == "" {
				dedupKey = raw.SourceEventID + "|" + raw.TargetRunID
			}
			// update_awareness intentionally re-sends on substantial change:
			// the revision suffix keeps its key off the delivered set.
			if raw.Decision != workspace.OrgDecisionUpdateAwareness && recentByKey[dedupKey+"|"+raw.TargetRunID] {
				continue // already delivered this exact fact to this run
			}
			var lookup []byte
			if raw.SuggestedLookup != nil {
				encoded, err := json.Marshal(raw.SuggestedLookup)
				if err == nil {
					lookup = encoded
				}
			}
			sourceRunID := source.RunID
			if raw.SuggestedLookup != nil && raw.SuggestedLookup.RunID != "" {
				sourceRunID = raw.SuggestedLookup.RunID
			}
			out = append(out, workspace.OrgAwarenessDecision{
				ID: fmt.Sprintf("orgdec-%d-%s", now.UnixNano(), raw.TargetRunID),
				BatchID: batchID, OrgNodeID: nodeID,
				SourceEventID: source.SourceID + "/" + source.EventID,
				SourceRunID:   sourceRunID,
				TargetRunID:   raw.TargetRunID,
				Decision:      raw.Decision,
				Relevance:     relevance,
				Reason:        raw.Reason,
				Message:       raw.Message,
				SuggestedLookup: lookup,
				Urgency:       urgency,
				DeduplicationKey: dedupKey,
				// The executable steer survives only on urgent_steer: any other
				// decision kind is informational by contract.
				Action: func() string {
					if raw.Decision == workspace.OrgDecisionUrgentSteer {
						return raw.Action
					}
					return ""
				}(),
				Model:         modelName,
				CreatedAt:     now,
			})
		} else {
			out = append(out, workspace.OrgAwarenessDecision{
				ID: fmt.Sprintf("orgdec-%d-ign-%d", now.UnixNano(), len(out)),
				BatchID: batchID, OrgNodeID: nodeID,
				Decision: workspace.OrgDecisionIgnore,
				Relevance: "low", Urgency: "normal",
				Model: modelName, CreatedAt: now,
			})
		}
	}
	return out
}

// createOrgDelivery enforces the §5/§7.2 recipient limits and inserts the
// lifecycle row (status selected; the delivery worker queues it).
func (deps *orgSupervisorDeps) createOrgDelivery(ctx context.Context, decision workspace.OrgAwarenessDecision, activeRuns []workspace.OrgActiveRun, settings workspace.OrgActivitySettings, now time.Time, summaries map[string]string) (workspace.OrgAwarenessDelivery, bool, error) {
	project := ""
	actor := ""
	for _, r := range activeRuns {
		if r.RunID == decision.TargetRunID {
			project, actor = r.ProjectID, r.ActorID
			break
		}
	}
	// max_recipients_per_event (§5): one event may not storm the unit.
	if decision.SourceEventID != "" {
		count, err := deps.ws.CountOrgDeliveriesForEventSince(ctx, decision.SourceEventID, now.Add(-orgDeliveryRecipientWindow))
		if err == nil && count >= settings.MaxRecipientsPerEvent {
			return workspace.OrgAwarenessDelivery{}, false, nil
		}
	}
	// max_deliveries_per_run_per_window: a run cannot be flooded (§7.2).
	count, err := deps.ws.CountOrgDeliveriesForRunSince(ctx, decision.TargetRunID, now.Add(-orgDeliveryWindow))
	if err == nil && count >= settings.MaxDeliveriesPerRunPerWindow {
		return workspace.OrgAwarenessDelivery{}, false, nil
	}
	// The dedup last resort: the unique index turns a race into ErrConflict.
	exists, err := deps.ws.OrgDeliveryExists(ctx, decision.DeduplicationKey, decision.TargetRunID)
	if err == nil && exists {
		return workspace.OrgAwarenessDelivery{}, false, nil
	}
	urgency := decision.Urgency
	if urgency == "high" && !settings.UrgentProcessingEnabled {
		urgency = "normal" // out-of-band priority is opt-in (§5)
	}
	sourceSummary := summaries[decision.SourceEventID]
	sourceEvent := decision.SourceEventID
	modelMessage := decision.Message
	delivery := workspace.OrgAwarenessDelivery{
		ID: fmt.Sprintf("orgdlv-%d-%s", now.UnixNano(), decision.TargetRunID),
		DecisionID: decision.ID, BatchID: decision.BatchID, OrgNodeID: decision.OrgNodeID,
		ProjectID: project, SourceEventID: sourceEvent, SourceRunID: decision.SourceRunID,
		SourceSummary: sourceSummary, TargetRunID: decision.TargetRunID, TargetActorID: actor,
		Decision: decision.Decision, Relevance: decision.Relevance, Urgency: urgency,
		Reason: decision.Reason, Message: modelMessage,
		DetailLevel: orgSuggestedDetailLevel(decision.SuggestedLookup),
		Action: decision.Action,
		Status:      workspace.OrgDeliverySelected,
		DeduplicationKey: decision.DeduplicationKey,
		FollowUpRefs: json.RawMessage("[]"), RelatedActionRefs: json.RawMessage("[]"),
		CreatedAt: now,
	}
	if err := deps.ws.CreateOrgAwarenessDelivery(ctx, delivery); err != nil {
		return workspace.OrgAwarenessDelivery{}, false, err
	}
	return delivery, true, nil
}

func orgSuggestedDetailLevel(lookup []byte) string {
	if len(lookup) == 0 {
		return ""
	}
	var parsed struct {
		DetailLevel string `json:"detail_level"`
	}
	if err := json.Unmarshal(lookup, &parsed); err != nil {
		return ""
	}
	return parsed.DetailLevel
}

// publishBatchEvent journals the batch summary (§18 observability: batch
// count/duration/decisions, auditable without chain-of-thought).
func (deps *orgSupervisorDeps) publishBatchEvent(ctx context.Context, batchID, nodeID string, events, decisions, deliveries int, modelName string) {
	if deps.activities == nil {
		return
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("org-supervisor-batch/%s/%d", batchID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source: observation.Source{ID: deps.activities.SourceID, Integration: "temporality-agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: "org-supervisor", Type: "system"}},
		Type:       "org.supervisor.batch",
		Data: map[string]any{
			"batch_id": batchID, "org_node_id": nodeID, "events": events,
			"decisions": decisions, "deliveries": deliveries, "model": modelName,
		},
	}
	if err := deps.activities.RecordEvent(ctx, event); err != nil {
		deps.log.Warn("org supervisor: publish batch event", "error", err)
	}
}

// --- P3: the delivery worker ---

const orgAwarenessSteerPrefix = "Organizational awareness (from the department supervisor — background context, not a user request and not a new task).\n\n"

// orgAwarenessSteerMessage frames the §7 notification: source, status,
// reason, the details option and the consumption marker.
func orgAwarenessSteerMessage(d workspace.OrgAwarenessDelivery) string {
	var b strings.Builder
	b.WriteString(orgAwarenessSteerPrefix)
	if d.SourceRunID != "" {
		b.WriteString("Related work — run " + d.SourceRunID)
		if d.SourceSummary != "" {
			b.WriteString(": " + d.SourceSummary)
		}
		b.WriteString("\n")
	}
	if d.Reason != "" {
		b.WriteString("Why this matters: " + d.Reason + "\n")
	}
	if d.Message != "" && d.Message != d.SourceSummary {
		b.WriteString(d.Message + "\n")
	}
	if d.SourceRunID != "" && d.DetailLevel != "" {
		b.WriteString(fmt.Sprintf("You may request details with request_related_run_details(run_id=%q, detail_level=%q). Allowed levels: summary, findings, decisions, artifacts.\n", d.SourceRunID, d.DetailLevel))
	} else if d.SourceRunID != "" {
		b.WriteString(fmt.Sprintf("You may request details with request_related_run_details(run_id=%q, detail_level=\"summary\"). Allowed levels: summary, findings, decisions, artifacts.\n", d.SourceRunID))
	}
	b.WriteString("[org-awareness:" + d.ID + "]")
	return b.String()
}

// startOrgAwarenessDeliveryLoop moves selected deliveries through the steer
// mechanism every 30 seconds. The cancel closure executes urgent_steer
// cancel actions (terminate the workflow, journal the terminal event, settle
// the workspace row) — wired in orgboards.go where the journal credentials
// live.
func startOrgAwarenessDeliveryLoop(ctx context.Context, log *slog.Logger, ws *workspace.Store, signaler orgNoteSignaler, canceler orgSteerCanceler) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
		ticker := time.NewTicker(orgDeliveryWorkerTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runPassGuarded(log, "org-delivery", ctx, func(pctx context.Context) {
					runOrgDeliveryPass(pctx, log, ws, signaler, canceler)
				})
			}
		}
	}()
}

// orgSteerCanceler executes the cancellation: terminate the workflow, close
// the workspace row and journal the derived terminal event (the workflow's
// own deferred run.cancelled never fires on terminate — the same heal
// contract the operator stop route follows). Implemented in orgboards.go
// where the journal credentials and the temporal client live.
type orgSteerCanceler interface {
	Cancel(ctx context.Context, project, runID, reason string) error
}

// runOrgDeliveryPass queues selected rows and attempts the queued ones.
func runOrgDeliveryPass(ctx context.Context, log *slog.Logger, ws *workspace.Store, signaler orgNoteSignaler, canceler orgSteerCanceler) {
	if _, err := ws.QueueOrgAwarenessDeliveries(ctx, 50); err != nil {
		log.Warn("org awareness delivery: queue", "error", err)
	}
	pending, err := ws.PendingOrgAwarenessDeliveries(ctx, 50)
	if err != nil {
		log.Warn("org awareness delivery: pending", "error", err)
		return
	}
	expiredRuns := map[string]bool{}
	for _, delivery := range pending {
		if ctx.Err() != nil {
			return
		}
		if expiredRuns[delivery.TargetRunID] {
			continue // already expired this pass
		}
		if delivery.Action != "" && canceler != nil {
			if err := executeOrgSteerCancel(ctx, log, ws, canceler, delivery); err != nil {
				log.Warn("org awareness delivery: steer cancel", "delivery", delivery.ID, "error", err)
			}
			continue
		}
		expired, err := deliverOrgAwarenessRow(ctx, log, ws, signaler, delivery)
		if err != nil {
			log.Warn("org awareness delivery: attempt", "delivery", delivery.ID, "error", err)
		}
		if expired {
			expiredRuns[delivery.TargetRunID] = true
		}
	}
}

// deliverOrgAwarenessRow performs one delivery attempt: liveness pre-check,
// steer signal, terminal state. Returns whether the TARGET RUN was found
// finished (all its queued deliveries expire together).
func deliverOrgAwarenessRow(ctx context.Context, log *slog.Logger, ws *workspace.Store, signaler orgNoteSignaler, delivery workspace.OrgAwarenessDelivery) (expired bool, err error) {
	// Finished before delivery → expire (spec §22 scenario 5).
	statuses, err := ws.RunTerminalStatuses(ctx, []string{delivery.TargetRunID})
	if err != nil {
		return false, err
	}
	if _, terminal := statuses[delivery.TargetRunID]; terminal {
		if _, err := ws.ExpireOrgAwarenessDeliveriesForRun(ctx, delivery.TargetRunID, "target run finished before delivery"); err != nil {
			return true, err
		}
		return true, nil
	}
	project := delivery.ProjectID
	if project == "" {
		project, err = ws.RunProjectID(ctx, delivery.TargetRunID)
		if err != nil {
			return false, err
		}
	}
	sourceID := ""
	if row, err := ws.GetOrgActiveRun(ctx, project, delivery.TargetRunID); err == nil {
		sourceID = row.SourceID
	}
	if sourceID == "" {
		sourceID, err = ws.RunSourceID(ctx, project, delivery.TargetRunID)
		if err != nil {
			return false, fmt.Errorf("resolve run source: %w", err)
		}
	}
	workflowID := agent.WorkflowID(sourceID, project, delivery.TargetRunID)
	message := orgAwarenessSteerMessage(delivery)
	note := agent.UserNote{Text: message, ActorID: orgAwarenessActorID}
	if err := signaler.SignalWorkflow(ctx, workflowID, "", agent.UserNoteSignal, note); err != nil {
		if isWorkflowGone(err) {
			// The run finished between the batch and this attempt: expire
			// everything still queued for it (precedent: deliverUserNote).
			if _, expireErr := ws.ExpireOrgAwarenessDeliveriesForRun(ctx, delivery.TargetRunID, "target run finished before delivery"); expireErr != nil {
				return true, expireErr
			}
			return true, nil
		}
		retry, retryErr := ws.RetryOrgAwarenessDelivery(ctx, delivery.ID, orgDeliveryMaxAttempts, err)
		if retryErr != nil {
			return false, retryErr
		}
		if !retry {
			log.Warn("org awareness delivery: exhausted attempts", "delivery", delivery.ID)
		}
		return false, nil
	}
	messageRef := "user_note/" + workflowID
		return false, ws.MarkOrgAwarenessDeliveryDelivered(ctx, delivery.ID, messageRef)
	}

	// executeOrgSteerCancel performs the executable urgent_steer: terminate the
	// target run (cancel_run) or the target plus its live subtree (cancel_subtree),
	// then close the delivery as delivered — the action IS the message. Every
	// child termination is audited by the canceler with the supervisor's reason.
	func executeOrgSteerCancel(ctx context.Context, log *slog.Logger, ws *workspace.Store, canceler orgSteerCanceler, delivery workspace.OrgAwarenessDelivery) error {
		project := delivery.ProjectID
		if project == "" {
			var err error
			project, err = ws.RunProjectID(ctx, delivery.TargetRunID)
			if err != nil {
				return err
			}
		}
		reason := "cancelled by the organizational supervisor (urgent_steer)"
		if delivery.Reason != "" {
			reason += ": " + delivery.Reason
		}
		targets := []string{delivery.TargetRunID}
		if delivery.Action == "cancel_subtree" {
			runs, _, listErr := ws.ListOrgActiveRuns(ctx, workspace.OrgActiveRunFilter{ProjectID: project, Limit: 500})
			if listErr != nil {
				return listErr
			}
			prefix := delivery.TargetRunID + "/"
			for _, run := range runs {
				if strings.HasPrefix(run.RunID, prefix) && run.Status == "running" {
					targets = append(targets, run.RunID)
				}
			}
		}
		var firstErr error
		for _, runID := range targets {
			if err := canceler.Cancel(ctx, project, runID, reason); err != nil {
				log.Warn("org steer cancel: terminate", "run", runID, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		if firstErr != nil {
			return firstErr
		}
		log.Info("org steer cancel executed", "target", delivery.TargetRunID, "action", delivery.Action, "runs", len(targets))
		return ws.MarkOrgAwarenessDeliveryDelivered(ctx, delivery.ID, "steer-cancel/"+delivery.TargetRunID)
	}
