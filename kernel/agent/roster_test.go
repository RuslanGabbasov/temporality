package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// agentsStub serves the workspace endpoint the agents tool reads: the
// project roster (full Agent rows, like ListAgentsByProject returns).
func agentsStub(t *testing.T, agentsJSON string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/workspace/projects/delegate-smoke/agents" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(agentsJSON))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server
}

func rosterFixture() string {
	return `{"agents":[
		{"id":"lead-ai-agent","name":"Lead","description":"Decomposes features and drives the dev pipeline","max_turns":50,
			 "definition":{"capabilities":{"delegation":true,"modify_files":true,"run_commands":true,"skills":true,"knowledge":true,"read_files":false}}},
		{"id":"researcher","name":"Product Analyst","description":"Researches the market and competitors"},
		{"id":"intake","name":"Intake","description":"Routes inbound messages","project_id":"delegate-smoke"}
	]}`
}

func decodeRoster(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &payload))
	return payload
}

func rosterCards(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	raw, _ := payload["agents"].([]any)
	cards := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		cards = append(cards, entry.(map[string]any))
	}
	return cards
}

func TestAgentsToolRendersDelegationRoster(t *testing.T) {
	server := agentsStub(t, rosterFixture())
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.handleAgents(context.Background(), ToolRequest{
		Name: "agents", Project: "delegate-smoke", Arguments: map[string]any{},
	})
	require.NoError(t, err)
	payload := decodeRoster(t, result.Content)
	require.EqualValues(t, 3, payload["total"])
	require.Equal(t, "pass the exact agent_id to delegate/plan", payload["hint"])

	cards := rosterCards(t, payload)
	lead := cards[0]
	require.Equal(t, "lead-ai-agent", lead["agent_id"])
	require.Equal(t, "Lead", lead["name"])
	require.Contains(t, lead["purpose"], "Decomposes features")
	require.True(t, lead["can_delegate"].(bool))
	// Unset capability means allowed, explicit false drops out.
	require.Contains(t, lead["capabilities"], "modify_files")
	require.NotContains(t, lead["capabilities"], "read_files")

	// The effective child budget: the card's configured value, or the
	// workflow default when the card does not configure one — the number a
	// delegated child runs with when the call sets no max_turns.
	require.EqualValues(t, 50, lead["max_turns"])

	// A legacy agent (no definition): no delegation, empty capabilities.
	researcher := cards[1]
	require.False(t, researcher["can_delegate"].(bool))
	require.Equal(t, []any{}, researcher["capabilities"])
	require.EqualValues(t, 8, researcher["max_turns"], "unset card falls back to the workflow default")

	// Project scope is surfaced.
	intake := cards[2]
	require.Equal(t, "project:delegate-smoke", intake["scope"])
}

func TestAgentsToolFiltersByQuery(t *testing.T) {
	server := agentsStub(t, rosterFixture())
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	// Substring matches names, case-insensitively.
	result, err := activities.handleAgents(context.Background(), ToolRequest{
		Name: "agents", Project: "delegate-smoke", Arguments: map[string]any{"query": "analyst"},
	})
	require.NoError(t, err)
	payload := decodeRoster(t, result.Content)
	require.EqualValues(t, 1, payload["total"])
	require.Equal(t, "researcher", rosterCards(t, payload)[0]["agent_id"])

	// No match hints at dropping the query.
	result, err = activities.handleAgents(context.Background(), ToolRequest{
		Name: "agents", Project: "delegate-smoke", Arguments: map[string]any{"query": "nope"},
	})
	require.NoError(t, err)
	payload = decodeRoster(t, result.Content)
	require.EqualValues(t, 0, payload["total"])
	require.Equal(t, "no agents match; drop the query to see the full roster", payload["hint"])
}

func TestAgentsToolReportsUnavailableWorkspace(t *testing.T) {
	server := agentsStub(t, rosterFixture())
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	// Unknown project → the stub answers 404 → the tool returns an error string.
	result, err := activities.handleAgents(context.Background(), ToolRequest{
		Name: "agents", Project: "other-project", Arguments: map[string]any{},
	})
	require.NoError(t, err)
	require.Contains(t, result.Content, "error: list agents failed (HTTP 404)")

	// No project in the request at all.
	result, err = activities.handleAgents(context.Background(), ToolRequest{Name: "agents"})
	require.NoError(t, err)
	require.Equal(t, "error: project not set", result.Content)
}

// The 404 body of run-config reaches the model through ResolveAgent's error
// detail (unknown id, ambiguous name candidates).
func TestResolveAgentSurfacesWorkspaceErrorDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"agent name \"lead\" matches multiple agents (lead-ai-agent, lead-qa); use an exact agent_id"}`))
	}))
	t.Cleanup(server.Close)
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	_, err := activities.ResolveAgent(context.Background(), ResolveAgentRequest{
		Project: "delegate-smoke", AgentID: "lead",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "lead-ai-agent, lead-qa")
	require.Contains(t, err.Error(), "not available in project")
}

// TestResolveAgentFloorsDelegatedBudgets: delegating models guess budgets low
// (observed 9–15) and children died mid-work as incomplete. A low explicit
// budget is raised to DelegatedTurnsFloor, a generous one is honored, and no
// budget at all keeps the agent card's value (the workflow normalizes 0 to
// its own default).
func TestResolveAgentFloorsDelegatedBudgets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"run_id":"child","project":"p","max_turns":50}`))
	}))
	t.Cleanup(server.Close)
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	// A low model guess is floored.
	child, err := activities.ResolveAgent(context.Background(), ResolveAgentRequest{Project: "p", AgentID: "dev", RunID: "child", MaxTurns: 15})
	require.NoError(t, err)
	require.Equal(t, DelegatedTurnsFloor, child.MaxTurns)

	// A generous guess is honored as-is.
	child, err = activities.ResolveAgent(context.Background(), ResolveAgentRequest{Project: "p", AgentID: "dev", RunID: "child", MaxTurns: 60})
	require.NoError(t, err)
	require.Equal(t, 60, child.MaxTurns)

	// No explicit budget keeps the card's value.
	child, err = activities.ResolveAgent(context.Background(), ResolveAgentRequest{Project: "p", AgentID: "dev", RunID: "child"})
	require.NoError(t, err)
	require.Equal(t, 50, child.MaxTurns)
}
