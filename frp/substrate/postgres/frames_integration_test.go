package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

func TestFrameTransitionSurvivesRestartAndIsReplayable(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000002_claims.up.sql", "../../../migrations/000003_claim_guards.up.sql", "../../../migrations/000004_frames.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	episodeID, branchID := newTestUUID(), newTestUUID()
	initial := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: newTestUUID(), AgentID: newTestUUID(), EpisodeID: episodeID, BranchID: branchID, ObjectiveID: newTestUUID(), AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "integration test"}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Zoom: 2, Filters: frame.Filters{TrustMin: 0.5, AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 8000}}
	createdEvent := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: now, ValidTime: now, EpisodeID: episodeID, BranchID: branchID, Type: "frame.created", Payload: map[string]any{"frame_id": initial.FrameID}, Provenance: map[string]any{"source": "integration-test"}}
	if err = store.CreateFrame(ctx, initial, createdEvent); err != nil {
		store.Close()
		t.Fatal(err)
	}
	mode := frame.ModeVerify
	transition := frame.Transition{AsOf: now.Add(time.Second).Format(time.RFC3339Nano), Operations: []frame.Operation{{Kind: frame.OpSetMode, Mode: mode}, {Kind: frame.OpPin, Ref: &frame.Ref{Type: frame.RefEvent, ID: createdEvent.EventID}}}}
	transitionEvent := protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: newTestUUID(), TransactionTime: now.Add(time.Second), ValidTime: now.Add(time.Second), Type: "frame.transitioned", Payload: map[string]any{"parent_frame_id": initial.FrameID}, Provenance: map[string]any{"source": "integration-test"}}
	result, err := store.TransitionFrame(ctx, initial.FrameID, transition, transitionEvent)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()

	reopened, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.GetFrame(ctx, result.Frame.FrameID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ParentFrameID != initial.FrameID || restored.Mode != frame.ModeVerify || restored.Revision != 1 {
		t.Fatalf("unexpected restored frame: %#v", restored)
	}
	events, err := reopened.List(ctx, substrate.EventFilter{EpisodeID: episodeID, BranchID: branchID})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != "frame.created" || events[1].Type != "frame.transitioned" {
		t.Fatalf("unexpected frame replay events: %#v", events)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, "UPDATE frames SET revision=99 WHERE frame_id=$1", initial.FrameID); err == nil {
		t.Fatal("immutable frame accepted direct update")
	}
}
