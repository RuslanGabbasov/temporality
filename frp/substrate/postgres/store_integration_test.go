package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

func TestEventSurvivesReopenAndCannotBeMutated(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(ctx, "../../../migrations/000001_event_store.up.sql"); err != nil {
		store.Close()
		t.Fatal(err)
	}

	now := time.Now().UTC()
	event := protocol.Event{
		Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(),
		TransactionTime: now, ValidTime: now, Type: "episode.started",
		Payload: map[string]any{"test": true}, Provenance: map[string]any{"source": "integration-test"},
	}
	if err = store.Append(ctx, event); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Append(ctx, event); err == nil {
		store.Close()
		t.Fatal("duplicate event was accepted")
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE events SET type='episode.failed' WHERE event_id=$1", event.EventID); err == nil {
		pool.Close()
		store.Close()
		t.Fatal("append-only trigger allowed event mutation")
	}
	pool.Close()
	store.Close()

	reopened, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(ctx, event.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventID != event.EventID || got.Type != event.Type {
		t.Fatalf("unexpected event after reopen: %#v", got)
	}
	listed, err := reopened.List(ctx, substrate.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range listed {
		if candidate.EventID == event.EventID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("persisted event absent from replay stream")
	}
}

func TestPostgresCursorPagination(t *testing.T) {
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
	if err = store.Migrate(ctx, "../../../migrations/000001_event_store.up.sql"); err != nil {
		t.Fatal(err)
	}
	episodeID := newTestUUID()
	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		event := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), EpisodeID: episodeID, TransactionTime: base.Add(time.Duration(i) * time.Nanosecond), ValidTime: base.Add(time.Duration(i) * time.Nanosecond), Type: "metric.recorded", Payload: map[string]any{"index": i}, Provenance: map[string]any{"source": "integration-test"}}
		if err = store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListPage(ctx, substrate.PageRequest{Filter: substrate.EventFilter{EpisodeID: episodeID}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	second, err := store.ListPage(ctx, substrate.PageRequest{Filter: substrate.EventFilter{EpisodeID: episodeID}, Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.NextCursor != "" {
		t.Fatalf("unexpected second page: %#v", second)
	}
	if first.Events[1].EventID == second.Events[0].EventID {
		t.Fatal("cursor returned a duplicate event")
	}
}

func TestClaimCommitIsAtomic(t *testing.T) {
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
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000002_claims.up.sql", "../../../migrations/000003_claim_guards.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}

	base := claimCommit("Base claim", nil)
	if err = store.CommitClaim(ctx, base); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetClaim(ctx, base.Claim.ClaimID)
	if err != nil || got.Proposition != base.Claim.Proposition {
		t.Fatalf("unexpected persisted claim: %#v, %v", got, err)
	}

	invalid := claimCommit("Derived claim", []cognition.ClaimRelation{{DestinationClaim: newTestUUID(), Type: cognition.RelationSupports, Weight: 0.8}})
	invalid.Relations[0].SourceClaim = invalid.Claim.ClaimID
	invalid.Relations[0].EvidenceEvent = invalid.Event.EventID
	if err = store.CommitClaim(ctx, invalid); err == nil {
		t.Fatal("commit with missing destination claim succeeded")
	}
	if _, err = store.Get(ctx, invalid.Event.EventID); err != substrate.ErrNotFound {
		t.Fatalf("event was not rolled back: %v", err)
	}
	if _, err = store.GetClaim(ctx, invalid.Claim.ClaimID); err != cognition.ErrClaimNotFound {
		t.Fatalf("claim was not rolled back: %v", err)
	}
}

func TestClaimLifecycleAndGuard(t *testing.T) {
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
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000002_claims.up.sql", "../../../migrations/000003_claim_guards.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	commit := claimCommit("Lifecycle claim", nil)
	if err = store.CommitClaim(ctx, commit); err != nil {
		t.Fatal(err)
	}
	confidence := float32(0.95)
	at := commit.Claim.ValidFrom.Add(time.Second)
	transitionEvent := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: at, ValidTime: at, Type: "claim.supported", Payload: map[string]any{"claim_id": commit.Claim.ClaimID}, Provenance: map[string]any{"source": "integration-test"}}
	updated, err := store.TransitionClaim(ctx, cognition.Transition{Event: transitionEvent, ClaimID: commit.Claim.ClaimID, ToStatus: cognition.ClaimSupported, Confidence: &confidence, ValidAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != cognition.ClaimSupported || updated.Confidence != confidence || updated.ValidTo != nil {
		t.Fatalf("unexpected supported claim: %#v", updated)
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE claims SET confidence=0 WHERE claim_id=$1", commit.Claim.ClaimID); err == nil {
		pool.Close()
		t.Fatal("direct claim update bypassed canonical transition")
	}
	pool.Close()

	refutedAt := at.Add(time.Second)
	refutedEvent := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: refutedAt, ValidTime: refutedAt, Type: "claim.refuted", Payload: map[string]any{}, Provenance: map[string]any{}}
	updated, err = store.TransitionClaim(ctx, cognition.Transition{Event: refutedEvent, ClaimID: commit.Claim.ClaimID, ToStatus: cognition.ClaimRefuted, ValidAt: refutedAt})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ValidTo == nil || !updated.ValidTo.Equal(refutedAt) {
		t.Fatalf("terminal validity not closed: %#v", updated)
	}
	invalidEvent := refutedEvent
	invalidEvent.EventID = newTestUUID()
	invalidEvent.Type = "claim.supported"
	invalidEvent.ValidTime = refutedAt.Add(time.Second)
	if _, err = store.TransitionClaim(ctx, cognition.Transition{Event: invalidEvent, ClaimID: commit.Claim.ClaimID, ToStatus: cognition.ClaimSupported, ValidAt: invalidEvent.ValidTime}); err == nil {
		t.Fatal("transition from terminal state succeeded")
	}
	if _, err = store.Get(ctx, invalidEvent.EventID); err != substrate.ErrNotFound {
		t.Fatalf("invalid transition event was persisted: %v", err)
	}
}

func claimCommit(proposition string, relations []cognition.ClaimRelation) cognition.Commit {
	now := time.Now().UTC()
	eventID, claimID := newTestUUID(), newTestUUID()
	return cognition.Commit{Event: protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: eventID, TransactionTime: now, ValidTime: now, Type: "claim.candidate", Payload: map[string]any{}, Provenance: map[string]any{"source": "integration-test"}}, Claim: cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: claimID, Proposition: proposition, Confidence: 0.5, Status: cognition.ClaimCandidate, CreatedEvent: eventID, ValidFrom: now}, Relations: relations}
}

func newTestUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	h := hex.EncodeToString(value[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
