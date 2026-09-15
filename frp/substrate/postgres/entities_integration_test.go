package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/entity"
	"github.com/temporality-project/temporality/frp/projection"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

// TestEntitiesProjectionPersists verifies the M14 flow in PostgreSQL: claims
// with triples persist their subject/predicate/object, the entity projection
// replaces atomically, and entities/relations read back with filters.
func TestEntitiesProjectionPersists(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000002_claims.up.sql", "../../../migrations/000003_claim_guards.up.sql", "../../../migrations/000016_claim_evidence.up.sql", "../../../migrations/000017_entities.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	// Commit two triple-bearing claims plus one triple-less claim. Entity
	// names are unique per run: the suite shares the database and
	// ListClaims/ReplaceAllEntities are global, so the test must be idempotent
	// against rows left by earlier runs.
	module := entity.Ref{Type: entity.TypeModule, Name: "example.com/store-" + newTestUUID()}
	file := entity.Ref{Type: entity.TypeFile, Name: "go.mod"}
	dep := entity.Ref{Type: entity.TypeLibrary, Name: "github.com/x/y"}
	commitTriple := func(claimID string, subject, predicate, object string) {
		at := time.Now().UTC()
		event := protocol.Event{EventID: newTestUUID(), TransactionTime: at, ValidTime: at, Type: "claim.candidate", Payload: map[string]any{"claim_id": claimID}, Provenance: map[string]any{"source": "ingestion"}}
		event.ApplyDefaults(at)
		claim := cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: claimID, Proposition: subject + " " + predicate + " " + object, Confidence: 0.9, Status: cognition.ClaimCandidate, CreatedEvent: event.EventID, ValidFrom: at, Subject: subject, Predicate: predicate, Object: object}
		if err = store.CommitClaim(ctx, cognition.Commit{Event: event, Claim: claim}); err != nil {
			t.Fatal(err)
		}
	}
	first, second, third := newTestUUID(), newTestUUID(), newTestUUID()
	commitTriple(first, module.String(), entity.PredicateDeclaredIn, file.String())
	commitTriple(second, module.String(), entity.PredicateDependsOn, dep.String())
	at := time.Now().UTC()
	plainEvent := protocol.Event{EventID: newTestUUID(), TransactionTime: at, ValidTime: at, Type: "claim.candidate", Payload: map[string]any{"claim_id": third}, Provenance: map[string]any{"source": "ingestion"}}
	plainEvent.ApplyDefaults(at)
	if err = store.CommitClaim(ctx, cognition.Commit{Event: plainEvent, Claim: cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: third, Proposition: "no triple here", Confidence: 0.5, Status: cognition.ClaimCandidate, CreatedEvent: plainEvent.EventID, ValidFrom: at}}); err != nil {
		t.Fatal(err)
	}

	// Triples persist and read back. The suite shares the database, so scope
	// the assertion to this test's own module subject.
	claims, err := store.ListClaims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var triples int
	for _, claim := range claims {
		if claim.HasTriple() && claim.Subject == module.String() {
			triples++
		}
	}
	if triples != 2 {
		t.Fatalf("triple claims = %d, want 2", triples)
	}

	// Build and persist the projection from this test's claims only.
	own := make([]cognition.Claim, 0, 3)
	for _, claim := range claims {
		if claim.Subject == module.String() || claim.ClaimID == third {
			own = append(own, claim)
		}
	}
	entities, relations := projection.BuildEntities(own)
	if len(entities) != 3 || len(relations) != 2 {
		t.Fatalf("projection = %d entities / %d relations", len(entities), len(relations))
	}
	if err = store.ReplaceAllEntities(ctx, entities, relations); err != nil {
		t.Fatal(err)
	}
	// Replacing again is idempotent.
	if err = store.ReplaceAllEntities(ctx, entities, relations); err != nil {
		t.Fatal(err)
	}

	modules, err := store.ListEntities(ctx, entity.Filter{Type: entity.TypeModule})
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 1 || modules[0].Name != module.Name || modules[0].MentionCount != 2 {
		t.Fatalf("modules = %+v", modules)
	}
	got, err := store.GetEntity(ctx, modules[0].EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EntityID != modules[0].EntityID {
		t.Fatalf("entity mismatch: %+v", got)
	}
	if _, err = store.GetEntity(ctx, newTestUUID()); !errors.Is(err, entity.ErrEntityNotFound) {
		t.Fatalf("missing entity error = %v", err)
	}
	moduleRelations, err := store.ListEntityRelations(ctx, entity.RelationFilter{EntityID: modules[0].EntityID})
	if err != nil {
		t.Fatal(err)
	}
	if len(moduleRelations) != 2 {
		t.Fatalf("module relations = %+v", moduleRelations)
	}
	all, err := store.ListEntityRelations(ctx, entity.RelationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("all relations = %+v", all)
	}
}
