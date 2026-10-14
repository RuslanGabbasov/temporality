package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	// Investigation context: everything an operator needs to answer "who
	// called whom, with what parameters, and why is this unresolved" without
	// opening the run trace. Arguments come from tool.started (bounded at emit
	// time); ErrorDetail from the terminal tool.failed; AgentID/ParentRunID/
	// Title from the run's run.started.
	Arguments   string `json:"arguments,omitempty"`
	ErrorDetail string `json:"error_detail,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	ParentRunID string `json:"parent_run_id,omitempty"`
	Title       string `json:"title,omitempty"`
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

// defaultOperationsWindow bounds how far back the uncertain-operations
// listing scans the journal (overridable via TEMPORALITY_OPERATIONS_WINDOW).
// The listing runs on every operations-panel poll — 15s from the UI — and each
// call pages through six event types with full payloads (tool arguments), so
// an unbounded scan made the listing slower with every recorded event until
// the journal pool saturated and /v1/agent/operations started answering 500
// on busy projects. A crash-window question older than a week is stale as an
// operator question anyway: the run is long gone, the evidence cooled down,
// and the journal row stays queryable by run id regardless.
const defaultOperationsWindow = 7 * 24 * time.Hour

// toolEventsMaxPages caps pagination per event type. 40 pages × 500 events =
// 20k events of one type inside the window is far beyond any healthy project;
// stopping there keeps a pathological journal (or a cursor bug) from turning
// one listing call into an endless walk.
const toolEventsMaxPages = 40

// isOrchestrationTool marks tools that never run as a tool activity: plan
// and delegate orchestrate child workflows from workflow code (durable,
// replayed by Temporal) and routinely stay open for the whole DAG — rework
// rounds included — far past the tool-activity timeout the staleness
// heuristic is built on. Their effect is the child runs themselves, fully
// visible in the journal with their own tool operations, so an open
// plan/delegate call inside a running workflow is progress, not a stuck
// activity — the operator question it would raise ("verify the external
// system") has no external system to check.
func isOrchestrationTool(tool string) bool {
	bare, prefixed := bareToolName(tool)
	return !prefixed && (bare == "plan" || bare == "delegate")
}

// UncertainOperations projects tool lifecycle events from the journal into
// operations whose effect state is unresolved. The event stream stays the
// source of truth — nothing is stored here. An empty project lists across
// every project (docs/activity-scoping.md §5): the caller — the kernel API
// handler — is responsible for constraining the result to the principal's
// visible projects, which it can do losslessly because every operation
// carries its own project.
func (a *Activities) UncertainOperations(ctx context.Context, project string, liveness WorkflowLiveness) ([]UncertainOperation, error) {
	window := a.OperationsWindow
	if window <= 0 {
		window = defaultOperationsWindow
	}
	since := time.Now().UTC().Add(-window)
	started, err := a.toolEvents(ctx, project, "tool.started", since)
	if err != nil {
		return nil, err
	}
	// Run headers give each operation its "who": the agent that executed the
	// call, the parent run for delegated work, and the human-readable task
	// title. One extra scan per listing call; the journal is the source of
	// truth and nothing is stored.
	//
	// The same since-window applies to terminal events and run headers: a
	// terminal event always follows its tool.started, so anything that started
	// inside the window has its terminals inside the window too — windowing
	// never marks a settled operation as uncertain.
	runStarted, err := a.toolEvents(ctx, project, "run.started", since)
	if err != nil {
		return nil, err
	}
	runHeaders := make(map[string]runHeader, len(runStarted))
	for _, event := range runStarted {
		if event.Context.Run == "" {
			continue
		}
		runHeaders[event.Source.ID+"\x00"+event.Context.Run] = runHeader{
			AgentID:     dataString(event.Data, "agent_id"),
			ParentRunID: dataString(event.Data, "parent_run_id"),
			Title:       dataString(event.Data, "title"),
		}
	}
	terminals := map[string]observation.Event{}
	// operation.reconciled is terminal too: once an operator recorded a
	// verdict (including effect=unknown), the operation is settled and must
	// not resurface in the uncertain listing on every scan.
	for _, terminalType := range []string{"tool.completed", "tool.failed", "tool.blocked", "operation.reconciled"} {
		events, err := a.toolEvents(ctx, project, terminalType, since)
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
		if header, found := runHeaders[event.Source.ID+"\x00"+event.Context.Run]; found {
			operation.AgentID = header.AgentID
			operation.ParentRunID = header.ParentRunID
			operation.Title = header.Title
		}
		// A read that died at the boundary leaves nothing behind — never an
		// operator question, regardless of what a legacy terminal event claimed.
		if isReadOnlyEventCall(operation.Tool, operation.Arguments) {
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
				// The actual failure text — the "why is this question asked"
				// behind the listing. Newer events carry "error", some paths only
				// "detail"; both beat a bare error_type code.
				operation.ErrorDetail = dataString(terminal.Data, "error")
				if operation.ErrorDetail == "" {
					operation.ErrorDetail = dataString(terminal.Data, "detail")
				}
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
		running, livenessErr := liveness.IsWorkflowRunning(ctx, event.Source.ID, event.Context.Project, event.Context.Run)
		if livenessErr != nil {
			// One Temporal hiccup must not blank the whole listing — every other
			// operation still deserves attention. Surface this one as uncertain
			// with the diagnostic in the reason; the operator can reconcile it
			// (effect=unknown) once the underlying workflow state is knowable.
			operation.State = OperationStateUncertain
			operation.Reason = "liveness_error"
			operation.ErrorDetail = livenessErr.Error()
			uncertain = append(uncertain, operation)
			continue
		}
		operation.State = OperationStateInFlight
		operation.Reason = "workflow_running"
		if running && time.Since(event.OccurredAt) > staleInFlightAfter && !isOrchestrationTool(operation.Tool) {
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
// toolEvents pages through the journal's event listing for one event type.
// since bounds the scan (empty time.Time = unbounded); the operations listing
// always passes one, other callers may not.
func (a *Activities) toolEvents(ctx context.Context, project, eventType string, since time.Time) ([]observation.Event, error) {
	var collected []observation.Event
	cursor := ""
	for page := 0; page < toolEventsMaxPages; page++ {
		query := fmt.Sprintf("project=%s&type=%s&limit=500", urlQueryEscape(project), eventType)
		if !since.IsZero() {
			query += "&since=" + urlQueryEscape(since.Format(time.RFC3339Nano))
		}
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
			// Carry the journal's own error body (e.g. "could not list
			// observation events") — a bare status code is not diagnosable
			// from the operations panel's error banner.
			body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
			response.Body.Close()
			return nil, fmt.Errorf("journal event listing (type=%s project=%s) returned HTTP %d: %s", eventType, project, response.StatusCode, strings.TrimSpace(string(body)))
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
	// Page cap reached: return what was collected rather than erroring — the
	// listing stays useful (a bounded prefix) and a pathological journal cannot
	// hang the endpoint. The cap is far beyond any healthy project's volume.
	return collected, nil
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
		// The literal call arguments (bounded at emit time), not the hash: the
		// operator must see the command/query that may have landed downstream.
		Arguments:    dataString(event.Data, "arguments"),
		StartedAt:    event.OccurredAt,
		StartedEvent: event.EventID,
		SourceID:     event.Source.ID,
	}, true
}

// runHeader is the run-level context attached to every operation.
type runHeader struct {
	AgentID     string
	ParentRunID string
	Title       string
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

// isReadOnlyEventCall classifies a journal operation (tool + arguments JSON
// string) the same way toolFailureData classifies a live call, so legacy
// events recorded before the effect classification existed are filtered too.
func isReadOnlyEventCall(tool, argsJSON string) bool {
	if isReadOnlyTool(tool) {
		return true
	}
	if bare, _ := bareToolName(tool); bare != "browser" {
		return false
	}
	var args struct {
		Action string `json:"action"`
	}
	if json.Unmarshal([]byte(argsJSON), &args) != nil {
		return false
	}
	return browserReadOnlyActions[args.Action]
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
