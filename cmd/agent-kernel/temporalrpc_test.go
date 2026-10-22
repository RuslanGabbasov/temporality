package main

// Unit tests for R1/R2 of docs/fixes/kernel-wedge-2026-10-21: a wedged
// Temporal-RPC must break at its deadline and answer HTTP 503
// "temporal rpc timeout: …" instead of hanging the handler forever, while a
// healthy Temporal keeps every response code it had before (R5). The R2
// server budgets (WriteTimeout/IdleTimeout) get their own pin — including
// the SSE exemption: a stream that clears its write deadline must outlive
// the server-wide WriteTimeout.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── fakes ────────────────────────────────────────────────────────────────────

// fakeWorkflowRun is the client.WorkflowRun a fast ExecuteWorkflow returns.
type fakeWorkflowRun struct {
	id    string
	runID string
}

func (f fakeWorkflowRun) GetID() string    { return f.id }
func (f fakeWorkflowRun) GetRunID() string { return f.runID }

// Get satisfies client.WorkflowRun; the start-run path never calls it.
func (f fakeWorkflowRun) Get(context.Context, any) error { return nil }

// GetWithOptions satisfies client.WorkflowRun; never called on this path.
func (f fakeWorkflowRun) GetWithOptions(context.Context, any, client.WorkflowRunGetOptions) error {
	return nil
}

// fakeTemporalClient is the fake temporal client of the R1 tests: a blocking
// RPC hangs until the ctx deadline breaks it and returns ctx.Err() — the way
// the real transport surfaces a deadline — a fast one answers immediately.
// err answers immediately (a live deadline, the caller's own mapping), lateErr
// hangs until the deadline passes and then returns a non-timeout error (the
// transport shape where the deadline expired but the surfaced error is not
// readable as one).
type fakeTemporalClient struct {
	blocking bool
	err      error
	lateErr  error
}

// outcome is the shared failure shape of every fake RPC below.
func (f *fakeTemporalClient) outcome(ctx context.Context) error {
	if f.err != nil {
		return f.err
	}
	if f.lateErr != nil {
		<-ctx.Done()
		return f.lateErr
	}
	if f.blocking {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (f *fakeTemporalClient) ExecuteWorkflow(ctx context.Context, _ client.StartWorkflowOptions, _ string, _ ...any) (client.WorkflowRun, error) {
	if err := f.outcome(ctx); err != nil {
		return nil, err
	}
	return fakeWorkflowRun{id: "workflow-1", runID: "temporal-run-1"}, nil
}

// SignalWorkflow/CancelWorkflow/TerminateWorkflow serve the R1 route mirrors
// of the approval/cancel/terminate handlers.
func (f *fakeTemporalClient) SignalWorkflow(ctx context.Context, _, _, _ string, _ any) error {
	return f.outcome(ctx)
}

func (f *fakeTemporalClient) CancelWorkflow(ctx context.Context, _, _ string) error {
	return f.outcome(ctx)
}

func (f *fakeTemporalClient) TerminateWorkflow(ctx context.Context, _, _, _ string) error {
	return f.outcome(ctx)
}

// rpcStartedHandler mirrors the production wiring of the in-scope start-run
// handler (POST /v1/workspace/tasks/{taskID}/runs): the RPC runs through
// temporalRPC under the R1 deadline, a deadline expiry is answered by
// temporalRPC itself (done=true) and every other failure keeps the caller's
// original 409 mapping.
func rpcStartedHandler(t *fakeTemporalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var run client.WorkflowRun
		startErr, done := temporalRPC(w, r.Context(), func(ctx context.Context) error {
			var rpcErr error
			run, rpcErr = t.ExecuteWorkflow(ctx, client.StartWorkflowOptions{}, "AgentRun", nil)
			return rpcErr
		})
		if done {
			return
		}
		if startErr != nil {
			writeError(w, http.StatusConflict, startErr)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"run_id": "run-1", "workflow_id": run.GetID()})
	}
}

// postRun POSTs the start-run request and returns the status, the decoded
// JSON body and the round-trip duration (what proves "не зависает").
func postRun(t *testing.T, server *httptest.Server) (int, map[string]any, time.Duration) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/workspace/tasks/task-1/runs", strings.NewReader("{}"))
	require.NoError(t, err)
	started := time.Now()
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body), "response must be JSON: %s", string(raw))
	return response.StatusCode, body, time.Since(started)
}

// ── R1: the /nudge wiring deadline ───────────────────────────────────────────

// deadlineNudgeStore answers the run's source resolution and terminal
// lookups for the /nudge route (the route itself is the parallel
// working-tree change in nudge.go, exercised here untouched).
type deadlineNudgeStore struct{}

func (deadlineNudgeStore) RunSourceID(_ context.Context, _, _ string) (string, error) {
	return "journal-source", nil
}

func (deadlineNudgeStore) RunTerminalStatuses(_ context.Context, _ []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (deadlineNudgeStore) SettleRunCancelled(_ context.Context, _ string) error {
	return nil
}

// deadlineNudgeJournal is a no-op nudge journal (the heal path is not under
// test here — no test signals a gone workflow).
type deadlineNudgeJournal struct{}

func (deadlineNudgeJournal) Enqueue(_ context.Context, _ observation.Event) error { return nil }

// stubSignaler is the fake temporal client for the /nudge route: hang=true
// wedges the signal until the deadline breaks it, err answers a fixed
// failure, otherwise the signal is delivered instantly.
type stubSignaler struct {
	started chan struct{}
	hang    bool
	err     error
}

func (s *stubSignaler) SignalWorkflow(ctx context.Context, _, _, _ string, _ any) error {
	if s.started != nil {
		s.started <- struct{}{}
	}
	if s.err != nil {
		return s.err
	}
	if s.hang {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

// nudgeDeadlineServer mounts POST /nudge the way main wires it: real RBAC
// gate, the real nudge route (nudge.go) and the R1 nudgeWithRPCDeadline
// wrapper around it.
func nudgeDeadlineServer(t *testing.T, signaler *stubSignaler) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("admintoken:admin:admin:*\n")
	require.NoError(t, err)
	mux := http.NewServeMux()
	registerNudgeRoute(mux, nudgeDeps{
		gate:     gate,
		store:    deadlineNudgeStore{},
		journal:  deadlineNudgeJournal{},
		signaler: signaler,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	authed := gate.Authenticate(mux)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if matchesNudgeRPCRoute(r) {
			nudgeWithRPCDeadline(w, r, authed)
			return
		}
		authed.ServeHTTP(w, r)
	})
	server := httptest.NewServer(root)
	t.Cleanup(server.Close)
	return server
}

// postNudge nudges run-1 and returns the status, the decoded JSON body and
// the round-trip duration (what proves "не зависает").
func postNudge(t *testing.T, server *httptest.Server) (int, map[string]any, time.Duration) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent/runs/run-1/nudge?project=proj-a",
		strings.NewReader(`{"turn":2,"actor_id":"human-ui"}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer admintoken")
	started := time.Now()
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body), "response must be JSON: %s", string(raw))
	return response.StatusCode, body, time.Since(started)
}

// ── R1: the deadline helpers ─────────────────────────────────────────────────

// TestTemporalRPCTimeoutDefaultAndOverride pins the default (15s) and the
// KERNEL_TEMPORAL_RPC_TIMEOUT override; a broken override degrades to the
// default — never to no timeout at all.
func TestTemporalRPCTimeoutDefaultAndOverride(t *testing.T) {
	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "")
	require.Equal(t, 15*time.Second, temporalRPCTimeout())

	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "2s")
	require.Equal(t, 2*time.Second, temporalRPCTimeout())

	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "nonsense")
	require.Equal(t, 15*time.Second, temporalRPCTimeout())

	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "-1s")
	require.Equal(t, 15*time.Second, temporalRPCTimeout())
}

// TestEnvDuration pins the shared env reader used by the R1 and R2 budgets.
func TestEnvDuration(t *testing.T) {
	require.Equal(t, 120*time.Second, envDuration("KERNEL_HTTP_WRITE_TIMEOUT", 120*time.Second))

	t.Setenv("KERNEL_HTTP_WRITE_TIMEOUT", "30s")
	require.Equal(t, 30*time.Second, envDuration("KERNEL_HTTP_WRITE_TIMEOUT", 120*time.Second))

	t.Setenv("KERNEL_HTTP_WRITE_TIMEOUT", "0s")
	require.Equal(t, 120*time.Second, envDuration("KERNEL_HTTP_WRITE_TIMEOUT", 120*time.Second))

	t.Setenv("KERNEL_HTTP_WRITE_TIMEOUT", "forever")
	require.Equal(t, 120*time.Second, envDuration("KERNEL_HTTP_WRITE_TIMEOUT", 120*time.Second))
}

// TestTemporalRPCPassesNonDeadlineErrorsToCaller: a non-deadline failure is
// handed back untouched (the caller keeps its 404/409/502 mapping) and
// nothing is written — R1 must not reshape healthy-Temporal behavior (R5).
func TestTemporalRPCPassesNonDeadlineErrorsToCaller(t *testing.T) {
	recorder := httptest.NewRecorder()
	err, done := temporalRPC(recorder, context.Background(), func(context.Context) error {
		return errors.New("boom")
	})
	require.False(t, done)
	require.EqualError(t, err, "boom")
	require.Zero(t, recorder.Body.Len(), "a non-deadline error must not write R1's 503")
}

// ── R1: wedged RPC → 503 within the deadline, fast RPC → the old answer ─────

// TestBlockedTemporalRPCAnswers503WithinDeadline is the injection test of the
// incident: the fake client's ExecuteWorkflow blocks forever, the handler
// must answer HTTP 503 {"error":"temporal rpc timeout: …"} inside the R1
// deadline instead of hanging.
func TestBlockedTemporalRPCAnswers503WithinDeadline(t *testing.T) {
	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "100ms")
	server := httptest.NewServer(rpcStartedHandler(&fakeTemporalClient{blocking: true}))
	t.Cleanup(server.Close)

	code, body, elapsed := postRun(t, server)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, body["error"], "temporal rpc timeout:",
		"R1's 503 must carry the stable 'temporal rpc timeout: …' text, got %v", body["error"])
	// The deadline fired, not the transport: the answer lands within a small
	// multiple of the configured 100ms — never "never".
	require.Less(t, elapsed, 5*time.Second, "the handler must answer inside the deadline, not hang")
}

// TestFastTemporalRPCKeepsAcceptedAnswer: a healthy Temporal keeps the old
// 202 mapping (R5) — the deadline only fires for a wedged RPC.
func TestFastTemporalRPCKeepsAcceptedAnswer(t *testing.T) {
	server := httptest.NewServer(rpcStartedHandler(&fakeTemporalClient{}))
	t.Cleanup(server.Close)

	code, body, _ := postRun(t, server)
	require.Equal(t, http.StatusAccepted, code)
	require.Equal(t, "run-1", body["run_id"])
	require.Equal(t, "workflow-1", body["workflow_id"])
}

// ── R1: the /nudge wiring point ──────────────────────────────────────────────

// TestBlockedNudgeSignalAnswers503WithinDeadline: a wedged SignalWorkflow on
// the /nudge route breaks at the deadline installed at main's wiring point
// (nudgeWithRPCDeadline) and is rewritten from the route's stable 409 into
// R1's 503 "temporal rpc timeout: …".
func TestBlockedNudgeSignalAnswers503WithinDeadline(t *testing.T) {
	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "100ms")
	server := nudgeDeadlineServer(t, &stubSignaler{hang: true})

	code, body, elapsed := postNudge(t, server)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, body["error"], "temporal rpc timeout:")
	require.Less(t, elapsed, 5*time.Second, "the handler must answer inside the deadline, not hang")
}

// TestFastNudgeSignalKeepsAcceptedAnswer: a healthy signal keeps the route's
// 202 — the deadline wrapper never rewrites a live response (R5).
func TestFastNudgeSignalKeepsAcceptedAnswer(t *testing.T) {
	server := nudgeDeadlineServer(t, &stubSignaler{})

	code, body, _ := postNudge(t, server)
	require.Equal(t, http.StatusAccepted, code)
	require.Equal(t, true, body["accepted"])
}

// TestNudgeDeadlineKeepsForeign409: a signal failure that did NOT come from
// the installed deadline (the fake fails fast) stays the route's 409 — the
// rewrite fires only for an expired deadline.
func TestNudgeDeadlineKeepsForeign409(t *testing.T) {
	server := nudgeDeadlineServer(t, &stubSignaler{err: errors.New("connection refused: temporal-frontend:7233")})

	code, _, _ := postNudge(t, server)
	require.Equal(t, http.StatusConflict, code)
}

// ── R2: server budgets and the SSE exemption ────────────────────────────────

// TestNewKernelHTTPServerBudgets pins R2's server shape: default 120s
// Write/Idle timeouts with env overrides.
func TestNewKernelHTTPServerBudgets(t *testing.T) {
	server := newKernelHTTPServer(":0", http.NotFoundHandler())
	require.Equal(t, 120*time.Second, server.WriteTimeout)
	require.Equal(t, 120*time.Second, server.IdleTimeout)
	require.Equal(t, 5*time.Second, server.ReadHeaderTimeout)

	t.Setenv("KERNEL_HTTP_WRITE_TIMEOUT", "30s")
	t.Setenv("KERNEL_HTTP_IDLE_TIMEOUT", "45s")
	server = newKernelHTTPServer(":0", http.NotFoundHandler())
	require.Equal(t, 30*time.Second, server.WriteTimeout)
	require.Equal(t, 45*time.Second, server.IdleTimeout)
}

// TestSSEWriteDeadlineExemptionSurvivesWriteTimeout proves the R2 exemption
// mechanism the SSE handlers (/v1/workspace/runs/{runID}/stream and /tokens)
// rely on: the server-wide WriteTimeout is armed when request headers are
// read, so a handler that clears its write deadline with
// http.NewResponseController(w).SetWriteDeadline(time.Time{}) — exactly what
// both SSE handlers do — can keep streaming past the budget, while a handler
// without the exemption gets severed once the budget runs out.
func TestSSEWriteDeadlineExemptionSurvivesWriteTimeout(t *testing.T) {
	t.Setenv("KERNEL_HTTP_WRITE_TIMEOUT", "200ms")
	const gap = 600 * time.Millisecond // 3× the write budget

	// streamedBody writes chunk, sleeps past the WriteTimeout, writes
	// chunk again — the SSE pattern: long-lived stream, sparse writes.
	streamedBody := func(clearDeadline bool) (string, error) {
		server := newKernelHTTPServer(":0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if clearDeadline {
				// The exact exemption both SSE handlers install.
				_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
			}
			_, _ = io.WriteString(w, "chunk-1\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			time.Sleep(gap)
			_, _ = io.WriteString(w, "chunk-2\n")
		}))
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		response, err := http.Get("http://" + listener.Addr().String() + "/")
		require.NoError(t, err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		return string(raw), err
	}

	body, err := streamedBody(true)
	require.NoError(t, err, "with the exemption the stream must not be severed")
	require.Contains(t, body, "chunk-1")
	require.Contains(t, body, "chunk-2", "an SSE-style stream must outlive the server WriteTimeout")

	// Negative control: without the exemption the expired WriteTimeout
	// truncates the stream — this is what would kill /stream and /tokens.
	body, _ = streamedBody(false)
	require.NotContains(t, body, "chunk-2",
		"without the exemption the WriteTimeout severs the stream (the incident R2 protects SSE from)")
}

// ── R1 rework: gRPC-status attribution, the remaining routes, exact /nudge match ──

// postRPC POSTs path with an empty JSON body and returns the status, the
// decoded body and the round-trip duration (what proves «не зависает»).
func postRPC(t *testing.T, server *httptest.Server, path string) (int, map[string]any, time.Duration) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader("{}"))
	require.NoError(t, err)
	started := time.Now()
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body), "response must be JSON: %s", string(raw))
	return response.StatusCode, body, time.Since(started)
}

// approvalRPCHandler mirrors the production wiring of POST
// /v1/agent/runs/{runID}/approval: the signal runs through temporalRPC under
// the R1 deadline, expiry is answered by temporalRPC itself (503) and every
// other failure keeps the route's original 409 mapping.
func approvalRPCHandler(fake *fakeTemporalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		signalErr, done := temporalRPC(w, r.Context(), func(ctx context.Context) error {
			return fake.SignalWorkflow(ctx, "workflow-1", "", "agent.approval", nil)
		})
		if done {
			return
		}
		if signalErr != nil {
			writeError(w, http.StatusConflict, signalErr)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
	}
}

// cancelRPCHandler mirrors the production wiring of the two cancel routes
// (POST /v1/agent/runs/{runID}/cancel and POST /v1/workspace/runs/{runID}/cancel
// — wiring-identical: temporalRPC, 409 on failure, 200 cancel_requested).
func cancelRPCHandler(fake *fakeTemporalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cancelErr, done := temporalRPC(w, r.Context(), func(ctx context.Context) error {
			return fake.CancelWorkflow(ctx, "workflow-1", "")
		})
		if done {
			return
		}
		if cancelErr != nil {
			writeError(w, http.StatusConflict, cancelErr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"run_id": "run-1", "status": "cancel_requested"})
	}
}

// terminateRPCHandler mirrors POST /v1/agent/runs/{runID}/terminate: the RPC
// and both mappings (409 / already-gone 200). The production heal branch also
// journals the terminal event — the deadline must short-circuit before any
// branch, which the blocked test pins.
func terminateRPCHandler(fake *fakeTemporalClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		terminateErr, done := temporalRPC(w, r.Context(), func(ctx context.Context) error {
			return fake.TerminateWorkflow(ctx, "workflow-1", "", "terminated by operator")
		})
		if done {
			return
		}
		if terminateErr != nil {
			if !strings.Contains(terminateErr.Error(), "not found") && !strings.Contains(terminateErr.Error(), "NotFound") {
				writeError(w, http.StatusConflict, terminateErr)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"run_id": "run-1", "status": "already_gone"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"run_id": "run-1", "status": "terminated"})
	}
}

// TestIsTemporalRPCDeadlineAttribution pins the two error shapes R1 counts as
// a timeout: the context error (errors.Is) and the gRPC status the Temporal
// transport actually returns for an expired RPC deadline (codes.DeadlineExceeded,
// wrapped forms included). Every other failure — other gRPC statuses, plain
// errors, nil — is not a timeout.
func TestIsTemporalRPCDeadlineAttribution(t *testing.T) {
	require.True(t, isTemporalRPCDeadline(context.DeadlineExceeded))
	require.True(t, isTemporalRPCDeadline(fmt.Errorf("rpc: %w", context.DeadlineExceeded)))
	require.True(t, isTemporalRPCDeadline(status.Error(codes.DeadlineExceeded, "context deadline exceeded")))
	require.True(t, isTemporalRPCDeadline(fmt.Errorf("start run: %w", status.Error(codes.DeadlineExceeded, "deadline exceeded"))))
	require.False(t, isTemporalRPCDeadline(status.Error(codes.NotFound, "workflow not found")))
	require.False(t, isTemporalRPCDeadline(status.Error(codes.AlreadyExists, "workflow execution already started")))
	require.False(t, isTemporalRPCDeadline(errors.New("connection refused: temporal-frontend:7233")))
	require.False(t, isTemporalRPCDeadline(nil))
}

// TestGRPCDeadlineStatusAnswerMapsTo503: the fake returns a real gRPC status
// error codes.DeadlineExceeded — the shape the Temporal transport surfaces
// for an expired RPC deadline, with no context error anywhere — and the
// handler must still answer R1's 503 "temporal rpc timeout: …".
func TestGRPCDeadlineStatusAnswerMapsTo503(t *testing.T) {
	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "5s")
	server := httptest.NewServer(approvalRPCHandler(&fakeTemporalClient{err: status.Error(codes.DeadlineExceeded, "context deadline exceeded")}))
	t.Cleanup(server.Close)

	code, body, _ := postRPC(t, server, "/v1/agent/runs/run-1/approval")
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, body["error"], "temporal rpc timeout:",
		"a gRPC DeadlineExceeded status must be attributed as R1's timeout, got %v", body["error"])
}

// TestExpiredRPCDeadlineContextMapsTo503 pins temporalRPC's second
// attribution rule: an error that itself does not read as a timeout still
// answers 503 when this RPC's own deadline expired while the call was in
// flight — the deadline is the attributable cause of the refusal.
func TestExpiredRPCDeadlineContextMapsTo503(t *testing.T) {
	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "100ms")
	server := httptest.NewServer(approvalRPCHandler(&fakeTemporalClient{lateErr: errors.New("transport closed while waiting")}))
	t.Cleanup(server.Close)

	code, body, elapsed := postRPC(t, server, "/v1/agent/runs/run-1/approval")
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, body["error"], "temporal rpc timeout:")
	require.Less(t, elapsed, 5*time.Second, "the handler must answer inside the deadline, not hang")
}

// TestNonDeadlineErrorWithLiveDeadlineKeeps409: a failure that arrives while
// the deadline is still alive keeps the route's original mapping — R1 must
// not reshape non-timeout errors (R5), neither plain nor gRPC-status ones.
func TestNonDeadlineErrorWithLiveDeadlineKeeps409(t *testing.T) {
	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "5s")
	for name, failure := range map[string]error{
		"plain error":        errors.New("connection refused: temporal-frontend:7233"),
		"gRPC NotFound":      status.Error(codes.NotFound, "workflow not found"),
		"gRPC AlreadyExists": status.Error(codes.AlreadyExists, "workflow execution already started"),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(approvalRPCHandler(&fakeTemporalClient{err: failure}))
			t.Cleanup(server.Close)
			code, body, _ := postRPC(t, server, "/v1/agent/runs/run-1/approval")
			require.Equal(t, http.StatusConflict, code,
				"a live-deadline non-timeout failure must keep the route's 409, got %v: %v", code, body)
		})
	}
}

// TestBlockedApproveSignalAnswers503WithinDeadline: a wedged approval signal
// breaks at the R1 deadline and answers 503 instead of hanging the handler.
func TestBlockedApproveSignalAnswers503WithinDeadline(t *testing.T) {
	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "100ms")
	server := httptest.NewServer(approvalRPCHandler(&fakeTemporalClient{blocking: true}))
	t.Cleanup(server.Close)

	code, body, elapsed := postRPC(t, server, "/v1/agent/runs/run-1/approval")
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, body["error"], "temporal rpc timeout:")
	require.Less(t, elapsed, 5*time.Second, "the handler must answer inside the deadline, not hang")
}

// TestFastApproveSignalKeepsAcceptedAnswer: a healthy approval signal keeps
// the route's 202 — the deadline only fires for a wedged RPC (R5).
func TestFastApproveSignalKeepsAcceptedAnswer(t *testing.T) {
	server := httptest.NewServer(approvalRPCHandler(&fakeTemporalClient{}))
	t.Cleanup(server.Close)

	code, body, _ := postRPC(t, server, "/v1/agent/runs/run-1/approval")
	require.Equal(t, http.StatusAccepted, code)
	require.Equal(t, true, body["accepted"])
}

// TestBlockedCancelAnswers503WithinDeadline: a wedged cancel breaks at the
// R1 deadline with 503, never the old hang.
func TestBlockedCancelAnswers503WithinDeadline(t *testing.T) {
	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "100ms")
	server := httptest.NewServer(cancelRPCHandler(&fakeTemporalClient{blocking: true}))
	t.Cleanup(server.Close)

	code, body, elapsed := postRPC(t, server, "/v1/agent/runs/run-1/cancel")
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, body["error"], "temporal rpc timeout:")
	require.Less(t, elapsed, 5*time.Second, "the handler must answer inside the deadline, not hang")
}

// TestFastCancelKeepsPreviousAnswer: a healthy cancel keeps the 200
// cancel_requested (R5).
func TestFastCancelKeepsPreviousAnswer(t *testing.T) {
	server := httptest.NewServer(cancelRPCHandler(&fakeTemporalClient{}))
	t.Cleanup(server.Close)

	code, body, _ := postRPC(t, server, "/v1/agent/runs/run-1/cancel")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "cancel_requested", body["status"])
}

// TestBlockedTerminateAnswers503WithinDeadline: a wedged terminate breaks at
// the R1 deadline with 503 and never enters the already-gone heal branch.
func TestBlockedTerminateAnswers503WithinDeadline(t *testing.T) {
	t.Setenv("KERNEL_TEMPORAL_RPC_TIMEOUT", "100ms")
	server := httptest.NewServer(terminateRPCHandler(&fakeTemporalClient{blocking: true}))
	t.Cleanup(server.Close)

	code, body, elapsed := postRPC(t, server, "/v1/agent/runs/run-1/terminate")
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, body["error"], "temporal rpc timeout:")
	require.Less(t, elapsed, 5*time.Second, "the handler must answer inside the deadline, not hang")
}

// TestFastTerminateKeepsPreviousAnswer: a healthy terminate keeps the 200
// terminated (R5); a fast "not found" keeps the heal 200 already_gone.
func TestFastTerminateKeepsPreviousAnswer(t *testing.T) {
	server := httptest.NewServer(terminateRPCHandler(&fakeTemporalClient{err: status.Error(codes.NotFound, "workflow not found")}))
	t.Cleanup(server.Close)
	code, body, _ := postRPC(t, server, "/v1/agent/runs/run-1/terminate")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "already_gone", body["status"])

	server = httptest.NewServer(terminateRPCHandler(&fakeTemporalClient{}))
	t.Cleanup(server.Close)
	code, body, _ = postRPC(t, server, "/v1/agent/runs/run-1/terminate")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "terminated", body["status"])
}

// TestNudgeRouteExactMatch pins the narrowed wiring match: only
// POST /v1/agent/runs/{runID}/nudge (non-empty runID, exactly five path
// segments) is wrapped by nudgeWithRPCDeadline — …/nudgeX and …/runs//nudge
// are not, so the 409→503 rewrite can never fire for a neighbour route.
func TestNudgeRouteExactMatch(t *testing.T) {
	matches := func(method, path string) bool {
		return matchesNudgeRPCRoute(httptest.NewRequest(method, path, nil))
	}
	require.True(t, matches(http.MethodPost, "/v1/agent/runs/abc/nudge"))
	// ServeMux segments the escaped path: a run id carrying %2F is one segment.
	require.True(t, matches(http.MethodPost, "/v1/agent/runs/a%2Fb/nudge"))
	require.False(t, matches(http.MethodPost, "/v1/agent/runs/abc/nudgeX"))
	require.False(t, matches(http.MethodPost, "/v1/agent/runs//nudge"))
	require.False(t, matches(http.MethodPost, "/v1/agent/runs/abc/nudge/"))
	require.False(t, matches(http.MethodPost, "/v1/agent/runs/abc/extra/nudge"))
	require.False(t, matches(http.MethodPost, "/v1/workspace/runs/abc/nudge"))
	require.False(t, matches(http.MethodGet, "/v1/agent/runs/abc/nudge"))
}
