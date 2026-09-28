package priming

import (
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
	pat := ExperiencePattern{
		Scope:        "auth",
		ActiveCount:  3,
		InvalidCount: 1,
		Signals: []Signal{
			{Kind: "success", Weight: 1.0},
			{Kind: "failure", Weight: 0.5},
		},
	}

	// Task mentions auth → should score high
	scoreAuth := scorePattern(pat, "fix auth bug")
	if scoreAuth < 1.0 {
		t.Fatalf("score for auth task = %f, want >=1.0", scoreAuth)
	}

	// Task doesn't mention auth → should score lower
	scoreOther := scorePattern(pat, "fix build system")
	if scoreOther >= scoreAuth {
		t.Fatalf("unrelated task score %f should be < auth task %f", scoreOther, scoreAuth)
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
