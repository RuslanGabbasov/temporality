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
type fakeTemporalClient struct {
	blocking bool
}

func (f *fakeTemporalClient) ExecuteWorkflow(ctx context.Context, _ client.StartWorkflowOptions, _ string, _ ...any) (client.WorkflowRun, error) {
	if f.blocking {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return fakeWorkflowRun{id: "workflow-1", runID: "temporal-run-1"}, nil
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
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/agent/runs/") && strings.HasSuffix(r.URL.Path, "/nudge") {
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
