// Experience pattern projection: aggregates trajectories across runs into
// compact, reusable experience patterns with provenance.
package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Public types
// ──────────────────────────────────────────────────────────────────────────────

// ExperienceProjection is the aggregated view across multiple runs.
type ExperienceProjection struct {
	Project       string              `json:"project"`
	RunCount      int                 `json:"run_count"`
	ToolPatterns  []ToolPattern       `json:"tool_patterns"`
	KnowledgeLife []KnowledgeLifecyle `json:"knowledge_lifecycle"`
	Scopes        []ScopeSummary      `json:"scopes"`
	Summary       ProjectionSummary   `json:"summary"`
}

// ToolPattern is a tool-call sequence found across multiple runs.
type ToolPattern struct {
	Signature   string       `json:"signature"` // e.g. "run_command → mcp__search"
	Tools       []string     `json:"tools"`
	Count       int          `json:"count"`        // total occurrences
	RunCount    int          `json:"run_count"`    // how many runs
	Runs        []string     `json:"runs"`         // run IDs
	Turns       []int        `json:"turns"`        // turn numbers (within runs)
	SuccessRate float64      `json:"success_rate"` // 0.0–1.0
	FirstSeen   time.Time    `json:"first_seen"`
	LastSeen    time.Time    `json:"last_seen"`
	Provenance  []Provenance `json:"provenance"`
}

// KnowledgeLifecyle tracks a knowledge item across runs.
type KnowledgeLifecyle struct {
	KnowledgeID  string           `json:"knowledge_id"`
	Proposition  string           `json:"proposition"`
	CurrentState string           `json:"current_state"` // proposed, confirmed, invalidated, etc.
	Events       []KnowledgeEvent `json:"events"`        // all lifecycle events
	RunCount     int              `json:"run_count"`     // how many runs touched it
	FirstSeen    time.Time        `json:"first_seen"`
	LastSeen     time.Time        `json:"last_seen"`
	IsAlive      bool             `json:"is_alive"`
}

// ScopeSummary aggregates patterns within a semantic scope.
type ScopeSummary struct {
	Scope        string   `json:"scope"`
	ToolCount    int      `json:"tool_count"`
	KnowledgeIDs []string `json:"knowledge_ids"`
	RunIDs       []string `json:"run_ids"`
}

// ProjectionSummary is a compact overview.
type ProjectionSummary struct {
	TotalToolPatterns int      `json:"total_tool_patterns"`
	TotalKnowledge    int      `json:"total_knowledge"`
	AliveKnowledge    int      `json:"alive_knowledge"`
	DeadKnowledge     int      `json:"dead_knowledge"`
	AvgSuccessRate    float64  `json:"avg_success_rate"`
	MostUsedTools     []string `json:"most_used_tools"`
}

// Provenance links back to source events.
type Provenance struct {
	RunID   string `json:"run_id"`
	EventID string `json:"event_id"`
	Turn    int    `json:"turn"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Projection
// ──────────────────────────────────────────────────────────────────────────────

// ProjectExperience aggregates multiple Trajectory results into an ExperienceProjection.
func ProjectExperience(trajectories []Trajectory) ExperienceProjection {
	if len(trajectories) == 0 {
		return ExperienceProjection{}
	}

	project := trajectories[0].Project
	projection := ExperienceProjection{
		Project:  project,
		RunCount: len(trajectories),
	}

	// Collect all tool steps and knowledge events across runs.
	type toolOccurrence struct {
		signature string
		tools     []string
		runID     string
		turn      int
		success   bool
		at        time.Time
		eventIDs  []string
	}

	var allTools []toolOccurrence
	knowledgeMap := make(map[string]*KnowledgeLifecyle) // knowledgeID → lifecycle
	scopeSet := make(map[string]*ScopeSummary)          // scope → summary
	toolFreq := make(map[string]int)                    // tool name → count

	for _, traj := range trajectories {
		// Extract tool sequences per turn
		for _, turn := range traj.Turns {
			if len(turn.Tools) < 1 {
				continue
			}
			var toolNames []string
			allSuccess := true
			var eventIDs []string
			for _, t := range turn.Tools {
				toolNames = append(toolNames, t.Tool)
				if !t.Success {
					allSuccess = false
				}
				eventIDs = append(eventIDs, t.EventID)
				toolFreq[t.Tool]++
			}
			sig := strings.Join(toolNames, " → ")
			allTools = append(allTools, toolOccurrence{
				signature: sig,
				tools:     toolNames,
				runID:     traj.RunID,
				turn:      turn.Number,
				success:   allSuccess,
				at:        turn.StartedAt,
				eventIDs:  eventIDs,
			})
		}

		// Aggregate knowledge lifecycle
		for _, k := range traj.Knowledge {
			existing, ok := knowledgeMap[k.KnowledgeID]
			if !ok {
				existing = &KnowledgeLifecyle{
					KnowledgeID: k.KnowledgeID,
					Proposition: k.Proposition,
				}
				knowledgeMap[k.KnowledgeID] = existing
			}
			existing.Events = append(existing.Events, k)
			if k.Proposition != "" && existing.Proposition == "" {
				existing.Proposition = k.Proposition
			}
			existing.CurrentState = k.Kind // last event wins
			if k.At.Before(existing.FirstSeen) || existing.FirstSeen.IsZero() {
				existing.FirstSeen = k.At
			}
			if k.At.After(existing.LastSeen) {
				existing.LastSeen = k.At
			}
		}

		// Aggregate scopes from tool steps
		for _, tool := range traj.Tools {
			scope := toolScope(tool.Tool)
			if scope == "" {
				continue
			}
			ss, ok := scopeSet[scope]
			if !ok {
				ss = &ScopeSummary{Scope: scope}
				scopeSet[scope] = ss
			}
			if !containsStr(ss.RunIDs, traj.RunID) {
				ss.RunIDs = append(ss.RunIDs, traj.RunID)
			}
			ss.ToolCount++
		}
	}

	// Build tool patterns: group by signature, keep those appearing in 2+ runs
	sigMap := make(map[string]*ToolPattern)
	for _, occ := range allTools {
		tp, ok := sigMap[occ.signature]
		if !ok {
			tp = &ToolPattern{
				Signature: occ.signature,
				Tools:     occ.tools,
				FirstSeen: occ.at,
			}
			sigMap[occ.signature] = tp
		}
		tp.Count++
		if !containsStr(tp.Runs, occ.runID) {
			tp.Runs = append(tp.Runs, occ.runID)
			tp.RunCount++
		}
		tp.Turns = append(tp.Turns, occ.turn)
		if occ.success {
			// success rate calculated later
		}
		if occ.at.After(tp.LastSeen) {
			tp.LastSeen = occ.at
		}
		if occ.at.Before(tp.FirstSeen) || tp.FirstSeen.IsZero() {
			tp.FirstSeen = occ.at
		}
		for _, eid := range occ.eventIDs {
			tp.Provenance = append(tp.Provenance, Provenance{
				RunID:   occ.runID,
				EventID: eid,
				Turn:    occ.turn,
			})
		}
	}

	// Calculate success rates
	successCounts := make(map[string]int)
	for _, occ := range allTools {
		if occ.success {
			successCounts[occ.signature]++
		}
	}
	for sig, tp := range sigMap {
		if tp.Count > 0 {
			tp.SuccessRate = float64(successCounts[sig]) / float64(tp.Count)
		}
	}

	// Filter: keep patterns with 2+ occurrences (not necessarily 2+ runs)
	var toolPatterns []ToolPattern
	for _, tp := range sigMap {
		if tp.Count >= 2 {
			toolPatterns = append(toolPatterns, *tp)
		}
	}
	sort.Slice(toolPatterns, func(i, j int) bool {
		return toolPatterns[i].Count > toolPatterns[j].Count
	})
	projection.ToolPatterns = toolPatterns

	// Build knowledge lifecycle
	var knowledgeLife []KnowledgeLifecyle
	for _, kl := range knowledgeMap {
		// Count distinct runs
		runSet := make(map[string]bool)
		for _, ev := range kl.Events {
			// We don't have runID in KnowledgeEvent, but we can infer from the trajectory
			_ = ev
		}
		kl.IsAlive = kl.CurrentState != "invalidated" && kl.CurrentState != "superseded" && kl.CurrentState != "corrected"
		// Set RunCount from distinct trajectories that contain this knowledge
		for _, traj := range trajectories {
			for _, k := range traj.Knowledge {
				if k.KnowledgeID == kl.KnowledgeID {
					runSet[traj.RunID] = true
					break
				}
			}
		}
		kl.RunCount = len(runSet)
		knowledgeLife = append(knowledgeLife, *kl)
	}
	sort.Slice(knowledgeLife, func(i, j int) bool {
		return knowledgeLife[i].LastSeen.After(knowledgeLife[j].LastSeen)
	})
	projection.KnowledgeLife = knowledgeLife

	// Build scope summaries
	var scopes []ScopeSummary
	for _, ss := range scopeSet {
		// Collect knowledge IDs for this scope
		for _, kl := range knowledgeMap {
			for _, ev := range kl.Events {
				if toolScope(ev.KnowledgeID) == ss.Scope || strings.HasPrefix(ev.KnowledgeID, ss.Scope) {
					if !containsStr(ss.KnowledgeIDs, kl.KnowledgeID) {
						ss.KnowledgeIDs = append(ss.KnowledgeIDs, kl.KnowledgeID)
					}
					break
				}
			}
		}
		scopes = append(scopes, *ss)
	}
	sort.Slice(scopes, func(i, j int) bool {
		return scopes[i].ToolCount > scopes[j].ToolCount
	})
	projection.Scopes = scopes

	// Build summary
	var totalSuccess float64
	for _, tp := range toolPatterns {
		totalSuccess += tp.SuccessRate
	}
	avgSuccess := 0.0
	if len(toolPatterns) > 0 {
		avgSuccess = totalSuccess / float64(len(toolPatterns))
	}

	// Most used tools (top5)
	type toolCount struct {
		name  string
		count int
	}
	var toolCounts []toolCount
	for name, count := range toolFreq {
		toolCounts = append(toolCounts, toolCount{name, count})
	}
	sort.Slice(toolCounts, func(i, j int) bool {
		return toolCounts[i].count > toolCounts[j].count
	})
	var mostUsed []string
	for i, tc := range toolCounts {
		if i >= 5 {
			break
		}
		mostUsed = append(mostUsed, tc.name)
	}

	alive, dead := 0, 0
	for _, kl := range knowledgeLife {
		if kl.IsAlive {
			alive++
		} else {
			dead++
		}
	}

	projection.Summary = ProjectionSummary{
		TotalToolPatterns: len(toolPatterns),
		TotalKnowledge:    len(knowledgeLife),
		AliveKnowledge:    alive,
		DeadKnowledge:     dead,
		AvgSuccessRate:    avgSuccess,
		MostUsedTools:     mostUsed,
	}

	return projection
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

// TrajectoryArtifact is a reusable structure extracted from a trajectory.
type TrajectoryArtifact struct {
	ID          string       `json:"id"`
	Kind        string       `json:"kind"`      // "tool_sequence" or "workflow_fragment"
	Signature   string       `json:"signature"` // human-readable
	Tools       []string     `json:"tools"`
	SuccessRate float64      `json:"success_rate"`
	Count       int          `json:"count"`
	Runs        []string     `json:"runs"`
	Provenance  []Provenance `json:"provenance"`
}

// ExtractArtifacts extracts reusable artifacts from a trajectory.
// Artifacts are successful tool sequences that could be replayed.
func ExtractArtifacts(traj Trajectory) []TrajectoryArtifact {
	var artifacts []TrajectoryArtifact
	seen := make(map[string]*TrajectoryArtifact)

	for _, turn := range traj.Turns {
		if len(turn.Tools) < 2 {
			continue
		}
		var names []string
		allSuccess := true
		for _, t := range turn.Tools {
			names = append(names, t.Tool)
			if !t.Success {
				allSuccess = false
			}
		}
		if !allSuccess {
			continue
		}
		sig := strings.Join(names, " → ")
		art, ok := seen[sig]
		if !ok {
			art = &TrajectoryArtifact{
				ID:        fmt.Sprintf("artifact/%s/%d", traj.RunID, len(artifacts)+1),
				Kind:      "tool_sequence",
				Signature: sig,
				Tools:     names,
			}
			seen[sig] = art
			artifacts = append(artifacts, *art)
		}
		// Update the artifact in the slice
		for i := range artifacts {
			if artifacts[i].Signature == sig {
				artifacts[i].Count++
				if !containsStr(artifacts[i].Runs, traj.RunID) {
					artifacts[i].Runs = append(artifacts[i].Runs, traj.RunID)
				}
				for _, t := range turn.Tools {
					artifacts[i].Provenance = append(artifacts[i].Provenance, Provenance{
						RunID:   traj.RunID,
						EventID: t.EventID,
						Turn:    turn.Number,
					})
				}
				break
			}
		}
	}

	// Calculate success rates
	for i := range artifacts {
		// For single-run artifacts, success rate is1.0 (we only extracted successful sequences)
		artifacts[i].SuccessRate = 1.0
	}

	return artifacts
}
func toolScope(tool string) string {
	if strings.HasPrefix(tool, "mcp__") {
		return "mcp"
	}
	switch tool {
	case "run_command", "write_file", "read_file", "search", "glob", "grep", "list_directory":
		return "sandbox"
	case "remember", "recall":
		return "knowledge"
	default:
		return ""
	}
}
