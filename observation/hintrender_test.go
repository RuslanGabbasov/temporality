package observation

import (
	"strings"
	"testing"
)

func renderFixtureHints() []Hint {
	return []Hint{
		{KnowledgeID: "k-claim1", Proposition: "The gatekeeper CLI requires the --out flag to write the report", State: "confirmed", MatchedBy: []string{"entity:gatekeeper", "term:report"}},
		{KnowledgeID: "auto/obs1", Proposition: "Report generation command exited 0 with the required flag", State: "proposed", MatchedBy: []string{"term:report", "term:flag"}, Caution: "unconfirmed hypothesis"},
		{KnowledgeID: "k-claim2", Proposition: "Integration tests need Docker running before the suite starts", State: "confirmed", MatchedBy: []string{"topic:testing"}},
	}
}

func TestRenderHintBlockKeepsRankOrderAndContract(t *testing.T) {
	result := RenderHintBlock(renderFixtureHints(), DefaultRenderOptions())
	if result.Kept != 3 || result.Dropped != 0 {
		t.Fatalf("kept=%d dropped=%d, want 3/0", result.Kept, result.Dropped)
	}
	first := strings.Index(result.Block, "k-claim1")
	second := strings.Index(result.Block, "auto/obs1")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("cues must preserve rank order:\n%s", result.Block)
	}
	for _, want := range []string{"[confirmed]", "knowledge_id=k-claim1", "match: entity:gatekeeper", "caution: unconfirmed hypothesis"} {
		if !strings.Contains(result.Block, want) {
			t.Fatalf("block missing %q:\n%s", want, result.Block)
		}
	}
	if !strings.HasPrefix(result.Block, "Temporality prior knowledge") {
		t.Fatalf("block must start with the contract header:\n%s", result.Block)
	}
}

func TestRenderHintBlockHoldsTotalBudget(t *testing.T) {
	hints := make([]Hint, 12)
	for i := range hints {
		hints[i] = Hint{KnowledgeID: "k-long-" + strings.Repeat("x", 20) + "-0000", Proposition: strings.Repeat("verified durable rule about migrations and schemas ", 30), State: "confirmed", MatchedBy: []string{"term:migrations", "term:schemas"}}
	}
	opts := RenderOptions{MaxTokens: 150, MaxCueTokens: 40, MaxHints: 8}
	result := RenderHintBlock(hints, opts)
	if result.Block == "" {
		t.Fatal("expected a non-empty block")
	}
	if result.Tokens > opts.MaxTokens {
		t.Fatalf("block tokens %d exceed budget %d", result.Tokens, opts.MaxTokens)
	}
	if result.Kept+result.Dropped != len(hints) {
		t.Fatalf("kept %d + dropped %d != offered %d", result.Kept, result.Dropped, len(hints))
	}
	if result.Kept == opts.MaxHints && result.Dropped != len(hints)-opts.MaxHints {
		t.Fatalf("hints beyond MaxHints must count as dropped: %+v", result)
	}
}

func TestRenderHintBlockTruncatesSingleCue(t *testing.T) {
	long := Hint{KnowledgeID: "k-huge", Proposition: strings.Repeat("detail ", 2000), State: "confirmed"}
	result := RenderHintBlock([]Hint{long}, RenderOptions{MaxTokens: 200, MaxCueTokens: 30, MaxHints: 4})
	if result.Kept != 1 {
		t.Fatalf("the single cue must be kept truncated, kept=%d", result.Kept)
	}
	line := strings.SplitN(strings.SplitN(result.Block, "\n", 2)[1], "\n", 2)[0]
	if cueTokens(line) > 30 {
		t.Fatalf("cue tokens %d exceed per-cue cap 30: %s", cueTokens(line), line)
	}
	if !strings.Contains(line, "knowledge_id=k-huge") || !strings.Contains(line, "[confirmed]") {
		t.Fatalf("truncated cue must keep the contract suffix: %s", line)
	}
}

func TestRenderHintBlockEmptyWhenNothingFits(t *testing.T) {
	if got := RenderHintBlock(nil, DefaultRenderOptions()); got.Block != "" || got.Tokens != 0 || got.Kept != 0 {
		t.Fatalf("no hints must yield an empty result, got %+v", got)
	}
	hint := Hint{KnowledgeID: "k-x", Proposition: "rule", State: "confirmed"}
	// A budget too small for the header plus one viable cue yields an honest
	// zero instead of a header-only block the model could mistake for memory.
	got := RenderHintBlock([]Hint{hint}, RenderOptions{MaxTokens: 20, MaxCueTokens: 120, MaxHints: 8})
	if got.Block != "" || got.Kept != 0 || got.Dropped != 1 {
		t.Fatalf("unfittable hint must be dropped entirely, got %+v", got)
	}
}

func TestRenderHintBlockDeterministic(t *testing.T) {
	hints := renderFixtureHints()
	first := RenderHintBlock(hints, DefaultRenderOptions())
	second := RenderHintBlock(hints, DefaultRenderOptions())
	if first != second {
		t.Fatal("identical input and options must render identical blocks")
	}
}
