package main

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/workspace"
)

// corpusCase mirrors testdata/conversation_corpus.json
// (docs/conversational-runtime.md §20).
type corpusCase struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Text        string `json:"text"`
	ActiveTask  bool   `json:"active_task"`
	Attachments bool   `json:"attachments"`
	Classifier *struct {
		Intent                string  `json:"intent"`
		Execution             string  `json:"execution"`
		Confidence            float64 `json:"confidence"`
		RequiresClarification bool    `json:"requires_clarification"`
	} `json:"classifier"`
	ExpectedDeterministic struct {
		Intent    string `json:"intent"`
		Execution string `json:"execution"`
		Level     string `json:"level"`
	} `json:"expected_deterministic"`
	ExpectedCascade struct {
		Intent         string `json:"intent"`
		Execution      string `json:"execution"`
		Level          string `json:"level"`
		ClassifierRuns int    `json:"classifier_calls"`
	} `json:"expected_cascade"`
}

// corpusCategories is the §20 minimum category set: the corpus is incomplete
// (and the test fails) until every category has at least one pinned case.
var corpusCategories = []string{
	"simple_question", "discussion", "continuation", "topic_switch",
	"research_request", "development_request", "task_amendment", "task_status",
	"cancellation", "humanrequest_reply", "artifact_approval", "parallel_approvals",
	"multi_target", "question_during_task", "multi_task", "multi_source",
	"duplicate_message", "attachments", "ambiguous_command", "untrusted_instructions",
}

func loadCorpus(t *testing.T) []corpusCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/conversation_corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Cases []corpusCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	require.NotEmpty(t, corpus.Cases)
	seen := map[string]bool{}
	for _, c := range corpus.Cases {
		require.NotEmpty(t, c.Category, "case %q needs a category", c.Name)
		seen[c.Category] = true
	}
	for _, cat := range corpusCategories {
		require.True(t, seen[cat], "corpus misses §20 category %q", cat)
	}
	return corpus.Cases
}

// scoreCase runs the cascade once and reports the decision plus how many
// classifier rounds it took (concurrency-safe: the counting classifier may
// be shared by parallel subtests).
func scoreCase(tc corpusCase, withModel bool) (ConversationIntentDecision, int) {
	calls := 0
	var mu sync.Mutex
	var classifier ConversationMessageClassifier
	if withModel && tc.Classifier != nil {
		scripted := *tc.Classifier
		classifier = func(context.Context, ConversationMessageClassifyInput) (ConversationMessageClassifyOutput, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return ConversationMessageClassifyOutput{
				Intent: scripted.Intent, Execution: scripted.Execution,
				Confidence: scripted.Confidence, RequiresClarification: scripted.RequiresClarification,
			}, nil
		}
	}
	decision := resolveConversationIntentCascade(context.Background(), classifier, "corpus", tc.Text, tc.Attachments, tc.ActiveTask)
	return decision, calls
}

// TestRegressionCorpus is the §20 evaluation: the corpus pins what the
// router must decide with and without the fast-model stage, per category.
// A failure here is a routing regression — fix the router or consciously
// update the corpus in the same commit.
func TestRegressionCorpus(t *testing.T) {
	cases := loadCorpus(t)
	perCategory := map[string]int{}
	for _, tc := range cases {
		perCategory[tc.Category]++
		t.Run(tc.Category+"/"+tc.Name, func(t *testing.T) {
			t.Parallel()

			deterministic, _ := scoreCase(tc, false)
			require.Equal(t, tc.ExpectedDeterministic.Intent, deterministic.Intent, "deterministic pass intent")
			require.Equal(t, tc.ExpectedDeterministic.Execution, deterministic.Execution, "deterministic pass execution")
			require.Equal(t, tc.ExpectedDeterministic.Level, deterministic.RouterLevel, "deterministic pass router level")

			cascade, calls := scoreCase(tc, true)
			require.Equal(t, tc.ExpectedCascade.Intent, cascade.Intent, "cascade pass intent")
			require.Equal(t, tc.ExpectedCascade.Execution, cascade.Execution, "cascade pass execution")
			require.Equal(t, tc.ExpectedCascade.Level, cascade.RouterLevel, "cascade pass router level")
			require.Equal(t, tc.ExpectedCascade.ClassifierRuns, calls, "cascade pass classifier rounds")

			// The two passes agree everywhere the model stage was not
			// consulted — the Router-first vs Lead-first comparison §20 asks
			// for: strategy differences must come from the classifier, never
			// from the deterministic layer drifting between passes.
			if calls == 0 {
				require.Equal(t, deterministic.Execution, cascade.Execution)
				require.Equal(t, deterministic.RouterLevel, cascade.RouterLevel)
			}
		})
	}
	for _, cat := range corpusCategories {
		t.Logf("category %-22s cases: %d", cat, perCategory[cat])
	}
}

// TestShadowAgreement pins the semantic mapping between the legacy intake
// classification and the runtime decision (docs §23): what counts as
// agreement, what is a real disagreement, what cannot be compared.
func TestShadowAgreement(t *testing.T) {
	t.Parallel()
	row := func(level, execution string) workspace.RouterShadowRow {
		return workspace.RouterShadowRow{RuntimeLevel: level, RuntimeExecution: execution}
	}
	// legacy work verdicts against runtime work routes
	require.Equal(t, "agree", shadowAgreement("task", row("deterministic", "agent_execution")))
	require.Equal(t, "agree", shadowAgreement("task", row("deterministic", "delegated_execution")))
	// legacy task where runtime would have just chatted — the finding shadow mode exists for
	require.Equal(t, "disagree", shadowAgreement("task", row("deterministic", "direct")))
	require.Equal(t, "disagree", shadowAgreement("task", row("lead_first", "direct")))
	// amendment: new work OR continuation inside active work are both correct
	require.Equal(t, "agree", shadowAgreement("amendment", row("deterministic", "agent_execution")))
	require.Equal(t, "agree", shadowAgreement("amendment", row("lead_first", "direct")))
	require.Equal(t, "disagree", shadowAgreement("amendment", row("deterministic", "direct")))
	// conversation-served classes
	for _, legacy := range []string{"status", "reply", "chatter", "cancel"} {
		require.Equal(t, "agree", shadowAgreement(legacy, row("deterministic", "direct")), legacy)
		require.Equal(t, "agree", shadowAgreement(legacy, row("fast_model", "clarification")), legacy)
	}
	require.Equal(t, "disagree", shadowAgreement("chatter", row("deterministic", "agent_execution")))
	// unknown label or an unscored message cannot be compared
	require.Equal(t, "incomparable", shadowAgreement("task", row("not_evaluated", "unknown")))
	require.Equal(t, "incomparable", shadowAgreement("hologram", row("deterministic", "direct")))
}

// TestShadowExcerpt pins the excerpt budget.
func TestShadowExcerpt(t *testing.T) {
	t.Parallel()
	require.Equal(t, "ok", shadowExcerpt("  ok\t"))
	long := make([]rune, shadowExcerptLen+50)
	for i := range long {
		long[i] = 'б'
	}
	require.Len(t, []rune(shadowExcerpt(string(long))), shadowExcerptLen)
}

// TestShadowDisabledBySetting pins the per-channel opt-out.
func TestShadowDisabledBySetting(t *testing.T) {
	t.Parallel()
	require.True(t, shadowEnabled(map[string]string{}), "default is on — shadow accumulates without anyone remembering a flag")
	require.True(t, shadowEnabled(map[string]string{"conversation_shadow_enabled": "true"}))
	require.False(t, shadowEnabled(map[string]string{"conversation_shadow_enabled": "false"}))
	require.False(t, shadowEnabled(map[string]string{"conversation_shadow_enabled": "0"}))
	require.False(t, shadowEnabled(map[string]string{"conversation_shadow_enabled": "off"}))
}
