package httpapi

import (
	"testing"

	"github.com/temporality-project/temporality/observation"
)

func TestAggregateTierOutcomes(t *testing.T) {
	mk := func(run, typ string, data map[string]any) observation.Event {
		return observation.Event{Type: typ, Context: observation.Context{Run: run}, Data: data}
	}
	used := []observation.Event{
		mk("r1", "hint.used", map[string]any{"knowledge_id": "k1", "matched_by": "semantic:0.91"}),
		mk("r2", "hint.used", map[string]any{"knowledge_id": "k2", "matched_by": "entity:playwright"}),
		mk("r3", "hint.used", map[string]any{"knowledge_id": "k3", "matched_by": "term:csv"}),
		mk("r4", "hint.used", map[string]any{"knowledge_id": "k4"}),
	}
	outcomes := []observation.Event{
		mk("r1", "hint.outcome", map[string]any{"knowledge_id": "k1", "outcome": "helpful"}),
		mk("r1", "hint.outcome", map[string]any{"knowledge_id": "k1", "outcome": "helpful"}),
		mk("r2", "hint.outcome", map[string]any{"knowledge_id": "k2", "outcome": "harmful"}),
		mk("r3", "hint.outcome", map[string]any{"knowledge_id": "k3", "outcome": "helpful"}),
		mk("r5", "hint.outcome", map[string]any{"knowledge_id": "k9", "outcome": "harmful"}), // no used event → unknown tier
	}
	tiers := aggregateTierOutcomes(used, outcomes)
	byTier := map[string]TierOutcome{}
	for _, row := range tiers {
		byTier[row.Tier] = row
	}
	if row := byTier["semantic"]; row.Helpful != 2 || row.Harmful != 0 || row.HelpfulRatePct != 100 {
		t.Fatalf("semantic row = %+v", row)
	}
	if row := byTier["entity"]; row.Helpful != 0 || row.Harmful != 1 {
		t.Fatalf("entity row = %+v", row)
	}
	if row := byTier["term"]; row.Helpful != 1 {
		t.Fatalf("term row = %+v", row)
	}
	if row := byTier["unknown"]; row.Harmful != 1 {
		t.Fatalf("unknown row = %+v", row)
	}
}

func TestTierOf(t *testing.T) {
	cases := map[string]string{
		"semantic:0.91":     "semantic",
		"entity:playwright": "entity",
		"topic:ci":          "topic",
		"term:csv":          "term",
		"":                  "unknown",
		"mystery:x":         "other",
	}
	for in, want := range cases {
		if got := tierOf(in); got != want {
			t.Errorf("tierOf(%q) = %q, want %q", in, got, want)
		}
	}
}
