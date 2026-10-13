package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// fakeQualityStore satisfies the qualityStore interface without a database;
// counters let the tests assert how far a request got.
type fakeQualityStore struct {
	agent       workspace.Agent
	report      workspace.QualityReport
	diff        workspace.VersionDiff
	agentErr    error
	reportErr   error
	diffErr     error
	agentCalls  int
	reportCalls int
	diffCalls   int
}

func (f *fakeQualityStore) GetAgent(_ context.Context, _ string) (workspace.Agent, error) {
	f.agentCalls++
	return f.agent, f.agentErr
}

func (f *fakeQualityStore) AgentQualityReport(_ context.Context, _ string) (workspace.QualityReport, error) {
	f.reportCalls++
	return f.report, f.reportErr
}

func (f *fakeQualityStore) AgentVersionsDiff(_ context.Context, _ string, _, _ int) (workspace.VersionDiff, error) {
	f.diffCalls++
	return f.diff, f.diffErr
}

type captureQualityOutbox struct {
	events []observation.Event
}

func (c *captureQualityOutbox) Enqueue(_ context.Context, event observation.Event) error {
	c.events = append(c.events, event)
	return nil
}

func newQualityServer(t *testing.T, store qualityStore, outbox qualityOutbox, units []string) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterM5Routes(mux, gate, store, outbox, func(*http.Request) []string { return units })
	return httptest.NewServer(gate.Authenticate(mux))
}

func qualityGet(t *testing.T, server *httptest.Server, path, token string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(body)
}

// TestQualityEndpointRBACAndViewEvent: §10.3 keeps the aggregate at writer
// (a reader is stopped by the gate before storage), a served view answers
// the §8.4 payload and emits agent.quality.dashboard_viewed (§9) with unique
// event ids per view.
func TestQualityEndpointRBACAndViewEvent(t *testing.T) {
	store := &fakeQualityStore{
		agent: workspace.Agent{ID: "agent-a", ProjectID: "proj", OrgUnitID: "unit-a"},
		report: workspace.QualityReport{AgentID: "agent-a", Versions: []workspace.QualityVersion{
			{Version: 4, Runs: 120, Confirmed: 96, Corrected: 18, HasData: true},
		}},
	}
	outbox := &captureQualityOutbox{}
	server := newQualityServer(t, store, outbox, []string{"unit-a"})
	defer server.Close()

	status, body := qualityGet(t, server, "/v1/workspace/agents/agent-a/quality", "readertoken")
	require.Equal(t, http.StatusForbidden, status)
	require.Contains(t, body, "need writer")
	require.Zero(t, store.agentCalls, "the gate must stop a reader before storage")
	require.Empty(t, outbox.events)

	status, body = qualityGet(t, server, "/v1/workspace/agents/agent-a/quality", "writertoken")
	require.Equal(t, http.StatusOK, status)
	var report workspace.QualityReport
	require.NoError(t, json.Unmarshal([]byte(body), &report))
	require.Equal(t, "agent-a", report.AgentID)
	require.Len(t, report.Versions, 1)
	require.Equal(t, 4, report.Versions[0].Version)
	require.Equal(t, 120, report.Versions[0].Runs)
	require.True(t, report.Versions[0].HasData)

	status, _ = qualityGet(t, server, "/v1/workspace/agents/agent-a/quality", "writertoken")
	require.Equal(t, http.StatusOK, status)
	require.Len(t, outbox.events, 2)
	first, second := outbox.events[0], outbox.events[1]
	require.Equal(t, "agent.quality.dashboard_viewed", first.Type)
	require.Equal(t, "agent-a", first.Data["agent_id"])
	require.Equal(t, "proj", first.Context.Project)
	require.Equal(t, "human", first.Context.Actor.Type)
	require.NotEqual(t, first.EventID, second.EventID)
}

// TestQualityEndpointErrors: §8 error mapping — unknown agent and org-hidden
// agent read as 404, a storage failure as 500; neither case emits the §9
// event because no dashboard was served.
func TestQualityEndpointErrors(t *testing.T) {
	outbox := &captureQualityOutbox{}

	missing := &fakeQualityStore{agentErr: workspace.ErrNotFound}
	server := newQualityServer(t, missing, outbox, nil)
	status, body := qualityGet(t, server, "/v1/workspace/agents/nope/quality", "writertoken")
	require.Equal(t, http.StatusNotFound, status)
	require.Contains(t, body, "agent not found")
	require.Zero(t, missing.reportCalls)
	require.Empty(t, outbox.events)
	server.Close()

	hidden := &fakeQualityStore{
		agent:  workspace.Agent{ID: "agent-a", OrgUnitID: "unit-a"},
		report: workspace.QualityReport{AgentID: "agent-a", Versions: []workspace.QualityVersion{}},
	}
	server = newQualityServer(t, hidden, outbox, []string{"unit-b"})
	status, _ = qualityGet(t, server, "/v1/workspace/agents/agent-a/quality", "writertoken")
	require.Equal(t, http.StatusNotFound, status)
	require.Zero(t, hidden.reportCalls, "org-hidden agents must not reach storage aggregation")
	require.Empty(t, outbox.events)
	server.Close()

	broken := &fakeQualityStore{
		agent:     workspace.Agent{ID: "agent-a"},
		reportErr: errors.New("boom"),
	}
	server = newQualityServer(t, broken, outbox, nil)
	status, body = qualityGet(t, server, "/v1/workspace/agents/agent-a/quality", "writertoken")
	require.Equal(t, http.StatusInternalServerError, status)
	require.Contains(t, body, "boom")
	require.Empty(t, outbox.events)
	server.Close()
}

// TestVersionDiffValidatesRange: missing, unparsable and equal from/to are
// 400s before any storage is touched (§8.5).
func TestVersionDiffValidatesRange(t *testing.T) {
	store := &fakeQualityStore{agent: workspace.Agent{ID: "agent-a"}}
	server := newQualityServer(t, store, &captureQualityOutbox{}, nil)
	defer server.Close()
	base := "/v1/workspace/agents/agent-a/versions/diff"

	status, body := qualityGet(t, server, base, "readertoken")
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, body, "from and to query parameters")

	status, body = qualityGet(t, server, base+"?from=2&to=2", "readertoken")
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, body, "different versions")

	status, _ = qualityGet(t, server, base+"?from=abc&to=3", "readertoken")
	require.Equal(t, http.StatusBadRequest, status)

	require.Zero(t, store.agentCalls, "invalid ranges must not reach storage")
	require.Zero(t, store.diffCalls)
}

// TestVersionDiffErrorsAndSuccess: a missing snapshot/compiled prompt is 422
// (§8.5), a reader may read a well-formed diff (§8.5 payload shape), and the
// §9 dashboard event is never emitted by the diff endpoint.
func TestVersionDiffErrorsAndSuccess(t *testing.T) {
	outbox := &captureQualityOutbox{}

	store := &fakeQualityStore{
		agent:   workspace.Agent{ID: "agent-a"},
		diffErr: fmt.Errorf("version 5: %w", workspace.ErrVersionSnapshotMissing),
	}
	server := newQualityServer(t, store, outbox, nil)
	status, body := qualityGet(t, server, "/v1/workspace/agents/agent-a/versions/diff?from=3&to=5", "readertoken")
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Contains(t, body, "snapshot")
	server.Close()

	okStore := &fakeQualityStore{
		agent: workspace.Agent{ID: "agent-a"},
		diff: workspace.VersionDiff{
			From: 3, To: 4,
			DefinitionChanges: []workspace.DefinitionChange{
				{Field: "constraints", Kind: workspace.DefinitionChangeAdded, Value: "redact credentials"},
			},
			PromptDiff: []string{"- old line", "+ new line"},
		},
	}
	server = newQualityServer(t, okStore, outbox, nil)
	defer server.Close()
	status, body = qualityGet(t, server, "/v1/workspace/agents/agent-a/versions/diff?from=3&to=4", "readertoken")
	require.Equal(t, http.StatusOK, status)
	var payload struct {
		From              int                          `json:"from"`
		To                int                          `json:"to"`
		DefinitionChanges []workspace.DefinitionChange `json:"definition_changes"`
		PromptDiff        []string                     `json:"prompt_diff"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	require.Equal(t, 3, payload.From)
	require.Equal(t, 4, payload.To)
	require.Len(t, payload.DefinitionChanges, 1)
	require.Equal(t, workspace.DefinitionChangeAdded, payload.DefinitionChanges[0].Kind)
	require.Equal(t, []string{"- old line", "+ new line"}, payload.PromptDiff)
	require.Empty(t, outbox.events, "only the dashboard view emits the §9 event")
}

// TestEmitQualityDashboardViewNilOutbox: a missing outbox must not panic —
// the metric is best-effort beside the report (§9).
func TestEmitQualityDashboardViewNilOutbox(t *testing.T) {
	emitQualityDashboardView(context.Background(), nil, workspace.Agent{ID: "agent-a"}, observation.Actor{})
}
