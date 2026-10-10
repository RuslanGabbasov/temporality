package agent

import (
	"sort"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Activation chain: traces how knowledge influenced agent decisions.
// ──────────────────────────────────────────────────────────────────────────────

// ActivationChain is the full lifecycle of a knowledge item across runs.
type ActivationChain struct {
	KnowledgeID       string            `json:"knowledge_id"`
	Proposition       string            `json:"proposition"`
	CurrentState      string            `json:"current_state"`
	FormedAt          time.Time         `json:"formed_at"`
	FormedIn          string            `json:"formed_in"` // run ID
	FormedTurn        int               `json:"formed_turn"`
	FormationEvidence []ToolStep        `json:"formation_evidence"` // tool calls in the formation turn
	Activations       []Activation      `json:"activations"`
	Invalidation      *InvalidationInfo `json:"invalidation,omitempty"`
	SupersededBy      string            `json:"superseded_by,omitempty"`
	TotalRuns         int               `json:"total_runs"`
	IsAlive           bool              `json:"is_alive"`
}

// Activation is one recall/injection/use of knowledge in a run.
type Activation struct {
	RunID       string    `json:"run_id"`
	Turn        int       `json:"turn"`
	RecalledAt  time.Time `json:"recalled_at"`
	InjectedAt  time.Time `json:"injected_at,omitempty"`
	ToolName    string    `json:"tool_name,omitempty"`
	ToolSuccess *bool     `json:"tool_success,omitempty"`
	Outcome     string    `json:"outcome,omitempty"` // success, failure, uncertain
	Provenance  []string  `json:"provenance"`        // event IDs
}

// InvalidationInfo captures how and why knowledge was invalidated.
type InvalidationInfo struct {
	At      time.Time `json:"at"`
	RunID   string    `json:"run_id"`
	Kind    string    `json:"kind"` // invalidated, superseded, corrected
	Reason  string    `json:"reason,omitempty"`
	EventID string    `json:"event_id"`
}

// ExtractActivationChain builds the activation chain for a specific knowledge
// item across multiple trajectories.
func ExtractActivationChain(knowledgeID string, trajectories []Trajectory) ActivationChain {
	chain := ActivationChain{
		KnowledgeID: knowledgeID,
		IsAlive:     true,
	}

	runSet := make(map[string]bool)

	for _, traj := range trajectories {
		// Find knowledge events for this ID
		for _, k := range traj.Knowledge {
			if k.KnowledgeID != knowledgeID {
				continue
			}

			switch k.Kind {
			case "proposed":
				if chain.FormedAt.IsZero() || k.At.Before(chain.FormedAt) {
					chain.FormedAt = k.At
					chain.FormedIn = traj.RunID
					chain.FormedTurn = k.TurnNumber
					chain.Proposition = k.Proposition
					// Capture tool calls from the formation turn as evidence
					for _, turn := range traj.Turns {
						if turn.Number == k.TurnNumber {
							chain.FormationEvidence = turn.Tools
							break
						}
					}
				}
				chain.CurrentState = "proposed"
				runSet[traj.RunID] = true

			case "recalled":
				activation := Activation{
					RunID:      traj.RunID,
					Turn:       k.TurnNumber,
					RecalledAt: k.At,
					Provenance: []string{k.EventID},
				}
				// Find what happened after recall: look for tool calls in the same turn
				for _, turn := range traj.Turns {
					if turn.Number != k.TurnNumber {
						continue
					}
					for _, tool := range turn.Tools {
						// The tool call that follows recall is the "injection"
						if tool.StartedAt.After(k.At) || tool.StartedAt.Equal(k.At) {
							activation.InjectedAt = tool.StartedAt
							activation.ToolName = tool.Tool
							activation.ToolSuccess = &tool.Success
							if tool.Success {
								activation.Outcome = "success"
							} else {
								activation.Outcome = "failure"
							}
							activation.Provenance = append(activation.Provenance, tool.EventID)
							break
						}
					}
				}
				chain.Activations = append(chain.Activations, activation)
				runSet[traj.RunID] = true

			case "invalidated", "superseded", "corrected":
				chain.CurrentState = k.Kind
				chain.IsAlive = false
				chain.Invalidation = &InvalidationInfo{
					At:      k.At,
					RunID:   traj.RunID,
					Kind:    k.Kind,
					EventID: k.EventID,
				}
				if k.ReplacementID != "" {
					chain.SupersededBy = k.ReplacementID
				}
				runSet[traj.RunID] = true
			}
		}
	}

	chain.TotalRuns = len(runSet)

	// Sort activations by time
	sort.Slice(chain.Activations, func(i, j int) bool {
		return chain.Activations[i].RecalledAt.Before(chain.Activations[j].RecalledAt)
	})

	return chain
}

// ExtractAllActivationChains builds chains for all knowledge items in the trajectories.
func ExtractAllActivationChains(trajectories []Trajectory) []ActivationChain {
	knowledgeIDs := make(map[string]bool)
	for _, traj := range trajectories {
		for _, k := range traj.Knowledge {
			knowledgeIDs[k.KnowledgeID] = true
		}
	}

	var chains []ActivationChain
	for id := range knowledgeIDs {
		chain := ExtractActivationChain(id, trajectories)
		chains = append(chains, chain)
	}

	// Sort: alive first, then by activation count descending
	sort.Slice(chains, func(i, j int) bool {
		if chains[i].IsAlive != chains[j].IsAlive {
			return chains[i].IsAlive
		}
		return len(chains[i].Activations) > len(chains[j].Activations)
	})

	return chains
}

// ──────────────────────────────────────────────────────────────────────────────
// Scope inference from knowledge ID
// ──────────────────────────────────────────────────────────────────────────────

// KnowledgeScope infers a semantic scope from a knowledge ID.
// Knowledge IDs look like "run-id/knowledge/NN" — the run prefix gives context.
func KnowledgeScope(knowledgeID string) string {
	parts := strings.Split(knowledgeID, "/")
	if len(parts) < 2 {
		return "unknown"
	}
	// The run ID prefix contains the project/task context
	runPrefix := parts[0]
	// Extract meaningful parts from run ID like "project-20260928-123456-..."
	runParts := strings.Split(runPrefix, "-")
	if len(runParts) >= 1 {
		return runParts[0]
	}
	return "unknown"
}
