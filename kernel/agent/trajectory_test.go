package agent

import (
	"testing"
	"time"
)

// trajEvent builds one journal event the way the trajectory endpoint feeds
// the extractor: the run/project context comes from the query, not from the
// events (child runs resolve their project from the journal itself).
func trajEvent(id, typ string, at time.Time, data map[string]any) EventLike {
	el := EventLike{EventID: id, OccurredAt: at, Type: typ, Data: data}
	el.Context.Run = "child-run-1"
	el.Context.Project = "omni"
	return el
}

// A delegated child run journals its full lifecycle under its own run id:
// the trajectory must extract turns/tools/tokens exactly like for a
// top-level run.
func TestExtractTrajectoryChildRunLifecycle(t *testing.T) {
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	events := []EventLike{
		trajEvent("e1", "run.started", base, map[string]any{"agent_id": "dev", "title": "patch"}),
		trajEvent("e1t", "turn.started", base.Add(30*time.Second), map[string]any{"turn": 1}),
		trajEvent("e2", "model.completed", base.Add(time.Minute), map[string]any{"turn": 1, "total_tokens": 120, "latency_ms": 900}),
		trajEvent("e3", "tool.started", base.Add(2*time.Minute), map[string]any{"turn": 1, "tool": "run_command", "operation_id": "op-1", "arguments": `{"cmd":"go test"}`}),
		trajEvent("e4", "tool.completed", base.Add(3*time.Minute), map[string]any{"turn": 1, "operation_id": "op-1", "exit_code": 0, "output": "ok"}),
		trajEvent("e5t", "turn.started", base.Add(3*time.Minute+30*time.Second), map[string]any{"turn": 2}),
		trajEvent("e5", "model.completed", base.Add(4*time.Minute), map[string]any{"turn": 2, "total_tokens": 80, "latency_ms": 400}),
		trajEvent("e6", "run.completed", base.Add(5*time.Minute), map[string]any{}),
	}

	traj := ExtractTrajectory(events)
	if traj.RunID != "child-run-1" || traj.Project != "omni" {
		t.Fatalf("context = %s/%s", traj.RunID, traj.Project)
	}
	if traj.Summary.TotalTurns != 2 {
		t.Errorf("turns = %d, want 2", traj.Summary.TotalTurns)
	}
	if traj.Summary.TotalTools != 1 || len(traj.Summary.ToolsUsed) != 1 || traj.Summary.ToolsUsed[0] != "run_command" {
		t.Errorf("tools = %d %v", traj.Summary.TotalTools, traj.Summary.ToolsUsed)
	}
	if traj.Summary.TotalTokens != 200 || traj.Summary.RunStatus != "completed" {
		t.Errorf("tokens = %d, status = %s", traj.Summary.TotalTokens, traj.Summary.RunStatus)
	}
	// The tool call lands in the first turn (closed when the second
	// model.completed opens), with its latency matched by operation id.
	if len(traj.Turns) != 2 || len(traj.Turns[0].Tools) != 1 {
		t.Fatalf("turn structure wrong: %+v", traj.Turns)
	}
	if traj.Turns[0].Tools[0].Success != true || traj.Turns[0].Tools[0].LatencyMs != time.Minute.Milliseconds() {
		t.Errorf("tool step = %+v", traj.Turns[0].Tools[0])
	}
}

func TestExtractTrajectoryEmpty(t *testing.T) {
	traj := ExtractTrajectory(nil)
	if traj.Summary.TotalTurns != 0 || traj.Summary.TotalTools != 0 {
		t.Errorf("empty input must yield an empty trajectory: %+v", traj.Summary)
	}
}
