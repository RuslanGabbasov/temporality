package memory

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/branch"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/timetravel"
)

func TestDurableBranchIntegration(t *testing.T) {
	ctx := context.Background()
	store := New()
	source := integrationSourceFrame()
	created := integrationEvent("source-event", "frame.created", source.EpisodeID, source.BranchID, map[string]any{"frame_id": source.FrameID})
	if err := store.CreateFrame(ctx, source, created); err != nil {
		t.Fatal(err)
	}
	before, _ := store.GetFrame(ctx, source.FrameID)
	if _, err := store.Fork(ctx, source.FrameID, branch.ForkRequest{ForkGroupID: "group-bad", BranchIDs: []string{"duplicate", "duplicate"}}); err == nil {
		t.Fatal("invalid atomic fork succeeded")
	}
	if len(store.branches) != 0 || len(store.forkGroups) != 0 {
		t.Fatal("failed fork left durable state")
	}

	group, err := store.Fork(ctx, source.FrameID, branch.ForkRequest{ForkGroupID: "group", Branches: []branch.BranchSpec{{BranchID: "left", ModelConfig: map[string]any{"model": "a"}}, {BranchID: "right", ModelConfig: map[string]any{"model": "b"}}}})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := store.GetFrame(ctx, source.FrameID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("fork mutated source frame")
	}
	leftRoot, _ := store.GetFrame(ctx, group.Branches[0].RootFrameID)
	rightRoot, _ := store.GetFrame(ctx, group.Branches[1].RootFrameID)
	leftState, rightState := leftRoot, rightRoot
	leftState.FrameID, leftState.BranchID = "", ""
	rightState.FrameID, rightState.BranchID = "", ""
	if !reflect.DeepEqual(leftState, rightState) || leftRoot.BranchID == rightRoot.BranchID {
		t.Fatal("fork roots do not preserve equal state with isolated branch IDs")
	}
	if _, err = store.UpdateHead(ctx, "left", leftRoot.FrameID, rightRoot.FrameID); err == nil {
		t.Fatal("cross-branch head update succeeded")
	}

	candidateA, candidateB := leftRoot, leftRoot
	candidateA.FrameID, candidateA.ParentFrameID, candidateA.Revision = "left-next-a", leftRoot.FrameID, leftRoot.Revision+1
	candidateB.FrameID, candidateB.ParentFrameID, candidateB.Revision = "left-next-b", leftRoot.FrameID, leftRoot.Revision+1
	store.frames[candidateA.FrameID], store.frames[candidateB.FrameID] = candidateA, candidateB
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{candidateA.FrameID, candidateB.FrameID} {
		wg.Add(1)
		go func(next string) {
			defer wg.Done()
			_, updateErr := store.UpdateHead(ctx, "left", leftRoot.FrameID, next)
			results <- updateErr
		}(id)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for updateErr := range results {
		if updateErr == nil {
			successes++
		} else if errors.Is(updateErr, branch.ErrCASConflict) {
			conflicts++
		} else {
			t.Fatal(updateErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("CAS results: successes=%d conflicts=%d", successes, conflicts)
	}

	left := branch.Trajectory{BranchID: "left", EpisodeID: source.EpisodeID, ObjectiveID: source.ObjectiveID, SourceFrameID: source.FrameID, Claims: []string{"shared", "left"}, Cost: 1}
	right := branch.Trajectory{BranchID: "right", EpisodeID: source.EpisodeID, ObjectiveID: source.ObjectiveID, SourceFrameID: source.FrameID, Claims: []string{"shared", "right"}, Cost: 2}
	first, err := store.PutComparison(ctx, group, left, right)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PutComparison(ctx, group, left, right)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.GetComparison(ctx, first.ComparisonID)
	if err != nil || !reflect.DeepEqual(first, second) || !reflect.DeepEqual(first, persisted) {
		t.Fatalf("comparison is not reproducible: %#v %#v %#v %v", first, second, persisted, err)
	}

	late := integrationEvent("late-source-event", "metric.recorded", source.EpisodeID, source.BranchID, map[string]any{"late": true})
	if err = store.Append(ctx, late); err != nil {
		t.Fatal(err)
	}
	events, err := store.ListEventsThrough(ctx, source.EpisodeID, "right", timetravel.EventCursor{EventSeq: store.nextEventSeq})
	if err != nil {
		t.Fatal(err)
	}
	seenSource, seenLate, seenSibling := false, false, false
	for _, item := range events {
		seenSource = seenSource || item.Event.EventID == created.EventID
		seenLate = seenLate || item.Event.EventID == late.EventID
		seenSibling = seenSibling || item.Event.BranchID == "left"
	}
	if !seenSource || seenLate || seenSibling {
		t.Fatalf("incorrect shared-prefix visibility: source=%v late=%v sibling=%v", seenSource, seenLate, seenSibling)
	}
}

func integrationSourceFrame() frame.Frame {
	value := frame.Frame{FrameID: "source", AgentID: "agent", EpisodeID: "episode", BranchID: "main", ObjectiveID: "objective", AsOf: time.Now().UTC(), Focus: frame.Focus{Type: frame.RefQuery, Query: "test"}}
	value.ApplyDefaults()
	return value
}

func integrationEvent(id, kind, episodeID, branchID string, payload map[string]any) protocol.Event {
	now := time.Now().UTC()
	return protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: now, ValidTime: now, EpisodeID: episodeID, BranchID: branchID, Type: kind, Payload: payload, Provenance: map[string]any{"source": "integration-test"}}
}
