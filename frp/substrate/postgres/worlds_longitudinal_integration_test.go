package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

// TestWorldScopedClaimBase verifies the longitudinal-memory read path on real
// PostgreSQL: evidence-backed claims, episode-authored claims, their exclusions,
// and the world→episodes mapping procedures reuse.
func TestWorldScopedClaimBase(t *testing.T) {
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
	for _, migration := range []string{
		"../../../migrations/000001_event_store.up.sql",
		"../../../migrations/000002_claims.up.sql",
		"../../../migrations/000005_objectives.up.sql",
		"../../../migrations/000016_claim_evidence.up.sql",
	} {
		if err = store.Migrate(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	// A unique world per run: the shared integration database keeps data from
	// earlier runs, and the longitudinal scope must be judged per world.
	worldID := newTestUUID()
	otherEpisode := newTestUUID()
	event := func(id, episode, branch, eventType string, payload map[string]any) protocol.Event {
		value := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: now, ValidTime: now, EpisodeID: episode, BranchID: branch, Type: eventType, Payload: payload, Provenance: map[string]any{"source": "integration-test"}}
		value.ApplyDefaults(now)
		return value
	}
	// World episode: observation carrying the world id, an evidence-backed
	// claim, and a model-authored claim (evidence-free, episode path).
	worldEpisode := newTestUUID()
	if err = store.Append(ctx, event(newTestUUID(), worldEpisode, newTestUUID(), "world.observation", map[string]any{"world_id": worldID, "observation_type": "directory_listing"})); err != nil {
		t.Fatal(err)
	}
	observationID := newTestUUID()
	if err = store.Append(ctx, event(observationID, worldEpisode, newTestUUID(), "world.observation", map[string]any{"world_id": worldID, "observation_type": "git_status"})); err != nil {
		t.Fatal(err)
	}
	claim := func(id, proposition, createdEvent string, evidence []string) cognition.Commit {
		return cognition.Commit{Event: event(createdEvent, worldEpisode, newTestUUID(), "claim.candidate", map[string]any{"claim_id": id}), Claim: cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: id, Proposition: proposition, Confidence: 0.7, Status: cognition.ClaimCandidate, CreatedEvent: createdEvent, ValidFrom: now}, Evidence: evidence}
	}
	if err = store.CommitClaim(ctx, claim(newTestUUID(), "evidence-backed world claim", newTestUUID(), []string{observationID})); err != nil {
		t.Fatal(err)
	}
	modelClaimID := newTestUUID()
	if err = store.CommitClaim(ctx, claim(modelClaimID, "model-authored world claim", newTestUUID(), nil)); err != nil {
		t.Fatal(err)
	}
	// Same-world claim that is later refuted stays in the store's answer (the
	// renderer filters status); a foreign-episode claim must never be in it.
	refutedID := newTestUUID()
	if err = store.CommitClaim(ctx, claim(refutedID, "soon refuted world claim", newTestUUID(), []string{observationID})); err != nil {
		t.Fatal(err)
	}
	refuteAt := now.Add(time.Minute)
	refutationEvent := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: refuteAt, ValidTime: refuteAt, EpisodeID: worldEpisode, BranchID: newTestUUID(), Type: "claim.refuted", Payload: map[string]any{"claim_id": refutedID}, Provenance: map[string]any{"source": "integration-test"}}
	refutationEvent.ApplyDefaults(refuteAt)
	_, err = store.TransitionClaim(ctx, cognition.Transition{Event: refutationEvent, ClaimID: refutedID, ToStatus: cognition.ClaimRefuted, ValidAt: refuteAt})
	if err != nil {
		t.Fatal(err)
	}
	foreignID := newTestUUID()
	foreignEventID := newTestUUID()
	if err = store.CommitClaim(ctx, cognition.Commit{Event: event(foreignEventID, otherEpisode, newTestUUID(), "claim.candidate", map[string]any{"claim_id": foreignID}), Claim: cognition.Claim{Protocol: protocol.Name, Version: protocol.Version, ClaimID: foreignID, Proposition: "claim outside the world", Confidence: 0.7, Status: cognition.ClaimCandidate, CreatedEvent: foreignEventID, ValidFrom: now}}); err != nil {
		t.Fatal(err)
	}
	// An effect-backed claim cites a world.effect event: episode experience, not
	// durable world knowledge — it must stay out of the longitudinal base even
	// though its evidence carries the world id.
	effectID := newTestUUID()
	if err = store.Append(ctx, event(effectID, worldEpisode, newTestUUID(), "world.effect", map[string]any{"world_id": worldID, "effect_type": "file.patched"})); err != nil {
		t.Fatal(err)
	}
	effectClaimID := newTestUUID()
	if err = store.CommitClaim(ctx, claim(effectClaimID, "после правок go test проходит зелёно", newTestUUID(), []string{effectID})); err != nil {
		t.Fatal(err)
	}

	claims, err := store.ListClaimsByWorld(ctx, worldID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, value := range claims {
		ids[value.ClaimID] = true
	}
	if len(ids) != 3 {
		t.Fatalf("expected exactly the three world claims, got %v", ids)
	}
	if !ids[modelClaimID] || !ids[refutedID] {
		t.Fatalf("evidence and episode paths must both contribute: %v", ids)
	}
	for _, value := range claims {
		if value.ClaimID == foreignID {
			t.Fatal("foreign-episode claim leaked into the world claim base")
		}
		if value.ClaimID == effectClaimID {
			t.Fatal("effect-backed claim leaked into the world claim base")
		}
	}
	episodes, err := store.ListWorldEpisodes(ctx, worldID)
	if err != nil {
		t.Fatal(err)
	}
	if len(episodes) != 1 || episodes[0] != worldEpisode {
		t.Fatalf("unexpected world episodes: %v", episodes)
	}
	// EpisodeObjective feeds the procedure trigger seeding: the lookup must
	// resolve the episode's objective and report absence cleanly.
	objectiveID := newTestUUID()
	startedAt := now.Add(-time.Hour)
	startedEvent := event(newTestUUID(), worldEpisode, newTestUUID(), "episode.started", map[string]any{"objective_id": objectiveID, "world_id": worldID})
	startedEvent.ValidTime, startedEvent.TransactionTime = startedAt, startedAt
	if err = store.CreateObjective(ctx, objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: objectiveID, EpisodeID: worldEpisode, Text: "найти падающий тест", SuccessConditions: []string{}}, startedEvent); err != nil {
		t.Fatal(err)
	}
	gotObjective, err := store.EpisodeObjective(ctx, worldEpisode)
	if err != nil || gotObjective.ObjectiveID != objectiveID || gotObjective.Text != "найти падающий тест" {
		t.Fatalf("EpisodeObjective mismatch: %#v %v", gotObjective, err)
	}
	if _, err = store.EpisodeObjective(ctx, otherEpisode); !errors.Is(err, objective.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for objective-less episode, got %v", err)
	}
}
