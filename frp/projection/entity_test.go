package projection

import (
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/entity"
	"github.com/temporality-project/temporality/frp/protocol"
)

func tripleClaim(id, subject, predicate, object string, confidence float32, status cognition.ClaimStatus) cognition.Claim {
	return cognition.Claim{
		Protocol:     protocol.Name,
		Version:      protocol.Version,
		ClaimID:      id,
		Proposition:  subject + " " + predicate + " " + object,
		Confidence:   confidence,
		Status:       status,
		CreatedEvent: "event-" + id,
		ValidFrom:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Subject:      subject,
		Predicate:    predicate,
		Object:       object,
	}
}

func TestBuildEntitiesProjectsTriples(t *testing.T) {
	claims := []cognition.Claim{
		tripleClaim("c1", "module:demo", "declared_in", "file:go.mod", 0.9, cognition.ClaimCandidate),
		tripleClaim("c2", "module:demo", "depends_on", "library:github.com/x/y", 0.8, cognition.ClaimSupported),
		tripleClaim("c3", "module:demo", "depends_on", "library:github.com/x/z", 0.7, cognition.ClaimCandidate),
	}
	entities, relations := BuildEntities(claims)
	if len(entities) != 4 {
		t.Fatalf("entities = %d, want 4", len(entities))
	}
	if len(relations) != 3 {
		t.Fatalf("relations = %d, want 3", len(relations))
	}
	// module:demo is mentioned by all three claims; confidence is the max.
	var demo *entity.Entity
	for i := range entities {
		if entities[i].Type == entity.TypeModule && entities[i].Name == "demo" {
			demo = &entities[i]
		}
	}
	if demo == nil {
		t.Fatal("module:demo not projected")
	}
	if demo.MentionCount != 3 {
		t.Errorf("mention count = %d, want 3", demo.MentionCount)
	}
	if demo.Confidence != 0.9 {
		t.Errorf("confidence = %v, want 0.9", demo.Confidence)
	}
	if demo.EntityID != (entity.Ref{Type: entity.TypeModule, Name: "demo"}).ID() {
		t.Errorf("entity id mismatch: %s", demo.EntityID)
	}
}

func TestBuildEntitiesSkipsInactiveAndTripleLess(t *testing.T) {
	claims := []cognition.Claim{
		tripleClaim("c1", "module:demo", "declared_in", "file:go.mod", 0.9, cognition.ClaimCandidate),
		tripleClaim("c2", "module:demo", "depends_on", "library:x", 0.8, cognition.ClaimRefuted),
		tripleClaim("c3", "module:demo", "depends_on", "library:y", 0.8, cognition.ClaimSuperseded),
		{Protocol: protocol.Name, Version: protocol.Version, ClaimID: "c4", Proposition: "no triple", Status: cognition.ClaimCandidate, CreatedEvent: "e4", ValidFrom: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	entities, relations := BuildEntities(claims)
	if len(entities) != 2 {
		t.Fatalf("entities = %d, want 2 (only candidate triple)", len(entities))
	}
	if len(relations) != 1 {
		t.Fatalf("relations = %d, want 1", len(relations))
	}
	if relations[0].ClaimID != "c1" {
		t.Errorf("relation claim = %s, want c1", relations[0].ClaimID)
	}
}

func TestBuildEntitiesDeterministic(t *testing.T) {
	claims := []cognition.Claim{
		tripleClaim("c1", "module:demo", "depends_on", "library:a", 0.9, cognition.ClaimSupported),
		tripleClaim("c2", "module:other", "depends_on", "library:b", 0.9, cognition.ClaimSupported),
	}
	e1, r1 := BuildEntities(claims)
	e2, r2 := BuildEntities(claims)
	if len(e1) != len(e2) || len(r1) != len(r2) {
		t.Fatal("rebuild changed projection size")
	}
	for i := range e1 {
		if e1[i] != e2[i] {
			t.Fatalf("entity %d differs between rebuilds", i)
		}
	}
	for i := range r1 {
		if r1[i] != r2[i] {
			t.Fatalf("relation %d differs between rebuilds", i)
		}
	}
}

func TestBuildEntitiesDeduplicatesReassertions(t *testing.T) {
	claims := []cognition.Claim{
		tripleClaim("c1", "repository:repo", "on_branch", "branch:main", 0.6, cognition.ClaimCandidate),
		tripleClaim("c2", "repository:repo", "on_branch", "branch:main", 0.9, cognition.ClaimSupported),
	}
	entities, relations := BuildEntities(claims)
	if len(entities) != 2 {
		t.Fatalf("entities = %d, want 2", len(entities))
	}
	if len(relations) != 2 {
		t.Fatalf("relations = %d, want 2 (one per asserting claim)", len(relations))
	}
	for _, e := range entities {
		if e.MentionCount != 2 {
			t.Errorf("%s mention count = %d, want 2", e.Type+":"+e.Name, e.MentionCount)
		}
		if e.Confidence != 0.9 {
			t.Errorf("%s confidence = %v, want 0.9", e.Type+":"+e.Name, e.Confidence)
		}
	}
}
