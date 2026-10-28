package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/workspace"
)

// The uniform switch on the run-start choke point: every start path (API,
// intake, webhook and schedule triggers) resolves the run's agent here, so a
// disabled agent refuses new runs everywhere at once.
func TestResolveRunAgentRejectsDisabledExplicitAgent(t *testing.T) {
	store := &fakeRunAgentLookup{
		agents: map[string]workspace.Agent{
			"agg": {ID: "agg", Name: "Agg", Enabled: false},
		},
	}
	_, a, err := resolveRunAgent(context.Background(), store, "proj", "agg")
	require.Error(t, err)
	require.Contains(t, err.Error(), "disabled")
	require.Nil(t, a)
}

// A project whose default agent was disabled fails loudly: silently running
// bare-model would lose the configuration the project was set up with.
func TestResolveRunAgentRejectsDisabledDefaultAgent(t *testing.T) {
	store := &fakeRunAgentLookup{
		projects: map[string]workspace.Project{
			"proj": {ID: "proj", DefaultAgentID: "agg"},
		},
		agents: map[string]workspace.Agent{
			"agg": {ID: "agg", Name: "Agg", Enabled: false},
		},
	}
	_, _, err := resolveRunAgent(context.Background(), store, "proj", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "disabled")
}

// A project without a default agent still degrades to a bare run.
func TestResolveRunAgentNoDefaultStaysBare(t *testing.T) {
	store := &fakeRunAgentLookup{
		projects: map[string]workspace.Project{"proj": {ID: "proj"}},
	}
	id, a, err := resolveRunAgent(context.Background(), store, "proj", "")
	require.NoError(t, err)
	require.Empty(t, id)
	require.Nil(t, a)
}

// An enabled agent resolves as before — the switch only gates the off state.
func TestResolveRunAgentEnabledAgentResolves(t *testing.T) {
	store := &fakeRunAgentLookup{
		agents: map[string]workspace.Agent{"agg": {ID: "agg", Name: "Agg", Enabled: true}},
	}
	id, a, err := resolveRunAgent(context.Background(), store, "proj", "agg")
	require.NoError(t, err)
	require.Equal(t, "agg", id)
	require.NotNil(t, a)
}

// The toggle request shape: a boolean is required, missing or null bodies are
// a 400, not an accidental disable.
func TestDecodeEnabledRequest(t *testing.T) {
	assert := func(body string, wantEnabled bool, wantOK bool) {
		t.Helper()
		req := httptest.NewRequest("POST", "/enabled", strings.NewReader(body))
		rec := httptest.NewRecorder()
		enabled, ok := decodeEnabledRequest(rec, req)
		require.Equal(t, wantOK, ok, body)
		if wantOK {
			require.Equal(t, wantEnabled, enabled)
		}
	}
	assert(`{"enabled":false}`, false, true)
	assert(`{"enabled":true}`, true, true)
	assert(`{}`, false, false)
	assert(`{"enabled":null}`, false, false)
	assert(`not json`, false, false)
}

// --- minimal fakes ---------------------------------------------------------

type fakeRunAgentLookup struct {
	agents   map[string]workspace.Agent
	projects map[string]workspace.Project
}

func (f *fakeRunAgentLookup) GetAgent(_ context.Context, id string) (workspace.Agent, error) {
	a, ok := f.agents[id]
	if !ok {
		return workspace.Agent{}, workspace.ErrNotFound
	}
	return a, nil
}

func (f *fakeRunAgentLookup) GetProject(_ context.Context, id string) (workspace.Project, error) {
	p, ok := f.projects[id]
	if !ok {
		return workspace.Project{}, workspace.ErrNotFound
	}
	return p, nil
}
