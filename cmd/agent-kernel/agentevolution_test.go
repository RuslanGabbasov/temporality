package main

// Evolution v2 route tests (docs/agent-evolution.md): apply without a gate,
// the §5 metrics wire shape, observation validation and the pure drafting
// helpers. The heavy LLM paths (from-text drafting, smoke runs) are exercised
// through their pure parts here; their model calls share the wizard chain.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

// errNoModel is the wizard-chain failure the model-less tests simulate.
var errNoModel = errors.New("no model configured")

// fakeEvolutionStore captures what the evolution routes do; everything the
// tests do not assert on is a stub.
type fakeEvolutionStore struct {
	agent     workspace.Agent
	agentErr  error
	proposal  workspace.AgentProposal
	versions  []workspace.AgentVersion
	metrics   []workspace.AgentVersionMetrics
	obs       workspace.ObservationConfig
	cases     []workspace.AgentCheckCase
	applied   *workspace.AgentVersion
	appliedBy string
	obsSet    *workspace.ObservationConfig
	deletedID int64
	addedCase *workspace.AgentCheckCase
}

func (f *fakeEvolutionStore) GetAgent(_ context.Context, _ string) (workspace.Agent, error) {
	return f.agent, f.agentErr
}
func (f *fakeEvolutionStore) ListAgentVersions(_ context.Context, _ string) ([]workspace.AgentVersion, error) {
	return f.versions, nil
}
func (f *fakeEvolutionStore) GetAgentVersion(_ context.Context, _ string, version int) (workspace.AgentVersion, error) {
	for _, v := range f.versions {
		if v.Version == version {
			return v, nil
		}
	}
	return workspace.AgentVersion{}, workspace.ErrNotFound
}
func (f *fakeEvolutionStore) AgentVersionsDiff(_ context.Context, _ string, from, to int) (workspace.VersionDiff, error) {
	return workspace.VersionDiff{From: from, To: to}, nil
}
func (f *fakeEvolutionStore) InsertAgentVersion(_ context.Context, v workspace.AgentVersion) error {
	f.versions = append(f.versions, v)
	return nil
}
func (f *fakeEvolutionStore) RollbackAgentVersion(_ context.Context, _ string, toVersion int, author string) (workspace.AgentVersion, error) {
	v := f.versions[len(f.versions)-1]
	v.Version++
	v.Kind = "rollback"
	v.RollbackOf = toVersion
	v.Author = author
	f.versions = append(f.versions, v)
	return v, nil
}
func (f *fakeEvolutionStore) CreateAgentProposal(_ context.Context, p *workspace.AgentProposal) error {
	p.ID = 7
	p.CreatedAt = time.Now()
	f.proposal = *p
	return nil
}
func (f *fakeEvolutionStore) ListAgentProposals(_ context.Context, _, _ string, _ int) ([]workspace.AgentProposal, error) {
	return []workspace.AgentProposal{f.proposal}, nil
}
func (f *fakeEvolutionStore) GetAgentProposal(_ context.Context, _ int64) (workspace.AgentProposal, error) {
	return f.proposal, nil
}
func (f *fakeEvolutionStore) ApplyAgentProposal(_ context.Context, next workspace.Agent, _ int64, decidedBy string, snapshot *workspace.AgentVersion) (workspace.AgentProposal, error) {
	f.applied = snapshot
	f.appliedBy = decidedBy
	f.agent = next
	decided := f.proposal
	decided.Status = "applied"
	if snapshot != nil {
		decided.AppliedVersion = snapshot.Version
	}
	return decided, nil
}
func (f *fakeEvolutionStore) DecideAgentProposal(_ context.Context, _ int64, status, _ string) (workspace.AgentProposal, error) {
	f.proposal.Status = status
	return f.proposal, nil
}
func (f *fakeEvolutionStore) ListAgentVersionMetrics(_ context.Context, _ string, version int, _ time.Time) ([]workspace.AgentVersionMetrics, error) {
	var rows []workspace.AgentVersionMetrics
	for _, m := range f.metrics {
		if m.Version == version {
			rows = append(rows, m)
		}
	}
	return rows, nil
}
func (f *fakeEvolutionStore) AgentObservation(_ context.Context, _ string) (workspace.ObservationConfig, error) {
	if f.obs == (workspace.ObservationConfig{}) {
		return workspace.ObservationConfig{Days: 7, MinRuns: 10}, nil
	}
	return f.obs, nil
}
func (f *fakeEvolutionStore) SetAgentObservation(_ context.Context, _ string, cfg workspace.ObservationConfig) error {
	if cfg.Days < 1 || cfg.Days > 90 || cfg.MinRuns < 1 || cfg.MinRuns > 100 {
		return workspace.ErrConflict
	}
	f.obs = cfg
	f.obsSet = &cfg
	return nil
}
func (f *fakeEvolutionStore) ListAgentCheckCases(_ context.Context, _ string) ([]workspace.AgentCheckCase, error) {
	return f.cases, nil
}
func (f *fakeEvolutionStore) AddAgentCheckCase(_ context.Context, c *workspace.AgentCheckCase) error {
	c.ID = int64(len(f.cases) + 1)
	f.cases = append(f.cases, *c)
	f.addedCase = c
	return nil
}
func (f *fakeEvolutionStore) DeleteAgentCheckCase(_ context.Context, _ string, id int64) error {
	f.deletedID = id
	return nil
}

// newEvolutionServer mounts the evolution routes the way main wires them.
func newEvolutionServer(t *testing.T, store *fakeEvolutionStore) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterAgentEvolutionRoutes(mux, gate, store,
		func(http.ResponseWriter, *http.Request) *llm.Client { return nil }, // no model in these tests
		func(*http.Request) []string { return nil }, "http://journal.invalid", func() string { return "tok" },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return httptest.NewServer(gate.Authenticate(mux))
}

func evolutionRequest(t *testing.T, server *httptest.Server, method, path, token, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
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

// The apply endpoint applies a proposal unconditionally — no gate state, no
// suite readiness, no judge: one click, 201 with the new version.
func TestEvolutionApplyNeedsNoGate(t *testing.T) {
	definition := workspace.AgentDefinition{Constraints: []string{"no self-modification"}}
	store := &fakeEvolutionStore{
		agent: workspace.Agent{ID: "agent-a", Name: "Agent A", Description: "does things", DefinitionVersion: 3, Definition: &definition},
		proposal: workspace.AgentProposal{
			ID: 7, AgentID: "agent-a", BaseVersion: 3,
			Definition:    workspace.AgentDefinition{Constraints: []string{"no self-modification", "no docker"}},
			Problem:       "agent runs docker unprompted",
			Change:        "forbid docker without a request",
			ChangeSummary: "Агент перестанет сам запускать docker-команды.",
			Source:        "user", Status: "pending",
		},
		versions: []workspace.AgentVersion{{AgentID: "agent-a", Version: 3, Definition: definition, CreatedAt: time.Now().Add(-24 * time.Hour)}},
	}
	server := newEvolutionServer(t, store)
	defer server.Close()

	status, body := evolutionRequest(t, server, http.MethodPost, "/v1/workspace/agents/agent-a/proposals/7/apply", "writertoken", "")
	require.Equal(t, 201, status)

	var response struct {
		Version int `json:"version"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	require.Equal(t, 4, response.Version)
	require.NotNil(t, store.applied, "the snapshot must land with the apply")
	require.Equal(t, "Агент перестанет сам запускать docker-команды.", store.applied.ChangeSummary)
}

// The metrics endpoint speaks the docs/agent-evolution.md §5 wire shape:
// observation + current/baseline + delta with signal marks + daily rows.
func TestEvolutionMetricsWireShape(t *testing.T) {
	definition := workspace.AgentDefinition{}
	yesterday := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	store := &fakeEvolutionStore{
		agent: workspace.Agent{ID: "agent-a", Name: "Agent A", DefinitionVersion: 4, ObservationDays: 7, Definition: &definition},
		versions: []workspace.AgentVersion{
			{AgentID: "agent-a", Version: 4, Definition: definition, BaselineVersion: 3, CreatedAt: yesterday},
			{AgentID: "agent-a", Version: 3, Definition: definition, CreatedAt: yesterday.Add(-72 * time.Hour)},
		},
		metrics: []workspace.AgentVersionMetrics{
			{AgentID: "agent-a", Version: 4, Day: yesterday, Runs: 12, Succeeded: 10, VerdictAccepted: 8, VerdictCorrected: 3, VerdictRejected: 1},
			{AgentID: "agent-a", Version: 3, Day: yesterday.Add(-24 * time.Hour), Runs: 11, Succeeded: 10, VerdictAccepted: 9, VerdictCorrected: 2},
		},
	}
	server := newEvolutionServer(t, store)
	defer server.Close()

	status, body := evolutionRequest(t, server, http.MethodGet, "/v1/workspace/agents/agent-a/versions/4/metrics", "readertoken", "")
	require.Equal(t, 200, status)

	var payload struct {
		Observation struct {
			State   string `json:"state"`
			Verdict any    `json:"verdict"`
			Runs    int    `json:"runs"`
		} `json:"observation"`
		Current struct {
			Runs     int `json:"runs"`
			Verdicts struct {
				Accepted  int `json:"accepted"`
				Corrected int `json:"corrected"`
			} `json:"verdicts"`
		} `json:"current"`
		Baseline struct {
			Runs int `json:"runs"`
		} `json:"baseline"`
		Delta []struct {
			Metric string `json:"metric"`
			Signal string `json:"signal"`
		} `json:"delta"`
		Daily []map[string]any `json:"daily"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	require.Equal(t, 12, payload.Current.Runs)
	require.Equal(t, 3, payload.Current.Verdicts.Corrected)
	require.Equal(t, 11, payload.Baseline.Runs)
	require.Len(t, payload.Daily, 1)
	require.NotEmpty(t, payload.Delta)
	for _, d := range payload.Delta {
		require.Contains(t, []string{"signal", "noise"}, d.Signal, "delta marks are signal|noise strings")
	}
}

// Observation settings are validated: days 1..90, min runs 1..100.
func TestEvolutionObservationValidation(t *testing.T) {
	definition := workspace.AgentDefinition{}
	store := &fakeEvolutionStore{agent: workspace.Agent{ID: "agent-a", Name: "Agent A", Definition: &definition}}
	server := newEvolutionServer(t, store)
	defer server.Close()

	status, _ := evolutionRequest(t, server, http.MethodPut, "/v1/workspace/agents/agent-a/observation", "writertoken", `{"days": 3, "min_runs": 5}`)
	require.Equal(t, 200, status)
	require.NotNil(t, store.obsSet)
	require.Equal(t, 3, store.obsSet.Days)

	status, _ = evolutionRequest(t, server, http.MethodPut, "/v1/workspace/agents/agent-a/observation", "writertoken", `{"days": 0, "min_runs": 5}`)
	require.Equal(t, 422, status)
}

// from-text and the smoke run need a model: without one the wizard chain
// answers 502 and nothing is written.
func TestEvolutionFromTextNeedsModel(t *testing.T) {
	definition := workspace.AgentDefinition{}
	store := &fakeEvolutionStore{agent: workspace.Agent{ID: "agent-a", Name: "Agent A", Definition: &definition}}
	gate, err := controlplane.NewGate("writertoken:writer:writer:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterAgentEvolutionRoutes(mux, gate, store,
		func(w http.ResponseWriter, _ *http.Request) *llm.Client {
			writeError(w, 502, errNoModel)
			return nil
		},
		func(*http.Request) []string { return nil }, "http://journal.invalid", func() string { return "tok" },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(gate.Authenticate(mux))
	defer server.Close()

	status, _ := evolutionRequest(t, server, http.MethodPost, "/v1/workspace/agents/agent-a/proposals/from-text", "writertoken", `{"problem":"агент делает X"}`)
	require.Equal(t, 502, status)
	require.Equal(t, workspace.AgentProposal{}, store.proposal, "nothing must be written without a draft")
}

// extractFirstJSONObject tolerates prose around the JSON and rejects garbage.
func TestExtractFirstJSONObject(t *testing.T) {
	payload, err := extractFirstJSONObject(`Here you go: {"a": {"b": "c}"}, "d": 1} hope it helps`)
	require.NoError(t, err)
	require.JSONEq(t, `{"a": {"b": "c}"}, "d": 1}`, payload)

	// Escaped quotes and backslashes inside strings must not break balance.
	payload, err = extractFirstJSONObject(`prefix {"change": "never say \"hi\" to users", "path": "C:\\tmp"} suffix`)
	require.NoError(t, err)
	require.JSONEq(t, `{"change": "never say \"hi\" to users", "path": "C:\\tmp"}`, payload)

	// ```json fences are tolerated (the object inside is found).
	payload, err = extractFirstJSONObject("```json\n{\"change\": \"x\"}\n```")
	require.NoError(t, err)
	require.JSONEq(t, `{"change": "x"}`, payload)

	_, err = extractFirstJSONObject("no json here")
	require.Error(t, err)
	_, err = extractFirstJSONObject(`{"unbalanced": {`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "truncated", "the unbalanced error must hint at truncation")
}

// A broken first answer (the routine failure: truncation, unescaped quotes)
// is recovered by one corrective retry — the route never surfaces a raw
// "unbalanced JSON" 502 to the user while one retry can still save it.
func TestDraftAgentProposalRetriesBrokenJSON(t *testing.T) {
	calls := 0
	var sawMaxTokens float64
	var repairPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if mt, ok := body["max_tokens"].(float64); ok {
			sawMaxTokens = mt
		}
		content := `Here is my take: {"problem": "agent x", "change": "never say "hi""` // broken on purpose
		if calls >= 2 {
			if msgs, ok := body["messages"].([]any); ok && len(msgs) >= 2 {
				last := msgs[len(msgs)-1].(map[string]any)
				repairPrompt, _ = last["content"].(string)
			}
			content = `{"problem":"p","change":"c","effect":"e","change_summary":"s","constraints":[],"completion":[]}`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}}})
	}))
	defer server.Close()

	model := llm.New(llm.Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	agent := workspace.Agent{ID: "agent-a", Name: "Agent A", Description: "d"}
	draft, err := draftAgentProposal(context.Background(), model, agent, "the problem")
	require.NoError(t, err, "one broken first answer must be recovered by the retry")
	require.Equal(t, 2, calls, "exactly one corrective round")
	require.Equal(t, "c", draft.Change)
	require.Equal(t, float64(4096), sawMaxTokens, "the headroom must cover a verbose answer")
	require.Contains(t, repairPrompt, "not one complete JSON object", "the corrective round must tell the model what was wrong")
}

// Both attempts broken → the 502 text says a corrective retry happened.
func TestDraftAgentProposalRetryExhaustedIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"no json at all"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	model := llm.New(llm.Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	agent := workspace.Agent{ID: "agent-a", Name: "Agent A", Description: "d"}
	_, err := draftAgentProposal(context.Background(), model, agent, "the problem")
	require.Error(t, err)
	require.Contains(t, err.Error(), "corrective retry")
}
