package agent

import (
	"testing"
)

func skillIndexEntryForTest(id, name, description, version string) skillIndexEntry {
	var entry skillIndexEntry
	entry.ID, entry.Name, entry.Description, entry.Version = id, name, description, version
	entry.Manifest.Capabilities = []string{"testing"}
	entry.Manifest.Tools = []string{"run_command"}
	return entry
}

// TestRankSkillMatchesHybrid pins the two-tier behavior: lexical hits lead,
// semantic matches surface cross-language relevance that keywords miss, an
// unavailable embedder (all-zero cosines) keeps the pure lexical behavior,
// and an empty result hands over to the all_skills index.
func TestRankSkillMatchesHybrid(t *testing.T) {
	entries := []skillIndexEntry{
		skillIndexEntryForTest("tdd", "Test-Driven Development", "write the failing test first", "1"),
		skillIndexEntryForTest("api-review", "Code Review", "review an API design for quality", "1"),
		skillIndexEntryForTest("deploy", "Deployment", "ship a service to production", "1"),
	}
	item := func(e skillIndexEntry) map[string]any {
		return map[string]any{"id": e.ID, "name": e.Name}
	}
	scoredFor := func(scores ...int) []skillScoredMatch {
		out := make([]skillScoredMatch, len(scores))
		for i, s := range scores {
			out[i] = skillScoredMatch{item: item(entries[i]), score: s}
		}
		return out
	}

	// Russian query against English skills: no lexical hits anywhere, but the
	// review skill sits above the semantic bar and must surface.
	scored := scoredFor(0, 0, 0)
	cosines := []float32{0.31, 0.72, 0.12}
	matches := rankSkillMatches(scored, cosines)
	if len(matches) != 1 || matches[0]["id"] != "api-review" {
		t.Fatalf("semantic match missing: %v", matches)
	}

	// Lexical hits keep priority over stronger semantic-only matches.
	scored = scoredFor(2, 0, 0)
	cosines = []float32{0.31, 0.99, 0.05}
	matches = rankSkillMatches(scored, cosines)
	if len(matches) != 2 || matches[0]["id"] != "tdd" || matches[1]["id"] != "api-review" {
		t.Fatalf("lexical priority broken: %v", matches)
	}

	// Embedder unavailable (all zeros): below-bar skills drop out, only the
	// lexical match survives — the pre-semantic behavior.
	scored = scoredFor(1, 0, 0)
	matches = rankSkillMatches(scored, []float32{0, 0, 0})
	if len(matches) != 1 || matches[0]["id"] != "tdd" {
		t.Fatalf("lexical-only fallback broken: %v", matches)
	}
	scored = scoredFor(0, 0, 0)
	if matches := rankSkillMatches(scored, []float32{0, 0, 0}); len(matches) != 0 {
		t.Fatalf("all_skills index expected, got %v", matches)
	}

	// Semantic ties break by registry order (stable sort).
	scored = scoredFor(0, 0, 0)
	matches = rankSkillMatches(scored, []float32{0.7, 0.7, 0.7})
	if len(matches) != 3 || matches[0]["id"] != "tdd" {
		t.Fatalf("stable order broken: %v", matches)
	}
}

// TestCosine32 pins the similarity math: identical vectors are 1, orthogonal
// are 0, zero vectors never divide by zero.
func TestCosine32(t *testing.T) {
	if got := cosine32([]float32{1, 0}, []float32{1, 0}); got < 0.99 {
		t.Fatalf("identical vectors: %v", got)
	}
	if got := cosine32([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Fatalf("orthogonal vectors: %v", got)
	}
	if got := cosine32(nil, []float32{1, 1}); got != 0 {
		t.Fatalf("zero vector: %v", got)
	}
	// Scale-independence: e5 vectors are unnormalized.
	a := []float32{3, 4}
	b := []float32{6, 8}
	if got := cosine32(a, b); got < 0.99 {
		t.Fatalf("scale must not matter: %v", got)
	}
}
