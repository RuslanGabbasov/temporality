package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// agents tool (docs/agent-delegation.md §4): before delegate/plan the model
// needs the exact agent ids valid in the project. Ids are not always derivable
// from display names ("Lead" → lead-ai-agent), and a wrong guess costs a full
// failed-resolution round-trip (or a plan rejection). The roster returns id,
// name, purpose, the capability profile and the turn budget a child of this
// agent would run with — the fields a delegating agent needs to pick a target
// and size its task budgets.

// agentLite is the roster's unmarshal target: the workspace API returns full
// Agent rows, only the delegation-relevant fields are kept.
type agentLite struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ProjectID   string `json:"project_id"`
	MaxTurns    *int   `json:"max_turns,omitempty"`
	Definition  *struct {
		Capabilities struct {
			ReadFiles   *bool `json:"read_files"`
			ModifyFiles *bool `json:"modify_files"`
			RunCommands *bool `json:"run_commands"`
			Network     *bool `json:"network"`
			Skills      *bool `json:"skills"`
			Knowledge   *bool `json:"knowledge"`
			Delegation  *bool `json:"delegation"`
		} `json:"capabilities"`
	} `json:"definition"`
}

// agentCard is the agent-facing projection: identity, purpose and the
// capability profile that matters for delegation decisions.
type agentCard struct {
	AgentID      string   `json:"agent_id"`
	Name         string   `json:"name"`
	Purpose      string   `json:"purpose,omitempty"`
	Scope        string   `json:"scope,omitempty"` // set for project-scoped agents
	CanDelegate  bool     `json:"can_delegate"`
	Capabilities []string `json:"capabilities"` // always an array; empty for legacy agents
	// MaxTurns is the budget a delegated child of this agent runs with when
	// the delegate/plan call does not set its own — the same value
	// applyAgentConfig enforces. Without it the lead sizes budgets blind:
	// an 8-turn card quietly kills any real subtask mid-work.
	MaxTurns int `json:"max_turns"`
}

func (a *Activities) handleAgents(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.Project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	query, _ := request.Arguments["query"].(string)
	endpoint := fmt.Sprintf("%s/v1/workspace/projects/%s/agents", a.WorkspaceURL, url.PathEscape(request.Project))
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: list agents failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	return ToolResult{Content: renderAgentRoster(body, query)}, nil
}

// renderAgentRoster is pure: filter by query (id/name substring,
// case-insensitive), project to cards, cap the list so a large installation
// cannot flood the delegating agent's context. The API order (created_at) is
// preserved, so the roster is deterministic for a given snapshot.
func renderAgentRoster(body, query string) string {
	var list struct {
		Agents []agentLite `json:"agents"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		return "error: agent roster is malformed: " + err.Error()
	}
	query = strings.ToLower(strings.TrimSpace(query))
	const limit = 50
	cards := make([]agentCard, 0, len(list.Agents))
	total := 0
	truncated := false
	for _, candidate := range list.Agents {
		if query != "" &&
			!strings.Contains(strings.ToLower(candidate.ID), query) &&
			!strings.Contains(strings.ToLower(candidate.Name), query) {
			continue
		}
		total++
		if len(cards) == limit {
			truncated = true
			continue
		}
		cards = append(cards, agentCardFor(candidate))
	}
	hint := "pass the exact agent_id to delegate/plan"
	if total == 0 {
		hint = "no agents match; drop the query to see the full roster"
	}
	payload := map[string]any{"total": total, "hint": hint, "agents": cards}
	if truncated {
		payload["truncated"] = true
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

// agentCardFor projects one agent row. Capability semantics mirror
// workspace.AgentCapabilities: unset means allowed, delegation is the inverted
// exception — it must be explicitly granted. Agents without a structured
// definition (legacy prompt-only) report no capabilities and cannot delegate,
// matching what applyAgentConfig enforces.
func agentCardFor(a agentLite) agentCard {
	card := agentCard{AgentID: a.ID, Name: a.Name, Purpose: truncate(a.Description, 200)}
	if a.ProjectID != "" {
		card.Scope = "project:" + a.ProjectID
	}
	// The effective child budget: the card value when configured, the workflow
	// default otherwise — normalizeTurnBudget applies the same rule at run
	// start, so the number here is what a child would actually run with.
	cardTurns := 0
	if a.MaxTurns != nil {
		cardTurns = *a.MaxTurns
	}
	card.MaxTurns = normalizeTurnBudget(cardTurns)
	if a.Definition == nil {
		card.Capabilities = []string{}
		return card
	}
	caps := a.Definition.Capabilities
	card.CanDelegate = caps.Delegation != nil && *caps.Delegation
	enabled := []struct {
		flag *bool
		name string
	}{
		{caps.ReadFiles, "read_files"},
		{caps.ModifyFiles, "modify_files"},
		{caps.RunCommands, "run_commands"},
		{caps.Network, "network"},
		{caps.Skills, "skills"},
		{caps.Knowledge, "knowledge"},
	}
	card.Capabilities = []string{}
	for _, cap := range enabled {
		if cap.flag == nil || *cap.flag {
			card.Capabilities = append(card.Capabilities, cap.name)
		}
	}
	return card
}
