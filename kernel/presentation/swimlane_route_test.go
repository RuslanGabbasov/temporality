package presentation

import (
	"testing"
)

// TestSwimlaneEdgesDoNotCrossNodes locks the router invariant the frontend
// router also tests: no edge segment may pass through a node's interior.
// The old midpoint elbow violated it whenever the target sat in the same
// column or left of the source — lines cut through boxes (phantom inner
// rectangles) and arrowheads pointed the wrong way.
func TestSwimlaneEdgesDoNotCrossNodes(t *testing.T) {
	data := SwimlaneData{
		Lanes: []SwimlaneLane{
			{ID: "requester", Label: "Заявитель"},
			{ID: "agent", Label: "Агент"},
			{ID: "registry", Label: "Реестр"},
			{ID: "approver", Label: "Согласующие"},
		},
		Nodes: []SwimlaneNode{
			// Same column, different lanes — the vertical case.
			{ID: "n0", Lane: "requester", Label: "Заявка", Start: 0},
			{ID: "n1", Lane: "agent", Label: "Job Statement", Start: 0},
			{ID: "n2", Lane: "registry", Label: "artifact_created", Start: 0},
			// Forward, same lane with a blocker between.
			{ID: "n3", Lane: "agent", Label: "Черновик", Start: 1},
			{ID: "n4", Lane: "agent", Label: "DoR-гейт", Start: 2},
			// Backward edge target (left of the source).
			{ID: "n5", Lane: "registry", Label: "artifact_submitted", Start: 1},
			{ID: "n6", Lane: "approver", Label: "Аппрув", Start: 0},
			// Diagonal across three lanes.
			{ID: "n7", Lane: "approver", Label: "Эскалация", Start: 3},
		},
		Edges: []SwimlaneEdge{
			{From: "n0", To: "n1"}, // same column → vertical
			{From: "n1", To: "n3"}, // same lane, adjacent
			{From: "n3", To: "n4"}, // same lane, adjacent
			{From: "n4", To: "n5"}, // backward diagonal (target left AND below)
			{From: "n5", To: "n2"}, // backward, target left and above
			{From: "n1", To: "n6"}, // cross-lane diagonal
			{From: "n4", To: "n7"}, // long diagonal across lanes
			{From: "n2", To: "n7"}, // far diagonal
		},
	}
	frag := layoutSwimlane(data, SwimlaneProps{}, 480, LightTheme())

	// Collect node interiors (only stroked rects are nodes; lane bands are
	// fill-only) and edge segments (only connected multi-point paths are
	// edges — lane bands and node boxes are not Segments).
	var nodes []Rect
	var segs []Segment
	for _, op := range frag.Ops {
		switch v := op.(type) {
		case Rect:
			if v.Stroke != "" {
				nodes = append(nodes, v)
			}
		case Segment:
			segs = append(segs, v)
		}
	}
	if len(nodes) != 8 {
		t.Fatalf("expected 8 node rects, got %d", len(nodes))
	}
	if len(segs) == 0 {
		t.Fatal("no edge segments rendered")
	}
	// Every edge contributes at least one segment; with 8 edges we expect a
	// healthy number of segments (each route is 2-6 points).
	if len(segs) < 8 {
		t.Fatalf("expected >=8 edge segments, got %d", len(segs))
	}
	const eps = 1.0 // segments may touch edges but not cross interiors
	for si, s := range segs {
		minX, maxX := min(s.X1, s.X2), max(s.X1, s.X2)
		minY, maxY := min(s.Y1, s.Y2), max(s.Y1, s.Y2)
		for ni, r := range nodes {
			if maxX > r.X+eps && minX < r.X+r.W-eps &&
				maxY > r.Y+eps && minY < r.Y+r.H-eps {
				t.Errorf("segment %d (%.1f,%.1f)-(%.1f,%.1f) crosses node %d (%.1f,%.1f %.1fx%.1f)",
					si, s.X1, s.Y1, s.X2, s.Y2, ni, r.X, r.Y, r.W, r.H)
			}
		}
	}
}

// label is unused; kept out of the file on purpose (see git history).
