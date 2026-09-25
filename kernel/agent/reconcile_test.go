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
			for _, event := range events {
				if event.Type == eventType {
					matching = append(matching, event)
				}
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
	server := journalStub(t,
		toolEvent("repo", "run-1", "op-done", "tool.started", old),
		toolEvent("repo", "run-1", "op-done", "tool.completed", old),
		toolEvent("repo", "run-2", "op-crashed", "tool.started", old),
		toolEvent("repo", "run-3", "op-live", "tool.started", recent),
		toolEvent("repo", "run-4", "op-stale", "tool.started", old),
	)
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
