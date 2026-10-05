package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/observation"
)

type stubLiveness struct {
	running map[string]bool
}

func (s stubLiveness) IsWorkflowRunning(_ context.Context, _, _, runID string) (bool, error) {
	return s.running[runID], nil
}

func journalStub(t *testing.T, events ...observation.Event) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/observations/events":
			if r.Method == http.MethodPost {
				var payload struct {
					Events []observation.Event `json:"events"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				require.NotEmpty(t, payload.Events)
				results := make([]map[string]string, 0, len(payload.Events))
				for _, event := range payload.Events {
					require.NoError(t, event.Validate())
					results = append(results, map[string]string{"event_id": event.EventID, "status": "accepted"})
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
				return
			}
			var matching []observation.Event
			eventType := r.URL.Query().Get("type")
			run := r.URL.Query().Get("run")
			for _, event := range events {
				if eventType != "" && event.Type != eventType {
					continue
				}
				if run != "" && event.Context.Run != run {
					continue
				}
				matching = append(matching, event)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"events": matching, "count": len(matching)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func toolEvent(project, run, operationID, eventType string, occurredAt time.Time) observation.Event {
	data := map[string]any{"operation_id": operationID, "tool": "run_command", "arguments_hash": "sha256:abc"}
	if eventType == "tool.started" {
		data["server"] = ""
	}
	if eventType == "tool.failed" {
		data["error_type"] = "activity_failed"
		data["effect"] = "uncertain"
	}
	if eventType == "operation.reconciled" {
		data["effect"] = "unknown"
		data["reconciled_by"] = "operator-1"
		data["derived"] = true
	}
	return observation.Event{
		Schema: "temporality.event/1", EventID: eventType + "/" + operationID, OccurredAt: occurredAt,
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel"},
		Context: observation.Context{Project: project, Run: run},
		Type:    eventType, Data: data,
	}
}

func TestUncertainOperationsCrashWindow(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	recent := time.Now().Add(-time.Minute)
	rejected := toolEvent("repo", "run-6", "op-rejected", "tool.failed", old)
	rejected.Data["effect"] = "none"
	rejected.Data["error_type"] = "argument_rejected"
	server := journalStub(t,
		toolEvent("repo", "run-1", "op-done", "tool.started", old),
		toolEvent("repo", "run-1", "op-done", "tool.completed", old),
		toolEvent("repo", "run-2", "op-crashed", "tool.started", old),
		toolEvent("repo", "run-3", "op-live", "tool.started", recent),
		toolEvent("repo", "run-4", "op-stale", "tool.started", old),
		toolEvent("repo", "run-5", "op-failed", "tool.started", old),
		toolEvent("repo", "run-5", "op-failed", "tool.failed", old),
		toolEvent("repo", "run-6", "op-rejected", "tool.started", old),
		rejected,
		toolEvent("repo", "run-7", "op-reconciled", "tool.started", old),
		toolEvent("repo", "run-7", "op-reconciled", "operation.reconciled", old),
		toolEvent("repo", "run-8", "op-failed-recon", "tool.started", old),
		toolEvent("repo", "run-8", "op-failed-recon", "tool.failed", old),
		toolEvent("repo", "run-8", "op-failed-recon", "operation.reconciled", old),
	)
	// op-rejected was refused before execution (effect=none) — settled.
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL, APIToken: "writer"}
	liveness := stubLiveness{running: map[string]bool{"run-3": true, "run-4": true}}

	operations, err := activities.UncertainOperations(context.Background(), "repo", liveness)
	require.NoError(t, err)

	ids := map[string]UncertainOperation{}
	for _, operation := range operations {
		ids[operation.OperationID] = operation
	}
	require.NotContains(t, ids, "op-done", "operations with terminal events are settled")
	require.Contains(t, ids, "op-crashed", "missing terminal event after workflow end is the crash window")
	require.Equal(t, "crash_window", ids["op-crashed"].Reason)
	require.Equal(t, UncertainOperation{State: "uncertain", Reason: "crash_window"}, UncertainOperation{State: ids["op-crashed"].State, Reason: ids["op-crashed"].Reason})
	require.Contains(t, ids, "op-failed", "an activity failure at the execution boundary leaves the effect uncertain")
	require.Equal(t, "failed_uncertain", ids["op-failed"].Reason)
	require.NotContains(t, ids, "op-rejected", "pre-execution rejection (effect=none) is settled")
	require.NotContains(t, ids, "op-reconciled", "a recorded reconciliation verdict settles the operation")
	require.NotContains(t, ids, "op-failed-recon", "an operator verdict settles even an uncertain failure")
	require.NotContains(t, ids, "op-live", "fresh tool.started inside a running workflow is still in flight")
	require.Contains(t, ids, "op-stale", "a tool.started older than the activity budget inside a running workflow went uncertain")
	require.Equal(t, "stale_in_flight", ids["op-stale"].Reason)
	require.Equal(t, "run-2", ids["op-crashed"].RunID)
	require.Equal(t, "tool.started/op-crashed", ids["op-crashed"].StartedEvent)
	require.Equal(t, "kernel", ids["op-crashed"].SourceID)
}

func TestRecordReconciliationWritesDerivedEvent(t *testing.T) {
	server := journalStub(t)
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL, APIToken: "writer", SourceID: "kernel"}

	event, err := activities.RecordReconciliation(context.Background(), ReconcileRequest{
		Project: "repo", RunID: "run-2", OperationID: "op-crashed",
		Effect: "occurred", Note: "issue #7 exists downstream", ActorID: "operator-1",
		StartedEventID: "tool.started/op-crashed",
		Evidence:       []observation.Evidence{{Ref: "issues/7", Type: "external"}},
	})
	require.NoError(t, err)
	require.Equal(t, "operation.reconciled", event.Type)
	require.Equal(t, "occurred", event.Data["effect"])
	require.Equal(t, "op-crashed", event.Data["operation_id"])
	require.Equal(t, true, event.Data["derived"])
	require.Equal(t, "tool.started/op-crashed", event.Context.ParentEventID)
	require.Equal(t, "operator-1", event.Context.Actor.ID)
	require.NotEmpty(t, event.EventID)

	_, err = activities.RecordReconciliation(context.Background(), ReconcileRequest{
		Project: "repo", RunID: "run-2", OperationID: "op-crashed", Effect: "maybe", ActorID: "operator-1",
	})
	require.Error(t, err, "effect must be a closed vocabulary")

	_, err = activities.RecordReconciliation(context.Background(), ReconcileRequest{
		Project: "repo", RunID: "run-2", OperationID: "", Effect: "none", ActorID: "operator-1",
	})
	require.Error(t, err, "operation identity is required")
}

func TestClassifyChildOperations(t *testing.T) {
	stamp := time.Now().UTC()
	newEvent := func(eventType, operationID string) observation.Event {
		event := toolEvent("repo", "run-child", operationID, eventType, stamp)
		return event
	}
	// op-done: started + completed → settled.
	// op-failed-uncertain: started + failed(effect=uncertain) → unresolved.
	// op-crashed: started, no terminal → unresolved.
	// op-blocked: started + blocked → settled.
	// op-reconciled: started + failed(uncertain) + reconciled → settled by verdict.
	events := []observation.Event{
		newEvent("tool.started", "op-done"),
		newEvent("tool.completed", "op-done"),
		newEvent("tool.started", "op-failed-uncertain"),
		newEvent("tool.failed", "op-failed-uncertain"),
		newEvent("tool.started", "op-crashed"),
		newEvent("tool.started", "op-blocked"),
		newEvent("tool.blocked", "op-blocked"),
		newEvent("tool.started", "op-reconciled"),
		newEvent("tool.failed", "op-reconciled"),
		newEvent("operation.reconciled", "op-reconciled"),
	}
	summary := classifyChildOperations(events)
	require.Equal(t, ChildRunSummary{Total: 5, Unresolved: 2}, summary)
	require.Equal(t, ChildRunSummary{}, classifyChildOperations(nil), "a child that failed before any tool call has no operations")
}

func TestSummarizeChildRun(t *testing.T) {
	stamp := time.Now().UTC()
	server := journalStub(t,
		toolEvent("repo", "run-child", "op-1", "tool.started", stamp),
		toolEvent("repo", "run-child", "op-1", "tool.completed", stamp),
		toolEvent("repo", "run-child", "op-2", "tool.started", stamp),
		toolEvent("repo", "other-run", "op-3", "tool.started", stamp),
	)
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL, APIToken: "writer"}

	summary, err := activities.SummarizeChildRun(context.Background(), ChildRunSummaryRequest{Project: "repo", RunID: "run-child"})
	require.NoError(t, err)
	require.Equal(t, ChildRunSummary{Total: 2, Unresolved: 1}, summary, "op-2 has no terminal event; op-3 of other-run must not leak into the summary")

	_, err = activities.SummarizeChildRun(context.Background(), ChildRunSummaryRequest{Project: "repo"})
	require.Error(t, err, "run identity is required")
}

func TestUncertainOperationsCarryDelegationContext(t *testing.T) {
	stamp := time.Now().UTC()
	failed := toolEvent("repo", "run-parent", "op-delegate", "tool.failed", stamp)
	failed.Data["error_type"] = "delegated_run_failed"
	failed.Data["effect"] = "uncertain"
	failed.Data["child_run_id"] = "run-parent/delegate/01"
	failed.Data["child_ops_total"] = float64(3)
	failed.Data["child_ops_unresolved"] = float64(1)
	server := journalStub(t,
		toolEvent("repo", "run-parent", "op-delegate", "tool.started", stamp),
		failed,
	)
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL, APIToken: "operator"}

	operations, err := activities.UncertainOperations(context.Background(), "repo", stubLiveness{})
	require.NoError(t, err)
	require.Len(t, operations, 1)
	operation := operations[0]
	require.Equal(t, "failed_uncertain", operation.Reason)
	require.Equal(t, "delegated_run_failed", operation.ErrorType)
	require.Equal(t, "run-parent/delegate/01", operation.ChildRunID)
	require.Equal(t, 3, operation.ChildOpsTotal)
	require.Equal(t, 1, operation.ChildOpsUnresolved)
}
