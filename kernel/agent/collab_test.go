package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// Tests for the pure collaboration folds (docs/temporality-agent-collaboration-spec.md
// §7): participants from journal events, saga scope, addressing, payload
// bounds. The kernel-side access checks ride the same folds, so these cases
// mirror the spec's test table where a pure function can carry it.

func collabEvent(eventType, runID string, at time.Time, data map[string]any) observation.Event {
	return observation.Event{
		Type:       eventType,
		OccurredAt: at,
		Context:    observation.Context{Run: runID, Task: "task-1", Project: "proj"},
		Source:     observation.Source{ID: "kernel-1"},
		Data:       data,
	}
}

func TestFoldRunParticipants(t *testing.T) {
	base := time.Now().UTC()
	events := []observation.Event{
		collabEvent("run.started", "run-a", base, map[string]any{"agent_id": "coder-01", "role": "Coder", "title": "Implement API"}),
		collabEvent("run.started", "run-b", base.Add(time.Second), map[string]any{"agent_id": "reviewer-1", "role": "Reviewer"}),
		collabEvent("run.completed", "run-a", base.Add(2*time.Second), nil),
		// A retried run.started (same run, later time) must win — and carry
		// the newer agent identity.
		collabEvent("run.started", "run-b", base.Add(3*time.Second), map[string]any{"agent_id": "reviewer-2", "role": "Reviewer"}),
		// A terminal event without a started row still yields a participant.
		collabEvent("run.failed", "run-c", base.Add(4*time.Second), nil),
	}
	runs, humans := foldRunParticipants(events)
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
	if runs["run-a"].Status != "completed" || runs["run-a"].AgentID != "coder-01" {
		t.Fatalf("run-a folded wrong: %+v", runs["run-a"])
	}
	if runs["run-b"].AgentID != "reviewer-2" {
		t.Fatalf("expected the later run.started to win, got %+v", runs["run-b"])
	}
	if runs["run-c"].Status != "failed" {
		t.Fatalf("terminal-only run lost its status: %+v", runs["run-c"])
	}
	if len(humans) != 0 {
		t.Fatalf("no human actors expected, got %v", humans)
	}
}

func TestFoldRunHumans(t *testing.T) {
	base := time.Now().UTC()
	events := []observation.Event{
		collabEvent("approval.granted", "run-a", base, map[string]any{"approver": "user:ruslan"}),
		collabEvent("human.answered", "run-a", base, map[string]any{"actor": "user:ada"}),
		collabEvent("run.user_note", "run-a", base, map[string]any{"actor_id": "user:ada"}),
		// An empty actor must not create a phantom participant.
		collabEvent("approval.granted", "run-a", base, map[string]any{"approver": ""}),
	}
	_, humans := foldRunParticipants(events)
	if !humans["user:ruslan"] || !humans["user:ada"] {
		t.Fatalf("expected both human actors, got %v", humans)
	}
	if len(humans) != 2 {
		t.Fatalf("expected 2 human actors (dedup + no empty), got %v", humans)
	}
}

func TestBuildCollaborators(t *testing.T) {
	base := time.Now().UTC()
	runs := map[string]*runParticipant{
		"run-a":      {RunID: "run-a", AgentID: "coder-01", Role: "Coder", Title: "Implement API", Status: "running", StartedAt: base},
		"run-b":      {RunID: "run-b", AgentID: "reviewer-1", Role: "Reviewer", Status: "completed", StartedAt: base.Add(time.Second)},
		"own-run":    {RunID: "own-run", AgentID: "lead", Status: "running", StartedAt: base.Add(2 * time.Second)},
		"run-legacy": {RunID: "run-legacy", Status: "running", StartedAt: base.Add(3 * time.Second)},
	}
	list := buildCollaborators(runs, map[string]bool{"user:ruslan": true, "user:ada": true}, "own-run")
	if len(list) != 5 {
		t.Fatalf("expected 3 agents + 2 humans, got %d: %+v", len(list), list)
	}
	// The asker itself is excluded; a legacy run without agent_id falls back
	// to a run-prefixed id.
	for _, c := range list {
		if c.RunID == "own-run" {
			t.Fatalf("the calling run must not be its own collaborator: %+v", c)
		}
	}
	if list[0].ID != "agent:run:run-legacy" {
		t.Fatalf("agents sorted newest first, legacy id kept addressable: %+v", list[0])
	}
	if list[3].ID != "human:user:ada" || list[4].ID != "human:user:ruslan" {
		t.Fatalf("humans sorted alphabetically after agents: %+v", list[3:])
	}
}

func TestSagaScope(t *testing.T) {
	base := time.Now().UTC()
	sagaBind := func(eventType, sagaID, taskID string, at time.Time) sagaEvent {
		return sagaEvent{Type: eventType, SagaID: sagaID, TaskID: taskID, OccurredAt: at}
	}
	events := []sagaEvent{
		{Type: "saga.registered", SagaID: "saga-a", Title: "Original", TaskID: "task-1", OccurredAt: base},
		sagaBind("saga.attached", "saga-a", "task-2", base.Add(time.Second)),
		// task-3 joins a source saga that is later merged into saga-a.
		{Type: "saga.registered", SagaID: "saga-b", Title: "Temp", TaskID: "task-3", OccurredAt: base.Add(2 * time.Second)},
		{Type: "saga.merged", SagaID: "saga-a", Sources: []string{"saga-b"}, OccurredAt: base.Add(3 * time.Second)},
		// task-4 attaches then detaches — it must not be in the scope.
		sagaBind("saga.attached", "saga-a", "task-4", base.Add(4*time.Second)),
		sagaBind("saga.detached", "saga-a", "task-4", base.Add(5*time.Second)),
	}
	sagaID, tasks, title, ok := sagaScope(events, "task-1")
	if !ok || sagaID != "saga-a" {
		t.Fatalf("expected saga-a binding, got %q ok=%v", sagaID, ok)
	}
	if title != "Original" {
		t.Fatalf("expected the registered title, got %q", title)
	}
	if !tasks["task-1"] || !tasks["task-2"] {
		t.Fatalf("expected tasks 1 and 2 in scope, got %v", tasks)
	}
	if !tasks["task-3"] {
		t.Fatalf("the merged source's task must rebind to the target: %v", tasks)
	}
	if tasks["task-4"] {
		t.Fatalf("a detached task must leave the scope: %v", tasks)
	}
	// A renamed saga reports the renamed title.
	events = append(events, sagaEvent{Type: "saga.renamed", SagaID: "saga-a", Title: "Renamed", OccurredAt: base.Add(6 * time.Second)})
	_, _, title, _ = sagaScope(events, "task-1")
	if title != "Renamed" {
		t.Fatalf("expected the renamed title, got %q", title)
	}
	// An unbound task has no scope.
	if _, _, _, ok := sagaScope(events, "task-other"); ok {
		t.Fatal("an unbound task must have no saga scope")
	}
}

func TestParseCollaboratorTo(t *testing.T) {
	for _, testCase := range []struct {
		in, kind, value string
	}{
		{"agent:coder-01", "agent", "coder-01"},
		{"human:ada", "human", "ada"},
		{"run:run-123", "run", "run-123"},
		{"run-123/plan1/01-fix", "run", "run-123/plan1/01-fix"},
		{"  agent:x  ", "agent", "x"},
	} {
		kind, value := parseCollaboratorTo(testCase.in)
		if kind != testCase.kind || value != testCase.value {
			t.Fatalf("parseCollaboratorTo(%q) = %q/%q, want %q/%q", testCase.in, kind, value, testCase.kind, testCase.value)
		}
	}
}

func TestPickCollaboratorRun(t *testing.T) {
	base := time.Now().UTC()
	runs := map[string]*runParticipant{
		"old":   {RunID: "old", AgentID: "coder", Status: "completed", StartedAt: base},
		"live":  {RunID: "live", AgentID: "coder", Status: "running", StartedAt: base.Add(time.Second)},
		"other": {RunID: "other", AgentID: "writer", Status: "running", StartedAt: base.Add(2 * time.Second)},
	}
	// An agent address prefers the live run over the finished one.
	if got := pickCollaboratorRun("agent", "coder", runs); got.RunID != "live" {
		t.Fatalf("expected the live run of the agent, got %+v", got)
	}
	// Only finished runs → the latest one, with its explicit status (the
	// caller reports "finished, not addressable").
	runs["live"].Status = "failed"
	if got := pickCollaboratorRun("agent", "coder", runs); got.RunID != "live" || got.Status != "failed" {
		t.Fatalf("expected the latest terminal run with status, got %+v", got)
	}
	// A run address matches exactly.
	if got := pickCollaboratorRun("run", "other", runs); got.RunID != "other" {
		t.Fatalf("expected the exact run, got %+v", got)
	}
	// No such collaborator → nil, not a guess.
	if pickCollaboratorRun("agent", "nobody", runs) != nil {
		t.Fatal("an unknown collaborator must resolve to nil")
	}
}

func TestNormalizeAskPayload(t *testing.T) {
	if got := normalizeAskPayload("hello\n"); got != "hello" {
		t.Fatalf("string payload stays verbatim (trimmed): %q", got)
	}
	if got := normalizeAskPayload(map[string]any{"api_change": "x"}); got != `{"api_change":"x"}` {
		t.Fatalf("structured payload becomes canonical JSON: %q", got)
	}
	if got := normalizeAskPayload(nil); got != "" {
		t.Fatalf("nil payload is empty: %q", got)
	}
	long := strings.Repeat("x", 5000)
	if got := normalizeAskPayload(long); len(got) != 4096 {
		t.Fatalf("payload bounded to 4096, got %d", len(got))
	}
}

func TestAgentMessageFraming(t *testing.T) {
	msg := AgentMessage{RequestID: "req-abc", FromRunID: "run-1", FromAgentID: "coder-01", FromRole: "Coder", Type: "compatibility.check", Payload: `{"api_change":"x"}`, Mode: "wait"}
	framed := agentMessageFraming(msg)
	for _, want := range []string{"req-abc", "answer_agent", "compatibility.check", "Coder", "waiting"} {
		if !strings.Contains(framed, want) {
			t.Fatalf("framing missing %q:\n%s", want, framed)
		}
	}
	async := agentMessageFraming(AgentMessage{RequestID: "req-x", FromRunID: "run-1", Type: "data.share", Payload: "hi", Mode: "async"})
	if !strings.Contains(async, "If a reply is expected") {
		t.Fatalf("async framing must offer answer_agent:\n%s", async)
	}
	reply := agentReplyFraming(AgentReply{RequestID: "req-abc", FromRunID: "run-1", FromAgentID: "coder-01", Payload: "no"})
	if !strings.Contains(reply, "req-abc") || !strings.Contains(reply, "coder-01") || !strings.Contains(reply, "no") {
		t.Fatalf("reply framing lost identity:\n%s", reply)
	}
}

// The relay ask-run is woken purely by its prompt: it must carry the answer
// handle (request_id), the original run to ground on, and the Q&A-only
// framing — or the woken agent would treat the ask as new work.
// The continue run is woken purely by its prompt: it must carry the
// request, the original run to ground on, and the resume framing — the
// run's completion message IS the reply (no answer_agent handles).
func TestAgentContinuePrompt(t *testing.T) {
	msg := AgentMessage{RequestID: "req-relay", FromRunID: "run-lead", FromAgentID: "writer-01", Type: "compatibility.check", Payload: `{"api_change":"x"}`}
	prompt := agentContinuePrompt(msg, "run-coder/original", "task-9")
	for _, want := range []string{"run-coder/original", "task-9", "writer-01", "compatibility.check", "trace_read run_id=run-coder/original"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("continue prompt missing %q:\n%s", want, prompt)
		}
	}
	// No answer_agent in the continue semantics: the completion message is
	// the reply, delivered by the workflow machinery.
	if strings.Contains(prompt, "answer_agent") {
		t.Fatalf("continue prompt must not route through answer_agent:\n%s", prompt)
	}
}

// The late-answer follow-up is woken purely by its prompt: it must say WHY
// it runs (the predecessor died waiting), carry the answer itself, and
// forbid re-asking the resolved question — otherwise the successor restarts
// the very conversation that orphaned this answer.
func TestAgentLateReplyPrompt(t *testing.T) {
	msg := AgentMessage{RequestID: "req-8f993c", FromRunID: "run-lead/plan/01-fix", FromAgentID: "lead-agent", FromTaskID: "task-42", Payload: `{"q":1}`}
	prompt := agentLateReplyPrompt(msg, `{"diagnosis":"flapping","action":"fix"}`)
	for _, want := range []string{"run-lead/plan/01-fix", "task-42", "lead-agent", "req-8f993c", `{"diagnosis":"flapping","action":"fix"}`, "finished before the answer arrived", "Do not re-ask"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("late-reply prompt missing %q:\n%s", want, prompt)
		}
	}
}

// The channel notice is for the HUMAN who started the chain: it must say why
// an agent-to-agent answer is in their channels, what happens next, and mark
// the payload as agent-addressed so its instructions do not read as orders.
func TestLateReplyNoticeText(t *testing.T) {
	msg := AgentMessage{RequestID: "req-8f993c", FromRunID: "run-lead/plan/01-fix", FromAgentID: "lead-agent", Payload: `{"attempts":[]}`}
	notice := lateReplyNoticeText(msg, "req-8f993c", `{"attempts":[]}`, "run-lead/plan/01-fix/late/01")
	for _, want := range []string{"req-8f993c", "run-lead/plan/01-fix", "run-lead/plan/01-fix/late/01", "Запущен наследник", "адресованы агентам", `{"attempts":[]}`} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice missing %q:\n%s", want, notice)
		}
	}
	// Without a spawned follow-up the notice must say the answer is FYI only.
	quiet := lateReplyNoticeText(msg, "req-8f993c", `{"attempts":[]}`, "")
	if !strings.Contains(quiet, "Наследник не запускался") || strings.Contains(quiet, "/late/01") {
		t.Fatalf("no-followup notice must not mention a follow-up:\n%s", quiet)
	}
}

func TestCapLabel(t *testing.T) {
	if got := capLabel("short", 40); got != "short" {
		t.Fatalf("short label untouched: %q", got)
	}
	long := strings.Repeat("x", 60)
	if got := capLabel(long, 40); len([]rune(got)) != 40 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long label capped with ellipsis: %q", got)
	}
}

func TestCollabToolsVersionFiltered(t *testing.T) {
	tools := KernelTools()
	all := withoutCollabTools(tools)
	if len(all) != len(tools)-3 {
		t.Fatalf("expected exactly the three collaboration tools stripped, %d → %d", len(tools), len(all))
	}
	for _, tool := range all {
		switch tool.Name {
		case "get_collaborators", "ask_agent", "answer_agent":
			t.Fatalf("%s survived the filter", tool.Name)
		}
	}
	for _, name := range []string{"get_collaborators", "ask_agent", "answer_agent"} {
		if !policySafeTool(name) {
			t.Fatalf("%s must be policy-safe (an approval round-trip defeats the collaboration)", name)
		}
	}
}
