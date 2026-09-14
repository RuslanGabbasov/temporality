package attention_test

import (
	"testing"

	"github.com/temporality-project/temporality/frp/attention"
	"github.com/temporality-project/temporality/frp/frame"
)

func TestAmbientScoringIsDeterministicAndSticky(t *testing.T) {
	engine, err := attention.New(attention.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	candidates := []attention.Candidate{{Ref: frame.Ref{Type: frame.RefEvent, ID: "b"}, Features: attention.Features{SemanticRelevance: .5, Trust: .5}}, {Ref: frame.Ref{Type: frame.RefEvent, ID: "a"}, Features: attention.Features{SemanticRelevance: .5, Trust: .5}}}
	first, err := engine.SelectAmbient(candidates, map[string]struct{}{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Selected[0].Candidate.Ref.ID != "a" {
		t.Fatal("tie did not use stable ID order")
	}
	sticky, err := engine.SelectAmbient(candidates, map[string]struct{}{"event:b": {}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if sticky.Selected[0].Candidate.Ref.ID != "b" || !sticky.Selected[0].HysteresisApplied {
		t.Fatalf("hysteresis not applied: %#v", sticky)
	}
}
func TestDeliberateAttentionIgnoresAmbientHistory(t *testing.T) {
	engine, _ := attention.New(attention.DefaultPolicy())
	target := frame.Focus{Type: frame.RefQuery, Query: "semantic jump"}
	selected, err := engine.SelectDeliberate(target)
	if err != nil {
		t.Fatal(err)
	}
	if selected != target {
		t.Fatal("deliberate target changed")
	}
}
func TestInvalidFeaturesRejected(t *testing.T) {
	engine, _ := attention.New(attention.DefaultPolicy())
	_, err := engine.SelectAmbient([]attention.Candidate{{Ref: frame.Ref{Type: frame.RefEvent, ID: "e"}, Features: attention.Features{Trust: 1.1}}}, nil, 1)
	if err == nil {
		t.Fatal("invalid feature accepted")
	}
}
