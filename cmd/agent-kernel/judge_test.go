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

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// fakeJudgeStore satisfies the judge endpoints' storage surface without a
// database; the saved calibrations are captured for assertions
// (specs/quality-loop.md §6.4, §8.6, M4).
type fakeJudgeStore struct {
	mu sync.Mutex

	agent    workspace.Agent
	config   workspace.JudgeConfig
	set      []workspace.JudgeCalibrationCase
	agentErr error
	setErr   error

	saved []workspace.JudgeCalibration
}

func (f *fakeJudgeStore) GetAgent(_ context.Context, _ string) (workspace.Agent, error) {
	return f.agent, f.agentErr
}

func (f *fakeJudgeStore) JudgeConfig(_ context.Context) (workspace.JudgeConfig, error) {
	return f.config, nil
}

func (f *fakeJudgeStore) AgentJudgeCalibrationSet(_ context.Context, _ string, _ int) ([]workspace.JudgeCalibrationCase, error) {
	return f.set, f.setErr
}

func (f *fakeJudgeStore) SaveJudgeCalibration(_ context.Context, calibration *workspace.JudgeCalibration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, *calibration)
	return nil
}

// fakeJudgeActivities presets the kernel calibration outcome per test.
type fakeJudgeActivities struct {
	outcome agent.JudgeCalibrationOutcome
	err     error
}

func (f *fakeJudgeActivities) CalibrateJudge(_ context.Context, _ []agent.JudgeCalibrationCase) (agent.JudgeCalibrationOutcome, error) {
	return f.outcome, f.err
}

// newJudgeServer mounts the judge endpoints the way main wires them — real
// RBAC gate, fake store/activities/outbox.
func newJudgeServer(t *testing.T, store *fakeJudgeStore, activities judgeActivities, events judgeOutbox) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterJudgeRoutes(mux, gate, store, activities, events,
		func(*http.Request) []string { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return httptest.NewServer(gate.Authenticate(mux))
}

func judgeRequest(t *testing.T, server *httptest.Server, method, path, token string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(""))
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

func defaultJudgeFixture() (*fakeJudgeStore, *fakeJudgeActivities, *captureEvalGateOutbox) {
	store := &fakeJudgeStore{
		agent:  workspace.Agent{ID: "agent-a", Name: "Agent A"},
		config: workspace.JudgeConfig{Model: "judge-x", ProviderID: "prov", Configured: true},
		set: []workspace.JudgeCalibrationCase{
			{Name: "c1", Input: "in", Answer: "ans", Label: true, LabelSource: "patterns"},
		},
	}
	activities := &fakeJudgeActivities{}
	return store, activities, &captureEvalGateOutbox{}
}

// happy path: agreement over the floor is stored with its history and the
// agent.evaluation.judge.calibrated event carries agreement_rate (§6.4, §9).
func TestJudgeCalibrateHappyPathStoresHistoryAndEmits(t *testing.T) {
	store, activities, outbox := defaultJudgeFixture()
	activities.outcome = agent.JudgeCalibrationOutcome{
		JudgeModel: "judge-x", CasesChecked: 5, CasesSkipped: 0,
		Scores: []agent.CalibrationScore{
			{Name: "c1", Label: true, Score: 90, Match: true},
			{Name: "c2", Label: true, Score: 80, Match: true},
			{Name: "c3", Label: false, Score: 15, Match: true},
			{Name: "c4", Label: false, Score: 30, Match: true},
			{Name: "c5", Label: true, Score: 85, Match: true},
		},
	}
	server := newJudgeServer(t, store, activities, outbox)
	defer server.Close()

	status, body := judgeRequest(t, server, http.MethodPost,
		"/v1/workspace/agents/agent-a/evaluation-judge/calibrate", "writertoken")
	require.Equal(t, 200, status)

	var reply struct {
		JudgeModel       string  `json:"judge_model"`
		AgreementRate    float64 `json:"agreement_rate"`
		MeanAbsDeviation float64 `json:"mean_abs_deviation"`
		Degraded         bool    `json:"degraded"`
		CasesChecked     int     `json:"cases_checked"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &reply))
	require.Equal(t, "judge-x", reply.JudgeModel)
	require.InDelta(t, 1.0, reply.AgreementRate, 1e-9)
	require.False(t, reply.Degraded)
	require.Equal(t, 5, reply.CasesChecked)

	// The history row is persisted (§6.4: agreement, deviation, histogram,
	// snapshot) and is the source of truth for the degraded flag.
	require.Len(t, store.saved, 1)
	saved := store.saved[0]
	require.Equal(t, "agent-a", saved.AgentID)
	require.Equal(t, "judge-x", saved.JudgeModel)
	require.InDelta(t, 1.0, saved.AgreementRate, 1e-9)
	require.False(t, saved.Degraded)
	require.Len(t, saved.CasesSnapshot, 5)
	require.Len(t, saved.Histogram, 10)

	events := outbox.byType("agent.evaluation.judge.calibrated")
	require.Len(t, events, 1)
	require.Equal(t, "agent-a", events[0].Data["agent_id"])
	require.Equal(t, "judge-x", events[0].Data["judge_model"])
	require.InDelta(t, 1.0, events[0].Data["agreement_rate"], 1e-9)
	require.Equal(t, false, events[0].Data["degraded"])
}

// agreement under the 0.7 floor stores the calibration degraded — the gate
// then refuses judge runs with cause judge_degraded (§6.4).
func TestJudgeCalibrateAgreementBelowFloorIsDegraded(t *testing.T) {
	store, activities, outbox := defaultJudgeFixture()
	activities.outcome = agent.JudgeCalibrationOutcome{
		JudgeModel: "judge-x", CasesChecked: 5,
		Scores: []agent.CalibrationScore{
			{Name: "c1", Label: true, Score: 90, Match: true},
			{Name: "c2", Label: true, Score: 80, Match: true},
			{Name: "c3", Label: false, Score: 10, Match: true},
			{Name: "c4", Label: true, Score: 20, Match: false},
			{Name: "c5", Label: false, Score: 70, Match: false},
		},
	}
	server := newJudgeServer(t, store, activities, outbox)
	defer server.Close()

	status, body := judgeRequest(t, server, http.MethodPost,
		"/v1/workspace/agents/agent-a/evaluation-judge/calibrate", "writertoken")
	require.Equal(t, 200, status)

	var reply struct {
		AgreementRate float64 `json:"agreement_rate"`
		Degraded      bool    `json:"degraded"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &reply))
	require.InDelta(t, 0.6, reply.AgreementRate, 1e-9)
	require.True(t, reply.Degraded)

	require.Len(t, store.saved, 1)
	require.True(t, store.saved[0].Degraded)
	require.InDelta(t, 0.6, store.saved[0].AgreementRate, 1e-9)

	events := outbox.byType("agent.evaluation.judge.calibrated")
	require.Len(t, events, 1)
	require.Equal(t, true, events[0].Data["degraded"])
}

// calibration is a writer action (§6.4) — a reader is refused by RBAC
// before anything is judged or stored.
func TestJudgeCalibrateReaderForbidden(t *testing.T) {
	store, activities, outbox := defaultJudgeFixture()
	server := newJudgeServer(t, store, activities, outbox)
	defer server.Close()

	status, _ := judgeRequest(t, server, http.MethodPost,
		"/v1/workspace/agents/agent-a/evaluation-judge/calibrate", "readertoken")
	require.Equal(t, http.StatusForbidden, status)
	require.Empty(t, store.saved)
}

// the configuration endpoint reports the kernel's judge state (§6.2): the
// model resolution a judge-less installation turns into configured=false.
func TestJudgeConfigEndpointReportsUnconfigured(t *testing.T) {
	store, activities, outbox := defaultJudgeFixture()
	store.config = workspace.JudgeConfig{Model: "judge-x", Configured: false}
	server := newJudgeServer(t, store, activities, outbox)
	defer server.Close()

	status, body := judgeRequest(t, server, http.MethodGet,
		"/v1/workspace/evaluation-judge", "readertoken")
	require.Equal(t, 200, status)

	var config workspace.JudgeConfig
	require.NoError(t, json.Unmarshal([]byte(body), &config))
	require.Equal(t, "judge-x", config.Model)
	require.False(t, config.Configured)
}
