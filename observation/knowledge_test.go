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

func promotedEvent(eventID, knowledgeID string, at time.Time, data map[string]any) Event {
	base := map[string]any{"knowledge_id": knowledgeID, "scope_kind": "organization", "reason": "repeated across projects"}
	for key, value := range data {
		base[key] = value
	}
	return Event{
		Schema: Schema, EventID: eventID, OccurredAt: at,
		Source:  Source{ID: "temporality-manual", Integration: "temporality"},
		Context: Context{Project: "repo-a", Actor: Actor{ID: "ruslan", Type: "human"}},
		Type:    "knowledge.promoted",
		Data:    base,
	}
}

func TestKnowledgeScopeDefaultsToProjectAndPromoteWidens(t *testing.T) {
	base := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	events := []Event{
		proposalEvent("e1", "auto/abc", "The gatekeeper CLI requires the --out flag", base, nil),
		promotedEvent("e2", "auto/abc", base.Add(time.Minute), map[string]any{"scope_kind": "org_unit", "scope_id": "dept-dev"}),
		// Lifecycle continues in another project once the scope is widened.
		{
			Schema: Schema, EventID: "e3", OccurredAt: base.Add(2 * time.Minute),
			Source:  Source{ID: "kernel", Integration: "temporality-agent-kernel"},
			Context: Context{Project: "repo-b", Actor: Actor{ID: "agent", Type: "agent"}},
			Type:    "knowledge.confirmed", Data: map[string]any{"knowledge_id": "auto/abc"},
		},
	}
	knowledge, err := ProjectKnowledge(events)
	if err != nil {
		t.Fatalf("promoted knowledge lifecycle must project: %v", err)
	}
	if len(knowledge) != 1 {
		t.Fatalf("expected one knowledge node, got %d", len(knowledge))
	}
	item := knowledge[0]
	if item.ScopeKind != "org_unit" || item.ScopeID != "dept-dev" {
		t.Fatalf("scope after promote = %q/%q", item.ScopeKind, item.ScopeID)
	}
	if item.PromotedBy.ID != "ruslan" || item.PromotedAt == nil || !item.PromotedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("promote provenance = %v at %v", item.PromotedBy, item.PromotedAt)
	}
	if item.State != "confirmed" {
		t.Fatalf("cross-project lifecycle must apply, state = %q", item.State)
	}
	promoteSeen := false
	for _, entry := range item.History {
		if entry.Type == "knowledge.promoted" {
			promoteSeen = true
			if entry.Rule != "scope-promotion.v1" || entry.Reason != "repeated across projects" {
				t.Fatalf("promote transition = %+v", entry)
			}
		}
	}
	if !promoteSeen {
		t.Fatal("promote must be recorded in history")
	}
}

func TestProjectKnowledgeStillRejectsProjectBoundaryCrossing(t *testing.T) {
	base := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	events := []Event{
		proposalEvent("e1", "auto/abc", "The gatekeeper CLI requires the --out flag", base, nil),
		{
			Schema: Schema, EventID: "e2", OccurredAt: base.Add(time.Minute),
			Source:  Source{ID: "kernel", Integration: "temporality-agent-kernel"},
			Context: Context{Project: "repo-b", Actor: Actor{ID: "agent", Type: "agent"}},
			Type:    "knowledge.confirmed", Data: map[string]any{"knowledge_id": "auto/abc"},
		},
	}
	_, err := ProjectKnowledge(events)
	if err == nil || !strings.Contains(err.Error(), "crosses project boundary") {
		t.Fatalf("project-scoped boundary crossing must still be rejected, got: %v", err)
	}
}

func TestValidateKnowledgeScope(t *testing.T) {
	base := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	valid := []Event{
		proposalEvent("v1", "k1", "p", base, nil),
		proposalEvent("v2", "k2", "p", base, nil),
	}
	valid[1].Data["scope_kind"] = "organization"
	for _, event := range valid {
		if err := event.Validate(); err != nil {
			t.Fatalf("%s must validate: %v", event.EventID, err)
		}
	}
	missingUnit := proposalEvent("x1", "k3", "p", base, nil)
	missingUnit.Data["scope_kind"] = "org_unit"
	if err := missingUnit.Validate(); err == nil || !strings.Contains(err.Error(), "requires data.scope_id") {
		t.Fatalf("org_unit without scope_id must be rejected, got: %v", err)
	}
	badKind := proposalEvent("x2", "k4", "p", base, nil)
	badKind.Data["scope_kind"] = "galaxy"
	if err := badKind.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported data.scope_kind") {
		t.Fatalf("unknown scope_kind must be rejected, got: %v", err)
	}
	noReason := promotedEvent("x3", "k5", base, nil)
	delete(noReason.Data, "reason")
	if err := noReason.Validate(); err == nil || !strings.Contains(err.Error(), "requires data.reason") {
		t.Fatalf("promote without reason must be rejected, got: %v", err)
	}
	narrowing := promotedEvent("x4", "k6", base, map[string]any{"scope_kind": "project"})
	if err := narrowing.Validate(); err == nil || !strings.Contains(err.Error(), "org_unit or organization") {
		t.Fatalf("promote to project scope must be rejected, got: %v", err)
	}
}

func TestExtractionMarkersValidateAndProjectAway(t *testing.T) {
	base := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	marker := func(eventID, eventType string, at time.Time) Event {
		return Event{
			Schema: Schema, EventID: eventID, OccurredAt: at,
			Source:  Source{ID: "kernel", Integration: "temporality-agent-kernel"},
			Context: Context{Project: "repo-a", Run: "run-1", Actor: Actor{ID: "agent", Type: "agent"}},
			Type:    eventType,
			Data:    map[string]any{"extraction_id": "extraction-abc", "candidates_count": 1},
		}
	}
	markers := []Event{
		marker("m1", "knowledge.extraction.started", base),
		marker("m2", "knowledge.extraction.completed", base.Add(time.Second)),
		marker("m3", "knowledge.extraction.failed", base.Add(2*time.Second)),
	}
	for _, event := range markers {
		if err := event.Validate(); err != nil {
			t.Fatalf("%s must validate: %v", event.Type, err)
		}
	}
	broken := marker("m4", "knowledge.extraction.started", base)
	broken.Context.Project = ""
	if err := broken.Validate(); err == nil {
		t.Fatal("extraction markers without a project must be rejected")
	}

	// Markers carry no knowledge_id, so the projection must skip them instead
	// of failing the whole knowledge stream.
	events := append([]Event{}, markers...)
	events = append(events, proposalEvent("e1", "ext/abc", "The build requires a pre-existing out directory.", base.Add(3*time.Second), nil))
	knowledge, err := ProjectKnowledge(events)
	if err != nil {
		t.Fatalf("extraction markers must not break the projection: %v", err)
	}
	if len(knowledge) != 1 || knowledge[0].ID != "ext/abc" {
		t.Fatalf("expected exactly the proposed knowledge, got %#v", knowledge)
	}
}
