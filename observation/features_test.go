package observation

import (
	"encoding/json"
	"sort"
	"testing"
)

// The exploration feature extractor (docs/agent-knowledge-exploration.md §4.2)
// must normalize case, punctuation, identifiers and Russian word forms while
// keeping short technical tokens alive. It intentionally shares nothing with
// the benchmark-tuned contentTokens of the priming matcher.
func TestKnowledgeFeatureTokensNormalization(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"russian word forms converge", "Миграция миграции миграцию миграциями", []string{"миграц"}},
		{"case and punctuation", "Docker-daemon, is RUNNING!", []string{"docker", "daemon", "running"}},
		{"camelCase splits", "buildRunnerConfig", []string{"build", "runner", "config"}},
		{"upper acronym run splits", "HTTPServerV2", []string{"http", "server", "v2"}},
		{"snake and kebab split", "run_command / pre-commit", []string{"run", "command", "pre", "commit"}},
		{"digits glue to letters", "v2 k8s jwt2024", []string{"v2", "k8s", "jwt2024"}},
		{"short tech tokens survive", "go r c js", []string{"go", "r", "c", "js"}},
		{"ё folds to е", "щёлкнул ёлка", []string{"щелкнул", "елка"}},
		{"stopwords drop", "the и как для нужно", nil},
		{"single letters drop unless whitelisted", "x y z", nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := knowledgeFeatureTokens(testCase.input)
			if len(got) != len(testCase.want) {
				t.Fatalf("tokens = %v, want %v", got, testCase.want)
			}
			for _, want := range testCase.want {
				if !got[want] {
					t.Fatalf("token %q missing in %v", want, got)
				}
			}
		})
	}
}

func TestKnowledgeFeatureTokensDeterministicJSON(t *testing.T) {
	// Map iteration order must never leak into serialized results: callers
	// marshal MatchedBy lists and token-derived data.
	tokens := knowledgeFeatureTokens("Docker daemon running миграции базы данных")
	encoded, err := json.Marshal(sortedTokenList(tokens))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	again, err := json.Marshal(sortedTokenList(knowledgeFeatureTokens("Docker daemon running миграции базы данных")))
	if err != nil {
		t.Fatalf("marshal again: %v", err)
	}
	if string(encoded) != string(again) {
		t.Fatalf("token extraction not stable: %s vs %s", encoded, again)
	}
}

func TestStemRussianWordKeepsShortStems(t *testing.T) {
	// Words already at the four-rune floor must not be stripped further.
	for _, word := range []string{"тест", "код", "бд"} {
		if got := stemRussianWord(word); got != word {
			t.Fatalf("stemRussianWord(%q) = %q, want unchanged", word, got)
		}
	}
	if got := stemRussianWord("тесты"); got != "тест" {
		t.Fatalf("stemRussianWord(тесты) = %q, want тест", got)
	}
}

func sortedTokenList(tokens map[string]bool) []string {
	result := make([]string, 0, len(tokens))
	for token := range tokens {
		result = append(result, token)
	}
	sort.Strings(result)
	return result
}
