// Package agent — trajectory extraction primitive.
//
// Extracts deterministic structures from a completed run's event stream:
// turns, tool-call sequences, decision points, knowledge events, and
// repeating patterns. All results carry provenance back to source event IDs.
//
// No separate storage: everything is computed from observation events.
package agent

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// Public types
// ──────────────────────────────────────────────────────────────────────────────

// Trajectory is the full extraction result for one run.
type Trajectory struct {
	RunID     string            `json:"run_id"`
	Project   string            `json:"project"`
	Turns     []Turn            `json:"turns"`
	Tools     []ToolStep        `json:"tools"`
	Knowledge []KnowledgeEvent  `json:"knowledge"`
	Patterns  []Pattern         `json:"patterns"`
	Summary   TrajectorySummary `json:"summary"`
}

// Turn represents one agent turn: a model call followed by tool calls.
type Turn struct {
	Number     int        `json:"number"`
	ModelEvent string     `json:"model_event_id"` // provenance
	Tools      []ToolStep `json:"tools"`
	Tokens     int        `json:"tokens,omitempty"`
	LatencyMs  int64      `json:"latency_ms,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    time.Time  `json:"ended_at"`
}

// ToolStep is a single tool invocation with its outcome.
type ToolStep struct {
	Tool        string    `json:"tool"`
	Arguments   string    `json:"arguments,omitempty"` // compact JSON
	Output      string    `json:"output,omitempty"`    // truncated
	ExitCode    *int      `json:"exit_code,omitempty"`
	Success     bool      `json:"success"`
	Server      string    `json:"server,omitempty"` // MCP server if applicable
	OperationID string    `json:"operation_id"`
	EventID     string    `json:"event_id"` // tool.started event — provenance
	CompletedID string    `json:"completed_event_id,omitempty"`
	TurnNumber  int       `json:"turn_number"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	LatencyMs   int64     `json:"latency_ms,omitempty"`
}

// KnowledgeEvent captures knowledge formation, recall, or invalidation.
type KnowledgeEvent struct {
	Kind        string    `json:"kind"` // proposed, recalled, invalidated, superseded, corrected
	KnowledgeID string    `json:"knowledge_id"`
	Proposition string    `json:"proposition,omitempty"`
	EventID     string    `json:"event_id"` // provenance
	TurnNumber  int       `json:"turn_number"`
	At          time.Time `json:"at"`
}

// Pattern is a repeating tool-call sequence found across multiple turns.
type Pattern struct {
	Signature string   `json:"signature"` // e.g. "read_file → run_command"
	Turns     []int    `json:"turns"`     // which turns contain this pattern
	Count     int      `json:"count"`
	Tools     []string `json:"tools"`
}

// TrajectorySummary is a compact overview of the run.
type TrajectorySummary struct {
	TotalTurns        int      `json:"total_turns"`
	TotalTools        int      `json:"total_tools"`
	TotalTokens       int      `json:"total_tokens"`
	TotalLatencyMs    int64    `json:"total_latency_ms"`
	ToolsUsed         []string `json:"tools_used"`
	KnowledgeFormed   int      `json:"knowledge_formed"`
	KnowledgeRecalled int      `json:"knowledge_recalled"`
	FailedTools       int      `json:"failed_tools"`
	RunStatus         string   `json:"run_status"`
	Answer            string   `json:"answer,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Extraction
// ──────────────────────────────────────────────────────────────────────────────

// EventLike is the minimal interface for an observation event.
// We don't import the observation package to keep this module decoupled.
type EventLike struct {
	EventID    string
	OccurredAt time.Time
	Type       string
	Data       map[string]any
	Context    struct{ Run, Project string }
}

// ExtractTrajectory builds a Trajectory from a run's event stream.
// Events must be sorted by OccurredAt ascending.
func ExtractTrajectory(events []EventLike) Trajectory {
	if len(events) == 0 {
		return Trajectory{}
	}

	runID := events[0].Context.Run
	project := events[0].Context.Project

	var trajectory Trajectory
	trajectory.RunID = runID
	trajectory.Project = project

	// Index events by type for efficient lookup.
	turnNumber := 0
	var currentTools []ToolStep
	var toolStartMap = make(map[string]*ToolStep) // operationID → pending tool

	for _, ev := range events {
		d := ev.Data

		switch ev.Type {
		case "turn.started":
			turnNumber++
			if turnNumber > 1 && len(currentTools) > 0 {
				// Close previous turn
				trajectory.Turns[len(trajectory.Turns)-1].Tools = currentTools
				currentTools = nil
			}

		case "model.completed":
			tokens := intField(d, "total_tokens")
			latency := int64Field(d, "latency_ms")
			trajectory.Turns = append(trajectory.Turns, Turn{
				Number:     turnNumber,
				ModelEvent: ev.EventID,
				Tokens:     tokens,
				LatencyMs:  latency,
				StartedAt:  ev.OccurredAt,
			})
			trajectory.Summary.TotalTokens += tokens
			trajectory.Summary.TotalLatencyMs += latency

		case "tool.started":
			tool := stringField(d, "tool")
			step := ToolStep{
				Tool:        tool,
				Arguments:   stringField(d, "arguments"),
				Server:      stringField(d, "server"),
				OperationID: stringField(d, "operation_id"),
				EventID:     ev.EventID,
				TurnNumber:  turnNumber,
				StartedAt:   ev.OccurredAt,
			}
			toolStartMap[step.OperationID] = &step
			trajectory.Tools = append(trajectory.Tools, step)
			trajectory.Summary.TotalTools++

			if !containsStr(trajectory.Summary.ToolsUsed, tool) {
				trajectory.Summary.ToolsUsed = append(trajectory.Summary.ToolsUsed, tool)
			}

		case "tool.completed":
			opID := stringField(d, "operation_id")
			if pending, ok := toolStartMap[opID]; ok {
				pending.Output = stringField(d, "output")
				pending.CompletedID = ev.EventID
				pending.CompletedAt = ev.OccurredAt
				if exitCode, ok := d["exit_code"]; ok {
					code := int(exitCode.(float64))
					pending.ExitCode = &code
					pending.Success = code == 0
				} else {
					pending.Success = true
				}
				pending.LatencyMs = ev.OccurredAt.Sub(pending.StartedAt).Milliseconds()
				currentTools = append(currentTools, *pending)
				delete(toolStartMap, opID)
			}

		case "tool.failed":
			opID := stringField(d, "operation_id")
			if pending, ok := toolStartMap[opID]; ok {
				pending.Output = stringField(d, "detail")
				pending.CompletedID = ev.EventID
				pending.CompletedAt = ev.OccurredAt
				pending.Success = false
				pending.LatencyMs = ev.OccurredAt.Sub(pending.StartedAt).Milliseconds()
				currentTools = append(currentTools, *pending)
				delete(toolStartMap, opID)
				trajectory.Summary.FailedTools++
			}

		case "knowledge.proposed":
			trajectory.Knowledge = append(trajectory.Knowledge, KnowledgeEvent{
				Kind:        "proposed",
				KnowledgeID: stringField(d, "knowledge_id"),
				Proposition: stringField(d, "proposition"),
				EventID:     ev.EventID,
				TurnNumber:  turnNumber,
				At:          ev.OccurredAt,
			})
			trajectory.Summary.KnowledgeFormed++

		case "knowledge.recalled":
			trajectory.Knowledge = append(trajectory.Knowledge, KnowledgeEvent{
				Kind:        "recalled",
				KnowledgeID: stringField(d, "knowledge_id"),
				EventID:     ev.EventID,
				TurnNumber:  turnNumber,
				At:          ev.OccurredAt,
			})
			trajectory.Summary.KnowledgeRecalled++

		case "knowledge.invalidated", "knowledge.superseded", "knowledge.corrected":
			trajectory.Knowledge = append(trajectory.Knowledge, KnowledgeEvent{
				Kind:        strings.TrimPrefix(ev.Type, "knowledge."),
				KnowledgeID: stringField(d, "knowledge_id"),
				EventID:     ev.EventID,
				TurnNumber:  turnNumber,
				At:          ev.OccurredAt,
			})

		case "run.completed":
			trajectory.Summary.RunStatus = "completed"

		case "run.failed":
			trajectory.Summary.RunStatus = "failed"

		case "agent.summary":
			trajectory.Summary.Answer = stringField(d, "answer")
		}
	}

	// Close last turn
	if len(currentTools) > 0 && len(trajectory.Turns) > 0 {
		trajectory.Turns[len(trajectory.Turns)-1].Tools = currentTools
	}
	trajectory.Summary.TotalTurns = len(trajectory.Turns)

	// Set turn end times
	for i := range trajectory.Turns {
		if len(trajectory.Turns[i].Tools) > 0 {
			last := trajectory.Turns[i].Tools[len(trajectory.Turns[i].Tools)-1]
			trajectory.Turns[i].EndedAt = last.CompletedAt
		} else if trajectory.Turns[i].StartedAt.IsZero() == false {
			trajectory.Turns[i].EndedAt = trajectory.Turns[i].StartedAt
		}
	}

	// Extract repeating patterns
	trajectory.Patterns = findPatterns(trajectory.Turns)

	return trajectory
}

// ──────────────────────────────────────────────────────────────────────────────
// Pattern detection
// ──────────────────────────────────────────────────────────────────────────────

func findPatterns(turns []Turn) []Pattern {
	// Build tool sequences per turn
	type turnSeq struct {
		turn int
		seq  []string
	}
	var seqs []turnSeq
	for _, t := range turns {
		var seq []string
		for _, tool := range t.Tools {
			seq = append(seq, tool.Tool)
		}
		if len(seq) >= 2 {
			seqs = append(seqs, turnSeq{turn: t.Number, seq: seq})
		}
	}

	// Find repeating subsequences (length 2+)
	counts := make(map[string][]int) // signature → turn numbers
	for _, ts := range seqs {
		// Generate all subsequences of length 2..len
		for length := 2; length <= len(ts.seq); length++ {
			for start := 0; start <= len(ts.seq)-length; start++ {
				sub := ts.seq[start : start+length]
				sig := strings.Join(sub, " → ")
				if !containsInt(counts[sig], ts.turn) {
					counts[sig] = append(counts[sig], ts.turn)
				}
			}
		}
	}

	var patterns []Pattern
	for sig, turns := range counts {
		if len(turns) < 2 {
			continue // must appear in at least 2 turns
		}
		tools := strings.Split(sig, " → ")
		sort.Ints(turns)
		patterns = append(patterns, Pattern{
			Signature: sig,
			Turns:     turns,
			Count:     len(turns),
			Tools:     tools,
		})
	}

	// Sort by frequency descending
	sort.Slice(patterns, func(i, j int) bool {
		return patterns[i].Count > patterns[j].Count
	})

	return patterns
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func stringField(d map[string]any, key string) string {
	if v, ok := d[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	return ""
}

func intField(d map[string]any, key string) int {
	if v, ok := d[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

func int64Field(d map[string]any, key string) int64 {
	if v, ok := d[key]; ok {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int64:
			return n
		}
	}
	return 0
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func containsInt(slice []int, n int) bool {
	for _, v := range slice {
		if v == n {
			return true
		}
	}
	return false
}
