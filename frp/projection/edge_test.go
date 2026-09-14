package projection_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/projection"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func TestEdgeProjectionIsOrderIndependentAndTyped(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	events := []protocol.Event{
		{EventID: "event-a", ValidTime: now},
		{EventID: "event-b", ValidTime: now.Add(time.Second)},
		{EventID: "event-c", ValidTime: now.Add(2 * time.Second)},
	}
	regions := []projection.Region{
		{RegionID: "region-a", MemberEventIDs: []string{"event-a", "event-b"}},
		{RegionID: "region-b", MemberEventIDs: []string{"event-b", "event-c"}},
	}
	first := projection.BuildEdges("episode", "branch", events, regions)
	second := projection.BuildEdges("episode", "branch", []protocol.Event{events[2], events[0], events[1]}, []projection.Region{regions[1], regions[0]})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("edge projection depends on input order:\n%#v\n%#v", first, second)
	}
	if len(first) != 2 || first[0].Type != projection.EdgeSequenceAdjacent || first[0].EvidenceCount != 2 || first[1].Type != projection.EdgeSharedEvent || first[1].EvidenceCount != 1 {
		t.Fatalf("unexpected typed edges: %#v", first)
	}
	for _, edge := range first {
		if edge.SourceRegionID != "region-a" || edge.TargetRegionID != "region-b" || edge.Weight != 1 || edge.ProjectionVersion != projection.EdgeProjectorVersion {
			t.Fatalf("unexpected edge: %#v", edge)
		}
	}
}

func TestEdgeProjectionCanBeRebuiltByScope(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	old := projection.Edge{EdgeID: "old", EpisodeID: "episode", BranchID: "branch", SourceRegionID: "a", TargetRegionID: "b", Type: projection.EdgeSharedEvent, Weight: 1, EvidenceCount: 1, ProjectionVersion: projection.EdgeProjectorVersion}
	other := projection.Edge{EdgeID: "other", EpisodeID: "episode", BranchID: "other", SourceRegionID: "a", TargetRegionID: "c", Type: projection.EdgeSharedEvent, Weight: 1, EvidenceCount: 1, ProjectionVersion: projection.EdgeProjectorVersion}
	if err := store.ReplaceEdges(ctx, "episode", "branch", []projection.Edge{old}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceEdges(ctx, "episode", "other", []projection.Edge{other}); err != nil {
		t.Fatal(err)
	}
	if _, err := projection.RebuildEdges(ctx, store, "episode", "branch"); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListEdges(ctx, projection.EdgeFilter{EpisodeID: "episode"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []projection.Edge{other}) {
		t.Fatalf("rebuild did not replace only its episode/branch scope: %#v", got)
	}
}
