package priming

import (
	"fmt"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/observation"
)

func TestGroupPatterns(t *testing.T) {
	knowledge := []observation.Knowledge{
		{ID: "K1", Proposition: "OAuth works with PAT", State: "confirmed", Topics: []string{"auth"}, ReuseCount: 5},
		{ID: "K2", Proposition: "AUTH_TOKEN rejected in v2", State: "invalidated", Topics: []string{"auth"}},
		{ID: "K3", Proposition: "Test runner needs -v flag", State: "confirmed", Topics: []string{"test"}},
	}

	patterns := groupPatterns(knowledge)
	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(patterns))
	}

	// Find auth pattern
	var auth, test ExperiencePattern
	for _, p := range patterns {
		if p.Scope == "auth" {
			auth = p
		}
		if p.Scope == "test" {
			test = p
		}
	}

	if auth.ActiveCount != 1 || auth.InvalidCount != 1 {
		t.Fatalf("auth: active=%d invalid=%d, want 1/1", auth.ActiveCount, auth.InvalidCount)
	}
	if test.ActiveCount != 1 || test.InvalidCount != 0 {
		t.Fatalf("test: active=%d invalid=%d, want 1/0", test.ActiveCount, test.InvalidCount)
	}
	if len(auth.Signals) < 2 {
		t.Fatalf("auth should have ≥2 signals, got %d", len(auth.Signals))
	}
}

func TestScorePattern(t *testing.T) {
	patterns := groupPatterns([]observation.Knowledge{
		{ID: "K1", Proposition: "jwt refresh token rotation fails nightly", State: "confirmed", Topics: []string{"auth"}, ReuseCount: 2},
	})

	// Task whose tokens are contained in the proposition → high relevance.
	scoreMatched := scorePattern(patterns[0], "jwt refresh token rotation")
	if scoreMatched <= 0 {
		t.Fatalf("score for matching task = %f, want > 0", scoreMatched)
	}

	// Task sharing nothing with the items or the scope → zero relevance.
	scoreOther := scorePattern(patterns[0], "fix build system")
	if scoreOther >= scoreMatched {
		t.Fatalf("unrelated task score %f should be < matched task %f", scoreOther, scoreMatched)
	}
	if scoreOther != 0 {
		t.Fatalf("unrelated task score = %f, want 0", scoreOther)
	}
}

func TestSignalsFromKnowledge(t *testing.T) {
	k := observation.Knowledge{ID: "K1", Proposition: "OAuth works with PAT", State: "confirmed", ReuseCount: 3}
	signals := signalsFromKnowledge(k)
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	if signals[0].Kind != "success" {
		t.Fatalf("signal kind = %q, want success", signals[0].Kind)
	}

	k2 := observation.Knowledge{ID: "K2", Proposition: "Old method", State: "invalidated", AtRisk: true}
	signals2 := signalsFromKnowledge(k2)
	if len(signals2) != 2 {
		t.Fatalf("expected 2 signals (failure + stale), got %d", len(signals2))
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxPatterns != 7 {
		t.Fatalf("MaxPatterns = %d, want 7", cfg.MaxPatterns)
	}
	if cfg.MinScore != 0.1 {
		t.Fatalf("MinScore = %f, want 0.1", cfg.MinScore)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Fatalf("truncate short = %q", got)
	}
	got := truncate("hello world long text", 10)
	if len([]rune(got)) != 10 {
		t.Fatalf("truncate long rune count = %d", len([]rune(got)))
	}
}

func TestTokenize(t *testing.T) {
	tokens := tokenize("Fix the authentication bug in OAuth")
	if !tokens["authentication"] {
		t.Fatal("expected 'authentication' token")
	}
	if !tokens["oauth"] {
		t.Fatal("expected 'oauth' token")
	}
	if tokens["the"] {
		t.Fatal("'the' should be filtered (too short)")
	}
}

func TestValidityWeight(t *testing.T) {
	cases := map[string]float64{
		"confirmed":    1.0,
		"proposed":     0.7,
		"challenged":   0.5,
		"corrected":    0.5,
		"invalidated":  0.2,
		"superseded":   0.2,
		"hallucinated": 0.5,
	}
	for state, want := range cases {
		if got := validityWeight(state); got != want {
			t.Errorf("validityWeight(%q) = %f, want %f", state, got, want)
		}
	}
}

// TestRelevancePropositionBeatsScopeOnly checks that relevance is driven by
// item content: a pattern whose proposition matches the task outranks an
// equally sized pattern where only the scope matches.
func TestRelevancePropositionBeatsScopeOnly(t *testing.T) {
	task := "OAuth-token rotation fails"
	patterns := groupPatterns([]observation.Knowledge{
		// Content match: the task tokens are contained in the proposition,
		// while the scope ("auth") says nothing about the task.
		{ID: "K1", Proposition: "OAuth-token rotation fails nightly", State: "confirmed", Topics: []string{"auth"}},
		// Scope-only match: the scope derived from the leading proposition
		// words ("oauth-token") matches a task token, but no token of the
		// item content does.
		{ID: "K2", Proposition: "OAuth token flow differs", State: "confirmed"},
	})

	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(patterns))
	}
	var byContent, byScope ExperiencePattern
	for _, pat := range patterns {
		switch pat.Scope {
		case "auth":
			byContent = pat
		case "oauth-token":
			byScope = pat
		}
	}
	if byContent.ID == "" || byScope.ID == "" {
		t.Fatalf("expected scopes auth and oauth-token, got %+v", patterns)
	}

	scoreContent := scorePattern(byContent, task)
	scoreScope := scorePattern(byScope, task)
	if scoreContent <= scoreScope {
		t.Fatalf("proposition match score %f must be > scope-only score %f", scoreContent, scoreScope)
	}
}

// TestDeterministicOrder runs the full grouping+scoring pipeline twice over
// equal-score patterns and requires identical pattern ID sequences.
func TestDeterministicOrder(t *testing.T) {
	scopes := []string{"scope-alpha", "scope-beta", "scope-gamma", "scope-delta", "scope-epsilon"}
	var knowledge []observation.Knowledge
	for _, scope := range scopes {
		for i := 0; i < 6; i++ {
			knowledge = append(knowledge, observation.Knowledge{
				ID:          scope + "-item",
				Proposition: "identical proposition for equal scoring",
				State:       "proposed",
				Topics:      []string{scope},
			})
		}
	}
	if len(knowledge) < 30 {
		t.Fatalf("test setup requires >=30 items, got %d", len(knowledge))
	}

	config := Config{MaxPatterns: 3, MinScore: 0.1}

	ids := func(k []observation.Knowledge) []string {
		var out []string
		for _, pat := range rankPatterns(k, "matching identical proposition", config) {
			out = append(out, pat.ID)
		}
		return out
	}

	first := ids(knowledge)
	second := ids(knowledge)
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("pipeline runs produced different lengths: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("pattern order diverged at %d: %q vs %q", i, first[i], second[i])
		}
	}

	// Equal scores must fall back to ID ascending.
	for i := 1; i < len(first); i++ {
		if first[i-1] >= first[i] {
			t.Fatalf("expected ascending IDs for equal scores, got %v", first)
		}
	}

	// Reversing the input must not change the outcome either.
	reversed := make([]observation.Knowledge, 0, len(knowledge))
	for i := len(knowledge) - 1; i >= 0; i-- {
		reversed = append(reversed, knowledge[i])
	}
	reordered := ids(reversed)
	if len(reordered) != len(first) {
		t.Fatalf("reversed input changed pattern count: %v vs %v", reordered, first)
	}
	for i := range reordered {
		if reordered[i] != first[i] {
			t.Fatalf("reversed input changed order at %d: %q vs %q", i, reordered[i], first[i])
		}
	}
}

// TestInvalidatedBelowProposed checks that with equal relevance a pattern of
// proposed items outranks one built only from invalidated items.
func TestInvalidatedBelowProposed(t *testing.T) {
	task := "alpha beta gamma delta"
	patterns := groupPatterns([]observation.Knowledge{
		{ID: "K1", Proposition: "alpha beta gamma delta", State: "proposed", Topics: []string{"scope-proposed"}},
		{ID: "K2", Proposition: "alpha beta gamma delta", State: "invalidated", Topics: []string{"scope-invalidated"}},
	})

	var proposed, invalidated ExperiencePattern
	for _, pat := range patterns {
		switch pat.Scope {
		case "scope-proposed":
			proposed = pat
		case "scope-invalidated":
			invalidated = pat
		}
	}

	scoreProposed := scorePattern(proposed, task)
	scoreInvalidated := scorePattern(invalidated, task)
	if scoreProposed <= scoreInvalidated {
		t.Fatalf("proposed score %f must be > invalidated score %f", scoreProposed, scoreInvalidated)
	}
}

// TestEmptyTaskFallback checks that without a task patterns are ranked by
// validity and reuse, without the MinScore filter.
func TestEmptyTaskFallback(t *testing.T) {
	knowledge := []observation.Knowledge{
		{ID: "K1", Proposition: "confirmed workhorse", State: "confirmed", ReuseCount: 10, Topics: []string{"scope-confirmed"}},
		{ID: "K2", Proposition: "fresh hypothesis", State: "proposed", Topics: []string{"scope-proposed"}},
		{ID: "K3", Proposition: "dead end", State: "invalidated", Topics: []string{"scope-invalidated"}},
	}

	config := Config{MaxPatterns: 7, MinScore: 0.5}
	patterns := rankPatterns(knowledge, "", config)

	if len(patterns) != 3 {
		t.Fatalf("fallback must not apply MinScore, expected 3 patterns, got %d", len(patterns))
	}
	if patterns[0].Scope != "scope-confirmed" {
		t.Fatalf("top fallback pattern = %q, want scope-confirmed", patterns[0].Scope)
	}
	if patterns[1].Scope != "scope-proposed" {
		t.Fatalf("second fallback pattern = %q, want scope-proposed", patterns[1].Scope)
	}
	if patterns[0].Score <= patterns[1].Score {
		t.Fatalf("confirmed+reuse score %f must be > proposed-only %f", patterns[0].Score, patterns[1].Score)
	}

	// Sanity: the same knowledge with a non-matching task is filtered out
	// entirely by MinScore, unlike the fallback above.
	filtered := rankPatterns(knowledge, "totally unrelated tokens here", config)
	if len(filtered) != 0 {
		t.Fatalf("normal mode should filter out zero-relevance patterns, got %d", len(filtered))
	}
}

// TestBudgetRespected feeds more than a hundred items across ten scopes
// and requires the rendered output to stay inside the token budget, with
// the reported estimate equal to the sum over titles, cues and IDs.
func TestBudgetRespected(t *testing.T) {
	task := "knowledge item detail"
	var knowledge []observation.Knowledge
	for i := 0; i < 10; i++ {
		scope := fmt.Sprintf("scope-%02d", i)
		for j := 0; j < 12; j++ {
			knowledge = append(knowledge, observation.Knowledge{
				ID:          fmt.Sprintf("K-%s-%02d", scope, j),
				Proposition: fmt.Sprintf("knowledge item %s %02d %s", scope, j, strings.Repeat("detail ", 20)),
				State:       "confirmed",
				Topics:      []string{scope},
				ReuseCount:  j,
			})
		}
	}
	if len(knowledge) < 100 {
		t.Fatalf("test setup requires >=100 items, got %d", len(knowledge))
	}

	config := DefaultConfig()
	kept, hidden, tokens := renderBudget(rankPatterns(knowledge, task, config), config)

	if tokens > config.MaxTokens {
		t.Fatalf("TokensEstimate = %d, must be <= MaxTokens %d", tokens, config.MaxTokens)
	}

	sum := 0
	for _, pat := range kept {
		sum += estimateTokens(pat.Title) + estimateTokens(pat.ID)
		for _, cue := range pat.Cues {
			sum += estimateTokens(cue)
		}
	}
	if sum != tokens {
		t.Fatalf("recomputed estimate %d != reported TokensEstimate %d", sum, tokens)
	}

	// The corpus is large enough that the budget must actually bite.
	if hidden < 1 {
		t.Fatalf("expected >=1 hidden pattern, got %d (kept %d, tokens %d)", hidden, len(kept), tokens)
	}
	if len(kept)+hidden != 7 { // 10 scopes truncated to MaxPatterns before budgeting
		t.Fatalf("kept %d + hidden %d must cover the 7 ranked patterns", len(kept), hidden)
	}
}

// TestTop1AlwaysRendered checks that with a minimal budget exactly one
// pattern survives — the top-ranked one, with at least one cue — and
// everything else is reported as hidden.
func TestTop1AlwaysRendered(t *testing.T) {
	task := "shared matching proposition"
	var knowledge []observation.Knowledge
	for _, scope := range []string{"scope-a", "scope-b", "scope-c"} {
		for j := 0; j < 4; j++ {
			knowledge = append(knowledge, observation.Knowledge{
				ID:          fmt.Sprintf("K-%s-%d", scope, j),
				Proposition: "shared matching proposition",
				State:       "confirmed",
				Topics:      []string{scope},
			})
		}
	}

	config := Config{MaxPatterns: 7, MinScore: 0.1, MaxTokens: 10, CuesPerPattern: 3}
	ranked := rankPatterns(knowledge, task, config)
	kept, hidden, tokens := renderBudget(ranked, config)

	if len(ranked) != 3 {
		t.Fatalf("setup expected 3 ranked patterns, got %d", len(ranked))
	}
	if len(kept) != 1 {
		t.Fatalf("minimal budget must keep exactly 1 pattern, got %d", len(kept))
	}
	if len(kept[0].Cues) < 1 {
		t.Fatalf("top-1 pattern must render at least one cue, got %d", len(kept[0].Cues))
	}
	if kept[0].ID != ranked[0].ID {
		t.Fatalf("kept pattern %q must be the top-ranked %q", kept[0].ID, ranked[0].ID)
	}
	if hidden != len(ranked)-1 {
		t.Fatalf("hidden = %d, want %d", hidden, len(ranked)-1)
	}

	// The reported estimate is the real cost of the over-budget top-1.
	sum := estimateTokens(kept[0].Title) + estimateTokens(kept[0].ID)
	for _, cue := range kept[0].Cues {
		sum += estimateTokens(cue)
	}
	if tokens != sum || tokens <= config.MaxTokens {
		t.Fatalf("tokens = %d, want the over-budget top-1 cost %d", tokens, sum)
	}
}

// TestCueFormat verifies the cue line format, the reuse suffix rule, the
// CuesPerPattern cap and the cue ordering (validity, then reuse, then ID).
func TestCueFormat(t *testing.T) {
	knowledge := []observation.Knowledge{
		{ID: "K-a", Proposition: "proposed younger idea", State: "proposed", Topics: []string{"scope-cues"}},
		{ID: "K-b", Proposition: "confirmed workhorse with plenty of reuse", State: "confirmed", ReuseCount: 5, Topics: []string{"scope-cues"}},
		{ID: "K-c", Proposition: "challenged claim", State: "challenged", Topics: []string{"scope-cues"}},
		{ID: "K-d", Proposition: "unrated leftover without a state", Topics: []string{"scope-cues"}},
		{ID: "K-e", Proposition: "fresh unconfirmed idea", State: "confirmed", Topics: []string{"scope-cues"}},
	}

	config := Config{MaxPatterns: 7, MinScore: 0.1, MaxTokens: 600, CuesPerPattern: 3}
	kept, _, _ := renderBudget(rankPatterns(knowledge, "confirmed workhorse", config), config)

	if len(kept) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(kept))
	}
	cues := kept[0].Cues
	if len(cues) > 3 {
		t.Fatalf("cues = %d, must be <= CuesPerPattern 3", len(cues))
	}

	// Highest validity first: the confirmed item with reuse must lead.
	if !strings.HasPrefix(cues[0], "K-b: ") {
		t.Fatalf("first cue = %q, must start with %q", cues[0], "K-b: ")
	}
	if !strings.Contains(cues[0], ", reused 5") {
		t.Fatalf("cue %q must carry the reuse suffix", cues[0])
	}

	for _, cue := range cues {
		id, rest, ok := strings.Cut(cue, ": ")
		if !ok || id == "" || id != strings.TrimSpace(id) {
			t.Fatalf("cue %q must start with \"<knowledge_id>: \"", cue)
		}
		if n := len([]rune(cue)); n > 140 {
			t.Fatalf("cue is %d runes, must stay under 140: %q", n, cue)
		}
		if strings.Contains(rest, "reused 0") {
			t.Fatalf("cue %q must omit the reuse suffix at zero reuse", cue)
		}
	}

	// A never-reused confirmed item must render without the suffix.
	for _, cue := range cues {
		if strings.HasPrefix(cue, "K-e: ") && strings.Contains(cue, "reused") {
			t.Fatalf("zero-reuse cue must not mention reuse: %q", cue)
		}
	}
}

// TestEstimateTokens pins the token approximation formula.
func TestEstimateTokens(t *testing.T) {
	if got := estimateTokens("12345678"); got != 2 {
		t.Fatalf("estimateTokens(8 runes) = %d, want 2", got)
	}
	if got := estimateTokens("ab"); got != 1 {
		t.Fatalf("estimateTokens(2 runes) = %d, want ceil(0.5)=1", got)
	}
	if got := estimateTokens(""); got != 0 {
		t.Fatalf("estimateTokens(\"\") = %d, want 0", got)
	}
}
