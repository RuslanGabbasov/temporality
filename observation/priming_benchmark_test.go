package observation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Priming benchmark corpus (docs/plan-priming-relevance.md §12): JSON fixtures
// under testdata/priming freeze real-shaped tasks, the knowledge available at
// the time, and labels of what was actually useful. Every algorithm change
// runs here; a regression against the captured floors fails CI (§12.3).
type benchmarkCase struct {
	Name  string `json:"name"`
	Query struct {
		Text               string      `json:"text"`
		Entities           []string    `json:"entities"`
		Topics             []string    `json:"topics"`
		Limit              int         `json:"limit"`
		ActiveSkills       []HintSkill `json:"active_skills"`
		IssuedKnowledgeIDs []string    `json:"issued_knowledge_ids"`
	} `json:"query"`
	Knowledge []Knowledge `json:"knowledge"`
	// Useful lists knowledge ids the task actually needed; hints outside it
	// hurt precision.
	Useful []string `json:"useful"`
	// Stale lists retired knowledge that must never surface as an acting
	// rule; any leak is a hard failure, not a metric.
	Stale []string `json:"stale"`
	// ExpectedOrder optionally pins the exact top order of the output — the
	// deterministic contract (§8) made assertable.
	ExpectedOrder []string `json:"expected_order,omitempty"`
	Budget        struct {
		MaxTokens    int `json:"max_tokens"`
		MaxCueTokens int `json:"max_cue_tokens"`
		MaxHints     int `json:"max_hints"`
	} `json:"budget"`
}

// Captured floors (corpus v1, lexical-entity-topic.v3, 2026-10-10). A change
// that drops the corpus below any floor is not accepted without either fixing
// the regression or re-justifying the floors together with the corpus.
const (
	benchmarkMinCases        = 30
	benchmarkPrecisionFloor  = 0.90
	benchmarkRecallFloor     = 0.85
	benchmarkRedundancyCeil  = 0.05
	benchmarkEfficiencyFloor = 5.0 // useful cues per 1000 tokens of the block
)

func loadBenchmarkCases(t *testing.T) []benchmarkCase {
	t.Helper()
	paths, err := filepath.Glob("testdata/priming/*.json")
	if err != nil {
		t.Fatalf("glob corpus: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no corpus files found under testdata/priming")
	}
	sort.Strings(paths)
	var cases []benchmarkCase
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var fileCases []benchmarkCase
		if err := json.Unmarshal(raw, &fileCases); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, fileCase := range fileCases {
			if fileCase.Name == "" {
				t.Fatalf("%s: a case must carry a name", path)
			}
			cases = append(cases, fileCase)
		}
	}
	if len(cases) < benchmarkMinCases {
		t.Fatalf("corpus shrank: %d cases, want at least %d — the benchmark is only meaningful on a stable corpus", len(cases), benchmarkMinCases)
	}
	return cases
}

func TestPrimingBenchmarkCorpusQuality(t *testing.T) {
	cases := loadBenchmarkCases(t)

	precisionSum, precisionCases := 0.0, 0
	recallSum, recallCases := 0.0, 0
	blockTokensTotal, renderedTotal, usefulRenderedTotal, redundantTotal := 0, 0, 0, 0

	for _, bc := range cases {
		query := HintQuery{
			Text: bc.Query.Text, Entities: bc.Query.Entities, Topics: bc.Query.Topics, Limit: bc.Query.Limit,
			ActiveSkills: bc.Query.ActiveSkills, IssuedKnowledgeIDs: bc.Query.IssuedKnowledgeIDs,
		}
		hints := FindHints(bc.Knowledge, query)
		// Determinism is a per-case invariant (§8), not an aggregate hope.
		replay := FindHints(bc.Knowledge, query)
		if len(replay) != len(hints) {
			t.Fatalf("%s: non-deterministic hint count %d vs %d", bc.Name, len(hints), len(replay))
		}
		for i := range hints {
			if hints[i].KnowledgeID != replay[i].KnowledgeID {
				t.Fatalf("%s: non-deterministic order at %d: %q vs %q", bc.Name, i, hints[i].KnowledgeID, replay[i].KnowledgeID)
			}
		}

		stale := make(map[string]bool, len(bc.Stale))
		for _, id := range bc.Stale {
			stale[id] = true
		}
		useful := make(map[string]bool, len(bc.Useful))
		for _, id := range bc.Useful {
			useful[id] = true
		}

		for _, hint := range hints {
			if stale[hint.KnowledgeID] {
				t.Fatalf("%s: stale leakage — retired knowledge %q offered as acting rule", bc.Name, hint.KnowledgeID)
			}
		}

		if len(bc.ExpectedOrder) > 0 {
			if len(hints) < len(bc.ExpectedOrder) {
				t.Fatalf("%s: got %d hints, expected at least %d in order %v", bc.Name, len(hints), len(bc.ExpectedOrder), bc.ExpectedOrder)
			}
			for i, want := range bc.ExpectedOrder {
				if hints[i].KnowledgeID != want {
					t.Fatalf("%s: order[%d] = %q, want %q (full: %v)", bc.Name, i, hints[i].KnowledgeID, want, hintIDs(hints))
				}
			}
		}

		opts := RenderOptions{MaxTokens: bc.Budget.MaxTokens, MaxCueTokens: bc.Budget.MaxCueTokens, MaxHints: bc.Budget.MaxHints}
		rendered := RenderHintBlock(hints, opts)
		if rendered.Block != "" && rendered.Tokens > opts.MaxTokens {
			t.Fatalf("%s: block tokens %d exceed budget %d", bc.Name, rendered.Tokens, opts.MaxTokens)
		}
		keptIDs := make(map[string]bool, len(rendered.KeptIDs))
		for _, id := range rendered.KeptIDs {
			keptIDs[id] = true
		}

		usefulRendered := 0
		for _, id := range rendered.KeptIDs {
			if useful[id] {
				usefulRendered++
			}
		}

		// Redundancy: rendered cues that near-duplicate an earlier rendered
		// cue (Jaccard over content tokens) spent budget on repetition.
		redundant := 0
		keptTokens := make([]map[string]bool, 0, rendered.Kept)
		for _, hint := range hints {
			if !keptIDs[hint.KnowledgeID] {
				continue
			}
			tokens := contentTokens(hint.Proposition)
			duplicate := false
			for _, existing := range keptTokens {
				if jaccard(tokens, existing) >= hintNearDupJaccard {
					duplicate = true
					break
				}
			}
			if duplicate {
				redundant++
			} else {
				keptTokens = append(keptTokens, tokens)
			}
		}

		if len(hints) > 0 {
			matched := 0
			for _, hint := range hints {
				if useful[hint.KnowledgeID] {
					matched++
				}
			}
			precisionSum += float64(matched) / float64(len(hints))
			precisionCases++
		}
		if len(bc.Useful) > 0 {
			recallSum += float64(usefulRendered) / float64(len(bc.Useful))
			recallCases++
		}
		blockTokensTotal += rendered.Tokens
		renderedTotal += rendered.Kept
		usefulRenderedTotal += usefulRendered
		redundantTotal += redundant
	}

	if precisionCases == 0 || recallCases == 0 {
		t.Fatal("corpus must contain cases with hints and labeled useful knowledge")
	}
	precision := precisionSum / float64(precisionCases)
	recall := recallSum / float64(recallCases)
	redundancy := 0.0
	if renderedTotal > 0 {
		redundancy = float64(redundantTotal) / float64(renderedTotal)
	}
	efficiency := 0.0
	if blockTokensTotal > 0 {
		efficiency = float64(usefulRenderedTotal) / float64(blockTokensTotal) * 1000
	}

	if precision < benchmarkPrecisionFloor {
		t.Errorf("precision@k = %.3f, floor %.3f (%d cases)", precision, benchmarkPrecisionFloor, precisionCases)
	}
	if recall < benchmarkRecallFloor {
		t.Errorf("recall@budget = %.3f, floor %.3f (%d cases)", recall, benchmarkRecallFloor, recallCases)
	}
	if redundancy > benchmarkRedundancyCeil {
		t.Errorf("redundancy ratio = %.3f, ceiling %.3f (%d/%d rendered)", redundancy, benchmarkRedundancyCeil, redundantTotal, renderedTotal)
	}
	if efficiency < benchmarkEfficiencyFloor {
		t.Errorf("token efficiency = %.2f useful cues per 1000 tokens, floor %.2f", efficiency, benchmarkEfficiencyFloor)
	}
	t.Logf("corpus: %d cases · precision@k=%.3f · recall@budget=%.3f · redundancy=%.3f · efficiency=%.1f/1000tok",
		len(cases), precision, recall, redundancy, efficiency)
}

func hintIDs(hints []Hint) []string {
	ids := make([]string, 0, len(hints))
	for _, hint := range hints {
		ids = append(ids, hint.KnowledgeID)
	}
	return ids
}
