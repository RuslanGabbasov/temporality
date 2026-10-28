package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/workspace"
)

// fakeAgentRefStore satisfies agentRefLookup with canned responses.
type fakeAgentRefStore struct {
	byID      map[string]workspace.Agent
	idErr     error // returned for every GetAgent call
	byName    map[string][]workspace.Agent
	nameErr   error
	idCalls   int
	nameCalls int
}

func (f *fakeAgentRefStore) GetAgent(_ context.Context, id string) (workspace.Agent, error) {
	f.idCalls++
	if f.idErr != nil {
		return workspace.Agent{}, f.idErr
	}
	a, ok := f.byID[id]
	if !ok {
		return workspace.Agent{}, workspace.ErrNotFound
	}
	return a, nil
}

func (f *fakeAgentRefStore) FindAgentsByName(_ context.Context, name, _ string) ([]workspace.Agent, error) {
	f.nameCalls++
	if f.nameErr != nil {
		return nil, f.nameErr
	}
	// Mirrors the store's lower(name) = lower($2) match.
	return f.byName[strings.ToLower(name)], nil
}

func refFixture() *fakeAgentRefStore {
	lead := workspace.Agent{ID: "lead-ai-agent", Name: "Lead", Enabled: true}
	return &fakeAgentRefStore{
		byID: map[string]workspace.Agent{
			"lead-ai-agent": lead,
			"researcher":    {ID: "researcher", Name: "Product Analyst"},
		},
		byName: map[string][]workspace.Agent{
			"lead": {lead},
		},
	}
}

// The exact id path must not fall through to the name lookup.
func TestResolveAgentRefExactIDSkipsNameFallback(t *testing.T) {
	store := refFixture()
	a, err := resolveAgentRef(context.Background(), store, "lead-ai-agent", "proj")
	require.NoError(t, err)
	require.Equal(t, "lead-ai-agent", a.ID)
	require.Equal(t, 1, store.idCalls)
	require.Zero(t, store.nameCalls)
}

// The uniform switch: a disabled agent is not launchable, not even by exact
// id from inside another run (delegation goes through the same resolver).
func TestResolveAgentRefRejectsDisabledAgent(t *testing.T) {
	store := refFixture()
	store.byID["lead-ai-agent"] = workspace.Agent{ID: "lead-ai-agent", Name: "Lead", Enabled: false}
	_, err := resolveAgentRef(context.Background(), store, "lead-ai-agent", "proj")
	require.Error(t, err)
	require.Contains(t, err.Error(), "disabled")
}

// A model-supplied display name resolves when the match is unique — the actual
// failure mode that motivated the fallback ("Lead" → "lead" misses).
func TestResolveAgentRefNameFallbackResolvesUniqueMatch(t *testing.T) {
	store := refFixture()
	a, err := resolveAgentRef(context.Background(), store, "Lead", "proj")
	require.NoError(t, err)
	require.Equal(t, "lead-ai-agent", a.ID)
}

// An ambiguous name reports the candidate ids so the caller can disambiguate.
func TestResolveAgentRefAmbiguousNameListsCandidates(t *testing.T) {
	store := refFixture()
	store.byName["qa"] = []workspace.Agent{
		{ID: "qa-platform", Name: "QA"},
		{ID: "qa-security", Name: "qa"},
	}
	_, err := resolveAgentRef(context.Background(), store, "QA", "proj")
	require.Error(t, err)
	require.NotErrorIs(t, err, workspace.ErrNotFound)
	require.Contains(t, err.Error(), "qa-platform")
	require.Contains(t, err.Error(), "qa-security")
	require.Contains(t, err.Error(), "agents tool")
}

// No match at all stays a plain ErrNotFound.
func TestResolveAgentRefMissIsNotFound(t *testing.T) {
	store := refFixture()
	_, err := resolveAgentRef(context.Background(), store, "nope", "proj")
	require.ErrorIs(t, err, workspace.ErrNotFound)
}

// Storage failures surface as-is (the handler maps them to 500).
func TestResolveAgentRefStorageErrorPropagates(t *testing.T) {
	store := refFixture()
	store.idErr = errors.New("db down")
	_, err := resolveAgentRef(context.Background(), store, "lead-ai-agent", "proj")
	require.EqualError(t, err, "db down")
}
