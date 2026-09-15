package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

func evidenceCommit(eventID, claimID, evidenceID string) cognition.Commit {
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	event := protocol.Event{EventID: eventID, TransactionTime: at, ValidTime: at, Type: "claim.candidate", Payload: map[string]any{"claim_id": claimID}, Provenance: map[string]any{"source": "ingestion"}}
	event.ApplyDefaults(at)
	claim := cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: claimID, Proposition: "integration claim", Confidence: 0.82, Status: cognition.ClaimCandidate, CreatedEvent: eventID, ValidFrom: at}
	return cognition.Commit{Event: event, Claim: claim, Evidence: []string{evidenceID}}
}

// TestClaimEvidencePersists verifies the M13 provenance chain in PostgreSQL:
// a claim cites a pre-existing observation event, unknown evidence is
// rejected, and the chain reads back intact.
func TestClaimEvidencePersists(t *testing.T) {
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
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000002_claims.up.sql", "../../../migrations/000003_claim_guards.up.sql", "../../../migrations/000016_claim_evidence.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC()
	observation := protocol.Event{EventID: newTestUUID(), TransactionTime: at, ValidTime: at, EpisodeID: newTestUUID(), Type: "world.observation", Payload: map[string]any{"world_id": "w", "observation_type": "file_content"}, Provenance: map[string]any{"source": "ingestion"}}
	observation.ApplyDefaults(at)
	if err = store.Append(ctx, observation); err != nil {
		t.Fatal(err)
	}
	eventID, claimID := newTestUUID(), newTestUUID()
	if err = store.CommitClaim(ctx, evidenceCommit(eventID, claimID, observation.EventID)); err != nil {
		t.Fatalf("commit claim with evidence: %v", err)
	}
	evidence, err := store.ListClaimEvidence(ctx, claimID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0] != observation.EventID {
		t.Fatalf("evidence = %v, want [%s]", evidence, observation.EventID)
	}
	// Unknown evidence events are rejected so claims cannot cite facts that
	// were never observed.
	unknown := evidenceCommit(newTestUUID(), newTestUUID(), newTestUUID())
	if err = store.CommitClaim(ctx, unknown); !errors.Is(err, cognition.ErrEvidenceNotFound) {
		t.Fatalf("unknown evidence error = %v", err)
	}
	if _, err = store.ListClaimEvidence(ctx, newTestUUID()); !errors.Is(err, cognition.ErrClaimNotFound) {
		t.Fatalf("missing claim error = %v", err)
	}
}
