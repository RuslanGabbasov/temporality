package observation

import (
	"strings"
	"testing"
	"time"
)

func proposalEvent(eventID, knowledgeID, proposition string, at time.Time, evidence []Evidence) Event {
	return Event{
		Schema: Schema, EventID: eventID, OccurredAt: at,
		Source:   Source{ID: "kernel", Integration: "temporality-agent-kernel"},
		Context:  Context{Project: "repo-a", Run: "run-1", Actor: Actor{ID: "agent", Type: "agent"}},
		Type:     "knowledge.proposed",
		Data:     map[string]any{"knowledge_id": knowledgeID, "proposition": proposition},
		Evidence: evidence,
	}
}

func transitionEvent(eventID, knowledgeID, eventType string, at time.Time) Event {
	return Event{
		Schema: Schema, EventID: eventID, OccurredAt: at,
		Source:  Source{ID: "kernel", Integration: "temporality-agent-kernel"},
		Context: Context{Project: "repo-a", Run: "run-2", Actor: Actor{ID: "qa", Type: "agent"}},
		Type:    eventType,
		Data:    map[string]any{"knowledge_id": knowledgeID},
	}
}

func TestProjectKnowledgeTreatsIdenticalReProposalAsIdempotent(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	events := []Event{
		proposalEvent("e1", "auto/abc", "Verification command `go test ./...` exited 0", base, []Evidence{{Ref: "op-1", Type: "execution"}}),
		proposalEvent("e2", "auto/abc", "Verification command `go test ./...` exited 0", base.Add(time.Minute), []Evidence{{Ref: "op-2", Type: "execution"}}),
		transitionEvent("e3", "auto/abc", "knowledge.confirmed", base.Add(2*time.Minute)),
		transitionEvent("e4", "auto/abc", "knowledge.used", base.Add(3*time.Minute)),
	}
	knowledge, err := ProjectKnowledge(events)
	if err != nil {
		t.Fatalf("identical re-proposal must not fail: %v", err)
	}
	if len(knowledge) != 1 {
		t.Fatalf("expected one knowledge node, got %d", len(knowledge))
	}
	item := knowledge[0]
	if item.State != "confirmed" {
		t.Fatalf("state = %q", item.State)
	}
	if item.ReuseCount != 1 {
		t.Fatalf("reuse count = %d", item.ReuseCount)
	}
	if len(item.History) != 4 {
		t.Fatalf("history entries = %d", len(item.History))
	}
	refs := map[string]bool{}
	for _, evidence := range item.Evidence {
		refs[evidence.Ref] = true
	}
	if !refs["op-1"] || !refs["op-2"] {
		t.Fatalf("evidence from both proposals must be retained: %v", item.Evidence)
	}
}

func TestProjectKnowledgeRejectsConflictingReProposal(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	events := []Event{
		proposalEvent("e1", "auto/abc", "Verification command `go test ./...` exited 0", base, nil),
		proposalEvent("e2", "auto/abc", "Build command `go build ./...` exited 0", base.Add(time.Minute), nil),
	}
	_, err := ProjectKnowledge(events)
	if err == nil || !strings.Contains(err.Error(), "different propositions") {
		t.Fatalf("conflicting re-proposal must be rejected, got: %v", err)
	}
}
