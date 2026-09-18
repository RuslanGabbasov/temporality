package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/runtime/step"
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

// TestStepClaimLifecyclePersists walks the pivot knowledge lifecycle through
// the real step pipeline in PostgreSQL: a cited fact is born supported with
// claim_evidence rows, a later confirm keeps valid_to open (unlike retirement),
// an already-supported confirm becomes a visible rejection, and hallucinated
// evidence fails the step atomically.
func TestStepClaimLifecyclePersists(t *testing.T) {
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
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000002_claims.up.sql", "../../../migrations/000003_claim_guards.up.sql", "../../../migrations/000004_frames.up.sql", "../../../migrations/000005_objectives.up.sql", "../../../migrations/000008_step.up.sql", "../../../migrations/000016_claim_evidence.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	agentID, episodeID := newTestUUID(), newTestUUID()
	current := frame.Frame{FrameID: newTestUUID(), AgentID: agentID, EpisodeID: episodeID, BranchID: newTestUUID(), ObjectiveID: newTestUUID(), AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "lifecycle"}, Attention: frame.Attention{Ambient: true, Deliberate: true}}
	current.ApplyDefaults()
	objectiveEvent := protocol.Event{EventID: newTestUUID(), TransactionTime: now, ValidTime: now, AgentID: agentID, EpisodeID: episodeID, BranchID: current.BranchID, Type: "episode.started", Payload: map[string]any{"objective_id": current.ObjectiveID}, Provenance: map[string]any{"source": "integration-test"}}
	objectiveEvent.ApplyDefaults(now)
	goal := objective.Objective{ObjectiveID: current.ObjectiveID, EpisodeID: episodeID, Text: "prove claim lifecycle"}
	goal.ApplyDefaults()
	if err = store.CreateObjective(ctx, goal, objectiveEvent); err != nil {
		t.Fatal(err)
	}
	frameEvent := protocol.Event{EventID: newTestUUID(), TransactionTime: now, ValidTime: now, AgentID: agentID, EpisodeID: episodeID, BranchID: current.BranchID, Type: "frame.created", Payload: map[string]any{"frame_id": current.FrameID}, Provenance: map[string]any{"source": "integration-test"}}
	frameEvent.ApplyDefaults(now)
	if err = store.CreateFrame(ctx, current, frameEvent); err != nil {
		t.Fatal(err)
	}
	seed := protocol.Event{EventID: newTestUUID(), TransactionTime: now, ValidTime: now, AgentID: agentID, EpisodeID: episodeID, BranchID: current.BranchID, Type: "world.observation", Payload: map[string]any{"observation_type": "file_content"}, Provenance: map[string]any{"source": "integration-test"}}
	seed.ApplyDefaults(now)
	if err = store.Append(ctx, seed); err != nil {
		t.Fatal(err)
	}

	first, err := step.Run(ctx, store, step.Input{Current: current, Emission: cognition.CognitiveEmission{
		EmissionID: newTestUUID(), FrameID: current.FrameID,
		Observation: []cognition.Observation{{Ref: "event:" + seed.EventID, Interpretation: "observed"}},
		Claims: []cognition.EmittedClaim{
			{Proposition: "fact " + episodeID + " backed by observation", Confidence: 0.95, Status: cognition.ClaimSupported, Evidence: []string{"event:" + seed.EventID}},
			{Proposition: "hypothesis " + episodeID + " awaiting verification", Confidence: 0.6, Status: cognition.ClaimCandidate, Evidence: []string{"event:" + seed.EventID}},
		},
	}, NewID: newTestUUID, Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatalf("lifecycle step: %v", err)
	}
	if len(first.Claims) != 2 || first.Claims[0].Status != cognition.ClaimSupported || first.Claims[1].Status != cognition.ClaimCandidate {
		t.Fatalf("birth statuses wrong: %#v", first.Claims)
	}
	for _, claim := range first.Claims {
		evidence, listErr := store.ListClaimEvidence(ctx, claim.ClaimID)
		if listErr != nil || len(evidence) != 1 || evidence[0] != seed.EventID {
			t.Fatalf("claim %s evidence = %v err=%v", claim.ClaimID, evidence, listErr)
		}
	}

	second, err := step.Run(ctx, store, step.Input{Current: first.Frame, Emission: cognition.CognitiveEmission{
		EmissionID: newTestUUID(), FrameID: first.Frame.FrameID,
		ClaimOps: []cognition.ClaimOperation{
			{Op: "confirm", Claim: "claim:" + first.Claims[1].ClaimID},
			{Op: "confirm", Claim: "claim:" + first.Claims[0].ClaimID},
		},
	}, NewID: newTestUUID, Now: func() time.Time { return now.Add(2 * time.Second) }})
	if err != nil {
		t.Fatalf("confirm step: %v", err)
	}
	confirmed, err := store.GetClaim(ctx, first.Claims[1].ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != cognition.ClaimSupported || confirmed.ValidTo != nil {
		t.Fatalf("confirm must keep the claim open: %#v", confirmed)
	}
	var transition *protocol.Event
	for i := range second.Events {
		if second.Events[i].Type == step.EventFrameTransitioned {
			transition = &second.Events[i]
		}
	}
	encoded, marshalErr := json.Marshal(transition.Payload)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if !strings.Contains(string(encoded), "already supported") {
		t.Fatalf("already-supported rejection missing: %s", encoded)
	}

	_, err = step.Run(ctx, store, step.Input{Current: second.Frame, Emission: cognition.CognitiveEmission{
		EmissionID: newTestUUID(), FrameID: second.Frame.FrameID,
		Claims: []cognition.EmittedClaim{{Proposition: "hallucinated " + episodeID, Confidence: 0.9, Status: cognition.ClaimSupported, Evidence: []string{"event:" + newTestUUID()}}},
	}, NewID: newTestUUID, Now: func() time.Time { return now.Add(3 * time.Second) }})
	if err == nil {
		t.Fatal("step with unknown evidence succeeded")
	}
}
