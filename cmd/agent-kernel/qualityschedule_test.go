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
	"github.com/temporality-project/temporality/workspace"
)

// fakeScheduleStore satisfies the schedule surfaces without a database.
type fakeScheduleStore struct {
	mu sync.Mutex

	interval time.Duration
	intErr   error
	last     time.Time
	lastErr  error
	ids      []string
	idsErr   error

	setKey   string
	setValue string
}

func (f *fakeScheduleStore) EvalScheduleInterval(context.Context) (time.Duration, error) {
	return f.interval, f.intErr
}

func (f *fakeScheduleStore) SetSetting(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setKey, f.setValue = key, value
	return nil
}

func (f *fakeScheduleStore) LatestScheduledEvalRun(context.Context) (time.Time, error) {
	return f.last, f.lastErr
}

func (f *fakeScheduleStore) ListEvalSuiteAgentIDs(context.Context) ([]string, error) {
	return f.ids, f.idsErr
}

// fakeScheduleRunner captures the scheduled evaluation calls.
type fakeScheduleRunner struct {
	mu     sync.Mutex
	calls  []string
	result map[string]error
}

func (f *fakeScheduleRunner) RunAgentEvaluationMeta(_ context.Context, agentID string, version int, meta agent.EvalRunMeta) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, agentID)
	if f.result == nil {
		return "ok", nil
	}
	return "ok", f.result[agentID]
}

// TestScheduleDue pins the loop's firing rule: zero anchor fires at once,
// elapsed interval fires, pending anchor and disabled schedule do not.
func TestScheduleDue(t *testing.T) {
	now := time.Now()
	require.True(t, scheduleDue(time.Time{}, 24*time.Hour, now), "never ran fires immediately")
	require.True(t, scheduleDue(now.Add(-25*time.Hour), 24*time.Hour, now), "elapsed interval fires")
	require.False(t, scheduleDue(now.Add(-3*time.Hour), 24*time.Hour, now), "pending anchor waits")
	require.False(t, scheduleDue(now.Add(-48*time.Hour), 0, now), "disabled schedule never fires")
}

// TestRunScheduledEvaluations: every suite-bearing agent runs once with
// trigger=schedule against the current version, and one agent's failure does
// not stop the rest.
func TestRunScheduledEvaluations(t *testing.T) {
	store := &fakeScheduleStore{ids: []string{"a1", "a2", "a3"}}
	runner := &fakeScheduleRunner{result: map[string]error{"a2": context.DeadlineExceeded}}
	runScheduledEvaluations(context.Background(), store, runner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Equal(t, []string{"a1", "a2", "a3"}, runner.calls)
}

// TestQualityScheduleEndpoints: GET reflects the store, PUT validates the
// interval (negative is 422) and stores the hour string, and a reader may
// read but not write.
func TestQualityScheduleEndpoints(t *testing.T) {
	store := &fakeScheduleStore{interval: 24 * time.Hour, last: time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)}
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\noperatortoken:operator:operator:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	registerQualityScheduleRoutes(mux, gate, store, nil)
	server := httptest.NewServer(gate.Authenticate(mux))
	defer server.Close()

	status, body := judgeRequest(t, server, http.MethodGet, "/v1/workspace/quality-schedule", "readertoken")
	require.Equal(t, 200, status)
	var payload struct {
		IntervalHours int    `json:"interval_hours"`
		Enabled       bool   `json:"enabled"`
		NextRunAt     string `json:"next_run_at"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	require.Equal(t, 24, payload.IntervalHours)
	require.True(t, payload.Enabled)
	require.Equal(t, "2026-10-15T06:00:00Z", payload.NextRunAt)

	status, _ = judgeRequest(t, server, http.MethodPut, "/v1/workspace/quality-schedule", "readertoken")
	require.Equal(t, 403, status, "reader may not change the schedule")

	putStatus, _, err := judgePut(t, server, "/v1/workspace/quality-schedule", `{"interval_hours":-1}`, "operatortoken")
	require.NoError(t, err)
	require.Equal(t, 422, putStatus)

	putStatus, _, err = judgePut(t, server, "/v1/workspace/quality-schedule", `{"interval_hours":12}`, "operatortoken")
	require.NoError(t, err)
	require.Equal(t, 200, putStatus)
	require.Equal(t, "eval.schedule.interval", store.setKey)
	require.Equal(t, "12h", store.setValue)
}

func judgePut(t *testing.T, server *httptest.Server, path, body, token string) (int, string, error) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPut, server.URL+path, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(request)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), err
}

// writableJudgeStore layers the settings surface onto the read-only fake so
// the PUT route registers: saved settings and provider resolution recorded.
type writableJudgeStore struct {
	fakeJudgeStore
	mu sync.Mutex

	settings  map[string]string
	providers map[string]string // model -> provider id
}

func (w *writableJudgeStore) SetSetting(_ context.Context, key, value string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.settings == nil {
		w.settings = map[string]string{}
	}
	w.settings[key] = value
	return nil
}

func (w *writableJudgeStore) ProviderOfModel(_ context.Context, model string) (string, bool, error) {
	id, ok := w.providers[model]
	return id, ok, nil
}

// JudgeConfig resolves the same way the real store does after a PUT: the
// stored setting wins, validated against the provider catalog.
func (w *writableJudgeStore) JudgeConfig(_ context.Context) (workspace.JudgeConfig, error) {
	w.mu.Lock()
	model := w.settings[workspace.SettingJudgeModel]
	w.mu.Unlock()
	model = strings.TrimSpace(model)
	if model == "" {
		return workspace.JudgeConfig{}, nil
	}
	if id, ok := w.providers[model]; ok {
		return workspace.JudgeConfig{Model: model, ProviderID: id, Configured: true, Source: "setting"}, nil
	}
	return workspace.JudgeConfig{Model: model, Source: "setting"}, nil
}

// TestJudgePutConfig: a model from a configured provider saves and re-reads
// through the settings precedence; an unknown model is a 422 and saves
// nothing; the off choice (empty model) is stored too — it must override a
// non-empty env default.
func TestJudgePutConfig(t *testing.T) {
	store := &writableJudgeStore{providers: map[string]string{"glm-5.2": "prov-z"}}
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\noperatortoken:operator:operator:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterJudgeRoutes(mux, gate, store, &fakeJudgeActivities{}, nil,
		func(*http.Request) []string { return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(gate.Authenticate(mux))
	defer server.Close()

	status, _ := judgeRequest(t, server, http.MethodPut, "/v1/workspace/evaluation-judge", "readertoken")
	require.Equal(t, 403, status, "reader may not change the judge")

	status, _, err = judgePut(t, server, "/v1/workspace/evaluation-judge", `{"model":"no-such-model"}`, "operatortoken")
	require.NoError(t, err)
	require.Equal(t, 422, status)
	require.Empty(t, store.settings, "a rejected model must not be stored")

	status, body, err := judgePut(t, server, "/v1/workspace/evaluation-judge", `{"model":"glm-5.2"}`, "operatortoken")
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Equal(t, "glm-5.2", store.settings[workspace.SettingJudgeModel])
	require.Contains(t, body, `"configured":true`)

	status, _, err = judgePut(t, server, "/v1/workspace/evaluation-judge", `{"model":""}`, "operatortoken")
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Equal(t, "", store.settings[workspace.SettingJudgeModel], "explicit off must be stored, not dropped")
}
