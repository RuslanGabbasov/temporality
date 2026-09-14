package timetravel_test

import (
	"reflect"
	"testing"

	"github.com/temporality-project/temporality/frp/timetravel"
)

func TestBuildBlameGraphIsDeterministicDepthBoundedAndCycleSafe(t *testing.T) {
	nodes := []timetravel.BlameNode{
		{ID: "root", Type: timetravel.BlameNodeDecision},
		{ID: "a", Type: timetravel.BlameNodeEvent},
		{ID: "b", Type: timetravel.BlameNodeState},
		{ID: "deep", Type: timetravel.BlameNodeSnapshot},
	}
	edges := []timetravel.BlameEdge{
		{From: "b", To: "root", Type: timetravel.BlameEdgeDerivedFrom},
		{From: "a", To: "root", Type: timetravel.BlameEdgeCausedBy},
		{From: "root", To: "a", Type: timetravel.BlameEdgeReadFrom},
		{From: "deep", To: "b", Type: timetravel.BlameEdgeProduced},
	}
	first, err := timetravel.BuildBlameGraph("root", nodes, edges, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := timetravel.BuildBlameGraph("root", nodes, []timetravel.BlameEdge{edges[3], edges[2], edges[1], edges[0]}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("input order changed graph:\n%#v\n%#v", first, second)
	}
	wantIDs := []string{"root", "a", "b"}
	gotIDs := make([]string, len(first.Nodes))
	for i := range first.Nodes {
		gotIDs[i] = first.Nodes[i].ID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("nodes = %v, want %v", gotIDs, wantIDs)
	}
}

func TestPureReplayContractRejectsEffects(t *testing.T) {
	contract := timetravel.PureReplayContract()
	if err := contract.Validate(); err != nil {
		t.Fatal(err)
	}
	contract.SideEffectsAllowed = true
	if err := contract.Validate(); err == nil {
		t.Fatal("expected side effects to violate replay purity")
	}
}
