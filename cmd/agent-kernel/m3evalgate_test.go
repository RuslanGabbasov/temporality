package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// fakeEvalGateStore satisfies the gate handler's storage surface without a
// database; the decision is preset per test and the counters assert how far
// a request got (specs/quality-loop.md §5, §8.3).
type fakeEvalGateStore struct {
	mu sync.Mutex

	agent    workspace.Agent
	proposal workspace.AgentProposal
	decision workspace.EvalGateDecision

	agentErr     error
	proposalErr  error
	decisionErr  error
	createErr    error
	statusCalls  int
	createCalls  int
	createRuns   []workspace.AgentEvaluationRun
	nextRunID    int64
	audits       []storedAudit
	abandonedIDs []int64
}

type storedAudit struct {
	actor      string
	action     string
	entityKind string
	entityID   string
	details    map[string]any
}

func (f *fakeEvalGateStore) GetAgent(_ context.Context, _ string) (workspace.Agent, error) {
	return f.agent, f.agentErr
}

func (f *fakeEvalGateStore) GetAgentProposal(_ context.Context, _ string, _ int64) (workspace.AgentProposal, error) {
	return f.proposal, f.proposalErr
}

func (f *fakeEvalGateStore) EvalGateStatus(_ context.Context, _ string, _ int64, _ int, _ time.Time) (workspace.EvalGateDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	return f.decision, f.decisionErr
}

func (f *fakeEvalGateStore) CreateGateRun(_ context.Context, _ string, _ int, proposalID int64) (workspace.AgentEvaluationRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.createErr != nil {
		return workspace.AgentEvaluationRun{}, f.createErr
	}
	f.nextRunID++
	run := workspace.AgentEvaluationRun{
		ID: f.nextRunID, AgentID: f.agent.ID, AgentVersion: f.proposal.BaseVersion,
		Trigger: agent.EvalTriggerGate, ProposalID: proposalID, CreatedAt: time.Now().UTC(),
	}
	f.createRuns = append(f.createRuns, run)
	return run, nil
}

func (f *fakeEvalGateStore) AbandonGateRun(_ context.Context, runID int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.abandonedIDs = append(f.abandonedIDs, runID)
	return nil
}

func (f *fakeEvalGateStore) RecordAccessAudit(_ context.Context, actor, action, entityKind, entityID string, details map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, storedAudit{actor: actor, action: action, entityKind: entityKind, entityID: entityID, details: details})
	return nil
}

// fakeEvalGateRunner records the runs the handler started; it signals on
// done so tests can wait for the background goroutine deterministically.
type fakeEvalGateRunner struct {
	mu    sync.Mutex
	calls int
	done  chan struct{}
	err   error
}

func (f *fakeEvalGateRunner) RunGateEvaluation(_ context.Context, _ string, _ int, _ agent.EvalRunMeta) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.done != nil {
		close(f.done)
	}
	return "", f.err
}

type captureEvalGateOutbox struct {
	mu     sync.Mutex
	events []observation.Event
}

func (c *captureEvalGateOutbox) Enqueue(_ context.Context, event observation.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *captureEvalGateOutbox) byType(eventType string) []observation.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var matched []observation.Event
	for _, event := range c.events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

// newEvalGateServer mounts the gated apply handler alone, the way main
// wires it — real RBAC gate, fake store/runner/outbox.
func newEvalGateServer(t *testing.T, store *fakeEvalGateStore, runner *fakeEvalGateRunner, outbox evalGateOutbox, apply evalApplyFunc) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*")
	require.NoError(t, err)
	if apply == nil {
		apply = func(w http.ResponseWriter, _ *http.Request, _ workspace.Agent, _ workspace.AgentProposal) {
			writeJSON(w, 200, map[string]any{"applied": true})
		}
	}
	mux := http.NewServeMux()
	RegisterM3Routes(mux, gate, store, runner, outbox, apply,
		func(*http.Request) []string { return nil }, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return httptest.NewServer(gate.Authenticate(mux))
}

func evalGatePost(t *testing.T, server *httptest.Server, token, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost,
		server.URL+"/v1/workspace/agents/agent-a/proposals/7/apply", strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(raw)
}

func defaultEvalGateFixture() (*fakeEvalGateStore, *fakeEvalGateRunner, *captureEvalGateOutbox) {
	store := &fakeEvalGateStore{
		agent:    workspace.Agent{ID: "agent-a", Name: "Agent A", DefinitionVersion: 4},
		proposal: workspace.AgentProposal{ID: 7, AgentID: "agent-a", BaseVersion: 4, Status: "pending"},
	}
	runner := &fakeEvalGateRunner{}
	return store, runner, &captureEvalGateOutbox{}
}

// A fresh green run on the base version lets the apply through and emits
// agent.evaluation.gate.passed (§5.3, §9).
func TestEvalGateGreenAppliesAndEmitsPassed(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	green := &workspace.AgentEvaluationRun{ID: 41, AgentVersion: 4, Verdict: workspace.EvalVerdictPass}
	store.decision = workspace.EvalGateDecision{State: workspace.EvalGateGreen, Run: green}
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, body := evalGatePost(t, server, "writertoken", "")
	require.Equal(t, 200, status)
	require.Contains(t, body, `"applied":true`)
	require.Zero(t, store.createCalls)
	require.Zero(t, runner.calls)
	passed := outbox.byType("agent.evaluation.gate.passed")
	require.Len(t, passed, 1)
	require.EqualValues(t, 41, passed[0].Data["evaluation_run_id"])
}

// Nothing usable on the base version: the server pre-creates the gate run,
// starts it and answers 202 with that exact evaluation_run_id (§5.3).
func TestEvalGateStartsRunAndAnswers202(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	store.decision = workspace.EvalGateDecision{State: workspace.EvalGateNone}
	runner.done = make(chan struct{})
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, body := evalGatePost(t, server, "writertoken", "")
	require.Equal(t, 202, status)
	var accepted struct {
		EvaluationRunID int64  `json:"evaluation_run_id"`
		Status          string `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &accepted))
	require.EqualValues(t, 1, accepted.EvaluationRunID)
	require.Equal(t, "running", accepted.Status)
	require.Equal(t, 1, store.createCalls)
	<-runner.done
	require.Equal(t, 1, runner.calls)
}

// §8.3 idempotency: while the gate run is unfinished, a repeat apply gets
// 202 with the same evaluation_run_id and no second run is created.
func TestEvalGateRepeatApplyIsIdempotent202(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	store.decision = workspace.EvalGateDecision{State: workspace.EvalGateNone}
	runner.done = make(chan struct{})
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, body := evalGatePost(t, server, "writertoken", "")
	require.Equal(t, 202, status)
	<-runner.done
	var first struct {
		EvaluationRunID int64 `json:"evaluation_run_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &first))

	// The run is still unfinished: the store now reports the running state
	// (EvalGateStatus finds the proposal's verdict='unknown' gate run).
	running := &workspace.AgentEvaluationRun{ID: first.EvaluationRunID, AgentVersion: 4, Trigger: agent.EvalTriggerGate}
	store.mu.Lock()
	store.decision = workspace.EvalGateDecision{State: workspace.EvalGateRunning, Run: running}
	store.mu.Unlock()

	status, body = evalGatePost(t, server, "writertoken", "")
	require.Equal(t, 202, status)
	var second struct {
		EvaluationRunID int64 `json:"evaluation_run_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &second))
	require.Equal(t, first.EvaluationRunID, second.EvaluationRunID)
	require.Equal(t, 1, store.createCalls) // no second run
	require.Equal(t, 1, runner.calls)      // no second evaluation
}

// A red run blocks the apply with 409 and names the failed cases; the
// blocked event carries the cause (§5.4, §8.3).
func TestEvalGateRedBlocksWithFailedCases(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	store.decision = workspace.EvalGateDecision{
		State: workspace.EvalGateRed,
		Run: &workspace.AgentEvaluationRun{ID: 52, AgentVersion: 4, Verdict: workspace.EvalVerdictFail, Cases: []workspace.EvaluationCaseResult{
			{Name: "golden/abc123", Passed: false},
			{Name: "smoke", Passed: true},
		}},
		Cause: "",
	}
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, body := evalGatePost(t, server, "writertoken", "")
	require.Equal(t, 409, status)
	require.Contains(t, body, `"gate_blocked"`)
	require.Contains(t, body, "golden/abc123")
	require.NotContains(t, body, `"smoke"`)
	blocked := outbox.byType("agent.evaluation.gate.blocked")
	require.Len(t, blocked, 1)
	require.Zero(t, store.createCalls)
}

// Override (§5.4, §11.2): a writer promotes a red gate with a justification —
// the apply goes through, the audit entry and the overridden event record
// who and why.
func TestEvalGateRedOverrideByWriter(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	store.decision = workspace.EvalGateDecision{
		State: workspace.EvalGateRed,
		Run:   &workspace.AgentEvaluationRun{ID: 61, AgentVersion: 4, Verdict: workspace.EvalVerdictFail},
		Cause: workspace.CauseJudgeDegraded,
	}
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, body := evalGatePost(t, server, "writertoken", `{"override":true,"justification":"the failed golden is outdated; reviewed with the owner"}`)
	require.Equal(t, 200, status)
	require.Contains(t, body, `"applied":true`)

	require.Len(t, store.audits, 1)
	audit := store.audits[0]
	require.Equal(t, workspace.AuditEvalGateOverridden, audit.action)
	require.Equal(t, "agent_proposal", audit.entityKind)
	require.Equal(t, "7", audit.entityID)
	require.Equal(t, "writer", audit.actor)
	require.Contains(t, audit.details["override_justification"], "outdated")

	overridden := outbox.byType("agent.evaluation.gate.overridden")
	require.Len(t, overridden, 1)
	require.Equal(t, workspace.CauseJudgeDegraded, overridden[0].Data["cause"])
	require.Contains(t, overridden[0].Data["override_justification"], "outdated")
	require.Empty(t, outbox.byType("agent.evaluation.gate.blocked"))
}

// An override without a justification is refused — the red gate still holds
// (§5.4: override stays attributable).
func TestEvalGateOverrideRequiresJustification(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	store.decision = workspace.EvalGateDecision{
		State: workspace.EvalGateRed,
		Run:   &workspace.AgentEvaluationRun{ID: 62, AgentVersion: 4, Verdict: workspace.EvalVerdictFail},
	}
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, body := evalGatePost(t, server, "writertoken", `{"override":true}`)
	require.Equal(t, 400, status)
	require.Contains(t, body, "justification")
	require.Empty(t, store.audits)

	status, _ = evalGatePost(t, server, "writertoken", `{"override":true,"justification":"   "}`)
	require.Equal(t, 400, status)
	require.Empty(t, store.audits)
}

// Below-writer roles can neither apply nor override (§8.3: 403).
func TestEvalGateReaderForbidden(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	store.decision = workspace.EvalGateDecision{State: workspace.EvalGateGreen,
		Run: &workspace.AgentEvaluationRun{ID: 70, Verdict: workspace.EvalVerdictPass}}
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, _ := evalGatePost(t, server, "readertoken", "")
	require.Equal(t, 403, status)
	status, _ = evalGatePost(t, server, "readertoken", `{"gate":false}`)
	require.Equal(t, 403, status)
	status, _ = evalGatePost(t, server, "readertoken", `{"override":true,"justification":"x"}`)
	require.Equal(t, 403, status)
	require.Zero(t, store.statusCalls)
	require.Zero(t, store.createCalls)
}

// gate:false skips the gate decision entirely — no lookup, no run, no event
// (§8.3: writer/owner right).
func TestEvalGateFalseSkipsGate(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	store.decision = workspace.EvalGateDecision{State: workspace.EvalGateRed,
		Run: &workspace.AgentEvaluationRun{ID: 71, Verdict: workspace.EvalVerdictFail}}
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, body := evalGatePost(t, server, "writertoken", `{"gate":false}`)
	require.Equal(t, 200, status)
	require.Contains(t, body, `"applied":true`)
	require.Zero(t, store.statusCalls)
	require.Zero(t, store.createCalls)
	require.Empty(t, outbox.byType("agent.evaluation.gate.passed"))
	require.Empty(t, outbox.byType("agent.evaluation.gate.blocked"))
}

// A proposal whose base version moved past the agent is refused with 409
// base_version_stale before any gate work happens (§5.5, §10.1б).
func TestEvalGateBaseVersionStale(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	store.proposal.BaseVersion = 3 // agent is at 4
	store.decision = workspace.EvalGateDecision{State: workspace.EvalGateGreen,
		Run: &workspace.AgentEvaluationRun{ID: 72, Verdict: workspace.EvalVerdictPass}}
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, body := evalGatePost(t, server, "writertoken", "")
	require.Equal(t, 409, status)
	require.Contains(t, body, "base_version_stale")
	require.Contains(t, body, `"current_version":4`)
	require.Zero(t, store.statusCalls)
	require.Zero(t, store.createCalls)
}

// A decided proposal is a conflict, exactly like the pre-gate handler (§8.3).
func TestEvalGateNonPendingProposalConflicts(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	store.proposal.Status = "applied"
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, body := evalGatePost(t, server, "writertoken", "")
	require.Equal(t, 409, status)
	require.Contains(t, body, "already applied")
}

// A malformed body is a client error, never a silently-defaulted gate.
func TestEvalGateMalformedBody(t *testing.T) {
	store, runner, outbox := defaultEvalGateFixture()
	server := newEvalGateServer(t, store, runner, outbox, nil)
	defer server.Close()

	status, _ := evalGatePost(t, server, "writertoken", `{"gate":`)
	require.Equal(t, 400, status)
	require.Zero(t, store.statusCalls)
}
