package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// Operation state model for reconciliation. An operation is uncertain when
// the event stream cannot prove whether its effect landed: either the tool
// activity failed at/after the execution boundary, or the terminal event is
// missing entirely (worker crash inside the activity).
const (
	OperationStateUncertain = "uncertain"
	OperationStateInFlight  = "in_flight"
)

// Reconciliation effects an operator may assert after investigating the
// downstream system. "unknown" is a legitimate terminal record: the operator
// documents that the outcome could not be established.
const (
	ReconciledEffectNone     = "none"
	ReconciledEffectOccurred = "occurred"
	ReconciledEffectUnknown  = "unknown"
)

// UncertainOperation is one operation whose effect state the event stream
// cannot settle on its own.
type UncertainOperation struct {
	Project       string    `json:"project"`
	RunID         string    `json:"run_id"`
	OperationID   string    `json:"operation_id"`
	Tool          string    `json:"tool"`
	Server        string    `json:"server,omitempty"`
	ArgumentsHash string    `json:"arguments_hash,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	StartedEvent  string    `json:"started_event_id"`
	SourceID      string    `json:"source_id"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
	// Delegation context: when the failed tool is a delegate call, the terminal
	// tool.failed event carries the child run identity and a journal-derived
	// summary of the child's operations, so the UI can point the operator at
	// the child trajectory instead of "check the external system".
	ErrorType          string `json:"error_type,omitempty"`
	ChildRunID         string `json:"child_run_id,omitempty"`
	ChildOpsTotal      int    `json:"child_ops_total,omitempty"`
	ChildOpsUnresolved int    `json:"child_ops_unresolved,omitempty"`
}

// WorkflowLiveness answers whether the run's workflow is still executing.
// Temporal is the source of truth: a live workflow may still emit the
// terminal event, a finished one never will.
type WorkflowLiveness interface {
	IsWorkflowRunning(ctx context.Context, sourceID, project, runID string) (bool, error)
}

// ReconcileRequest records an operator's verdict about one operation.
type ReconcileRequest struct {
	Project        string                 `json:"project"`
	RunID          string                 `json:"run_id"`
	OperationID    string                 `json:"operation_id"`
	Effect         string                 `json:"effect"` // none | occurred | unknown
	Note           string                 `json:"note,omitempty"`
	Evidence       []observation.Evidence `json:"evidence,omitempty"`
	ActorID        string                 `json:"actor_id"`
	StartedEventID string                 `json:"started_event_id,omitempty"`
}

// staleInFlightAfter bounds how long a tool.started inside a running workflow
// can stay without a terminal event before it counts as uncertain: the tool
// activity's StartToClose timeout is 4 minutes, so anything older than this
// means the workflow is stuck or the worker died without the timeout firing.
const staleInFlightAfter = 10 * time.Minute

// UncertainOperations projects tool lifecycle events from the journal into
// operations whose effect state is unresolved. The event stream stays the
// source of truth — nothing is stored here.
func (a *Activities) UncertainOperations(ctx context.Context, project string, liveness WorkflowLiveness) ([]UncertainOperation, error) {
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	started, err := a.toolEvents(ctx, project, "tool.started")
	if err != nil {
		return nil, err
	}
	terminals := map[string]observation.Event{}
	// operation.reconciled is terminal too: once an operator recorded a
	// verdict (including effect=unknown), the operation is settled and must
	// not resurface in the uncertain listing on every scan.
	for _, terminalType := range []string{"tool.completed", "tool.failed", "tool.blocked", "operation.reconciled"} {
		events, err := a.toolEvents(ctx, project, terminalType)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			terminals[eventKey(event)] = event
		}
	}
	var uncertain []UncertainOperation
	for _, event := range started {
		operation, ok := operationFromEvent(event)
		if !ok {
			continue
		}
		if terminal, settled := terminals[eventKey(event)]; settled {
			// A failed activity does not settle the effect question: the failure
			// happened at or after the execution boundary (effect=uncertain; legacy
			// events without the field but with error_type=activity_failed count
			// too). Completed/blocked operations are settled for real.
			if terminal.Type == "tool.failed" && failedUncertain(terminal) {
				operation.State = OperationStateUncertain
				operation.Reason = "failed_uncertain"
				operation.ErrorType = dataString(terminal.Data, "error_type")
				operation.ChildRunID = dataString(terminal.Data, "child_run_id")
				if total, ok := terminal.Data["child_ops_total"].(float64); ok {
					operation.ChildOpsTotal = int(total)
				}
				if unresolved, ok := terminal.Data["child_ops_unresolved"].(float64); ok {
					operation.ChildOpsUnresolved = int(unresolved)
				}
				uncertain = append(uncertain, operation)
			}
			continue
		}
		running, err := liveness.IsWorkflowRunning(ctx, event.Source.ID, event.Context.Project, event.Context.Run)
		if err != nil {
			return nil, fmt.Errorf("check workflow %s: %w", event.Context.Run, err)
		}
		operation.State = OperationStateInFlight
		operation.Reason = "workflow_running"
		if running && time.Since(event.OccurredAt) > staleInFlightAfter {
			operation.State = OperationStateUncertain
			operation.Reason = "stale_in_flight"
		} else if !running {
			operation.State = OperationStateUncertain
			operation.Reason = "crash_window"
		}
		if operation.State != OperationStateUncertain {
			continue
		}
		uncertain = append(uncertain, operation)
	}
	return uncertain, nil
}

// toolEvents pages through one event type for a project.
func (a *Activities) toolEvents(ctx context.Context, project, eventType string) ([]observation.Event, error) {
	var collected []observation.Event
	cursor := ""
	for {
		query := fmt.Sprintf("project=%s&type=%s&limit=500", urlQueryEscape(project), eventType)
		if cursor != "" {
			query += "&cursor=" + urlQueryEscape(cursor)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.TemporalityURL+"/v1/observations/events?"+query, nil)
		if err != nil {
			return nil, err
		}
		if a.APIToken != "" {
			request.Header.Set("Authorization", "Bearer "+a.APIToken)
		}
		response, err := a.HTTP.Do(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("Temporality event listing returned HTTP %d", response.StatusCode)
		}
		var page struct {
			Events     []observation.Event `json:"events"`
			NextCursor string              `json:"next_cursor"`
		}
		err = json.NewDecoder(response.Body).Decode(&page)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		collected = append(collected, page.Events...)
		if page.NextCursor == "" {
			return collected, nil
		}
		cursor = page.NextCursor
	}
}

func eventKey(event observation.Event) string {
	return event.Source.ID + "\x00" + event.Context.Run + "\x00" + dataString(event.Data, "operation_id")
}

func operationFromEvent(event observation.Event) (UncertainOperation, bool) {
	if event.Context.Run == "" {
		return UncertainOperation{}, false
	}
	operationID := dataString(event.Data, "operation_id")
	if operationID == "" {
		return UncertainOperation{}, false
	}
	return UncertainOperation{
		Project:       event.Context.Project,
		RunID:         event.Context.Run,
		OperationID:   operationID,
		Tool:          dataString(event.Data, "tool"),
		Server:        dataString(event.Data, "server"),
		ArgumentsHash: dataString(event.Data, "arguments_hash"),
		StartedAt:     event.OccurredAt,
		StartedEvent:  event.EventID,
		SourceID:      event.Source.ID,
	}, true
}

func dataString(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return value
}

// failedUncertain reports whether a tool.failed event leaves the effect
// unresolved: explicit effect=uncertain, or a legacy activity failure
// recorded before the field existed (effect=none rejections are settled).
func failedUncertain(event observation.Event) bool {
	switch dataString(event.Data, "effect") {
	case "none":
		return false
	case "uncertain":
		return true
	}
	return dataString(event.Data, "error_type") == "activity_failed"
}

// ChildRunSummaryRequest asks for the operation summary of one delegated
// child run after it reached a terminal state.
type ChildRunSummaryRequest struct {
	Project string `json:"project"`
	RunID   string `json:"run_id"`
}

// ChildRunSummary describes a delegated child run's tool operations: how many
// executed and how many the journal cannot settle.
type ChildRunSummary struct {
	Total      int `json:"total"`
	Unresolved int `json:"unresolved"`
}

// SummarizeChildRun classifies a finished delegated run's operations from the
// journal. Called by the parent workflow right after the child failed, so the
// parent can report a precise effect instead of a generic "uncertain".
func (a *Activities) SummarizeChildRun(ctx context.Context, request ChildRunSummaryRequest) (ChildRunSummary, error) {
	if request.Project == "" || request.RunID == "" {
		return ChildRunSummary{}, fmt.Errorf("project and run_id are required")
	}
	events, err := a.fetchRunEvents(ctx, request.Project, request.RunID)
	if err != nil {
		return ChildRunSummary{}, fmt.Errorf("load child run events: %w", err)
	}
	return classifyChildOperations(events), nil
}

// classifyChildOperations counts a run's tool operations and how many remain
// unsettled, using the same rules as UncertainOperations: a tool.started with
// no terminal event, or with a tool.failed that failedUncertain considers
// unresolved. Pure so it can be unit-tested without the journal.
func classifyChildOperations(events []observation.Event) ChildRunSummary {
	terminals := map[string]observation.Event{}
	for _, event := range events {
		switch event.Type {
		case "tool.completed", "tool.failed", "tool.blocked", "operation.reconciled":
			terminals[eventKey(event)] = event
		}
	}
	var summary ChildRunSummary
	for _, event := range events {
		if event.Type != "tool.started" {
			continue
		}
		if _, ok := operationFromEvent(event); !ok {
			continue
		}
		summary.Total++
		if terminal, settled := terminals[eventKey(event)]; settled {
			if terminal.Type == "tool.failed" && failedUncertain(terminal) {
				summary.Unresolved++
			}
			continue
		}
		summary.Unresolved++
	}
	return summary
}

// RecordReconciliation writes the durable operation.reconciled event. It is
// derived data with provenance: it links back to the tool.started event it
// resolves and never rewrites execution history.
func (a *Activities) RecordReconciliation(ctx context.Context, request ReconcileRequest) (observation.Event, error) {
	switch request.Effect {
	case ReconciledEffectNone, ReconciledEffectOccurred, ReconciledEffectUnknown:
	default:
		return observation.Event{}, fmt.Errorf("effect must be one of none|occurred|unknown")
	}
	if request.Project == "" || request.RunID == "" || request.OperationID == "" || request.ActorID == "" {
		return observation.Event{}, fmt.Errorf("project, run_id, operation_id and actor_id are required")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", request.Project, request.RunID, request.OperationID, time.Now().UnixNano())))
	event := observation.Event{
		Schema:     "temporality.event/1",
		EventID:    fmt.Sprintf("reconciled/%s", hex.EncodeToString(digest[:12])),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: a.SourceID, Integration: "agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: request.Project, Run: request.RunID,
			Actor:         observation.Actor{ID: request.ActorID, Type: "human"},
			ParentEventID: request.StartedEventID,
		},
		Type: "operation.reconciled",
		Data: map[string]any{
			"operation_id":  request.OperationID,
			"effect":        request.Effect,
			"note":          boundedNote(request.Note),
			"reconciled_by": request.ActorID,
			"derived":       true,
		},
		Evidence: request.Evidence,
	}
	if err := event.Validate(); err != nil {
		return observation.Event{}, err
	}
	payload, err := json.Marshal(map[string]any{"events": []observation.Event{event}})
	if err != nil {
		return observation.Event{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TemporalityURL+"/v1/observations/events", bytes.NewReader(payload))
	if err != nil {
		return observation.Event{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if a.APIToken != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+a.APIToken)
	}
	response, err := a.HTTP.Do(httpRequest)
	if err != nil {
		return observation.Event{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return observation.Event{}, fmt.Errorf("Temporality event ingest returned HTTP %d", response.StatusCode)
	}
	return event, nil
}

func boundedNote(note string) string {
	if len(note) > 2000 {
		return note[:2000] + "…"
	}
	return note
}

func urlQueryEscape(value string) string {
	// Minimal RFC 3986 escaping sufficient for project ids and cursors.
	var b bytes.Buffer
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '~':
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}
