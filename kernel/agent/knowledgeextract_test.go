package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

func TestExtractionIdentityStable(t *testing.T) {
	first := extractionIdentity("run-1")
	require.Equal(t, first, extractionIdentity("run-1"))
	require.NotEqual(t, first, extractionIdentity("run-2"))
	require.True(t, strings.HasPrefix(first, "extraction-"))
}

func TestExtractionKnowledgeIDMatchesIdenticalTextOnly(t *testing.T) {
	id := extractionKnowledgeID("repo", "The build requires Go 1.24.")
	require.Equal(t, id, extractionKnowledgeID("repo", "The  build\trequires Go 1.24."))
	// Different raw wording — even just casing — is a different node: the
	// journal projection rejects re-proposals of one id with different text,
	// so near-duplicates must rely on the similarity check, not the id.
	require.NotEqual(t, id, extractionKnowledgeID("repo", "the build requires go 1.24."))
	require.NotEqual(t, id, extractionKnowledgeID("other", "The build requires Go 1.24."))
	require.True(t, strings.HasPrefix(id, "ext/"))
}

func TestParseExtractionCandidatesToleratesDirtyOutput(t *testing.T) {
	payload := `{"candidates":[{"kind":"observation","proposition":"Deploy requires the out directory to exist.","evidence":["r/event/000007"],"confidence":0.8}]}`
	variants := []string{
		payload,
		"```json\n" + payload + "\n```",
		"<think>let me look at the trajectory</think>" + payload,
		"Here is the result:\n" + payload + "\nDone.",
	}
	for _, variant := range variants {
		parsed, err := parseExtractionCandidates(variant)
		require.NoError(t, err, "variant: %.40s", variant)
		require.Len(t, parsed.Candidates, 1)
		require.Equal(t, "observation", parsed.Candidates[0].Kind)
		require.Equal(t, 0.8, parsed.Candidates[0].Confidence)
	}
}

func TestParseExtractionCandidatesRejectsGarbage(t *testing.T) {
	_, err := parseExtractionCandidates("Waiting for the human answer was the whole point. Done.")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid JSON")
}

func TestHintUsageFeedbackClassifiesOfferedHints(t *testing.T) {
	events := []observation.Event{
		{Type: "hint.offered", Data: map[string]any{"hint_id": "h1", "knowledge_id": "ext/1", "proposition": "The fetch command accepts a -limit flag, not --count."}},
		{Type: "hint.offered", Data: map[string]any{"hint_id": "h2", "knowledge_id": "ext/2", "proposition": "The indexer skips vendored directories entirely by default."}},
		{Type: "hint.offered", Data: map[string]any{"hint_id": "h3", "knowledge_id": "ext/3", "proposition": "Deployments go through the gatekeeper service."}},
		// Duplicate offer and unrelated events must not produce feedback twice.
		{Type: "hint.offered", Data: map[string]any{"hint_id": "h1", "knowledge_id": "ext/1", "proposition": "The fetch command accepts a -limit flag, not --count."}},
		{Type: "run.started", Data: map[string]any{}},
	}
	trajectory := Trajectory{
		Summary: TrajectorySummary{Answer: "Used fetch with -limit to fetch the manifest; the count flag is not supported."},
		Turns:   []Turn{{Tools: []ToolStep{{Tool: "bash", Arguments: "{\"command\":\"fetch -limit manifest\"}"}, {Tool: "bash", Arguments: "{\"command\":\"deploy --via gatekeeper\"}"}}}},
	}
	feedback := hintUsageFeedback(events, trajectory)
	require.Len(t, feedback, 3)

	// Two shared distinctive terms (fetch, limit) → used, terms reported.
	require.True(t, feedback[0].Used)
	require.Equal(t, "h1", feedback[0].HintID)
	require.Contains(t, feedback[0].MatchedBy, "term:fetch")
	require.Contains(t, feedback[0].MatchedBy, "term:-limit")

	// No shared terms → ignored.
	require.False(t, feedback[1].Used)
	require.Empty(t, feedback[1].MatchedBy)

	// A single long rare term (gatekeeper) → used on its own.
	require.True(t, feedback[2].Used)
	require.Equal(t, []string{"term:gatekeeper"}, feedback[2].MatchedBy)
}

func TestHintReflectedRequiresDistinctiveEvidence(t *testing.T) {
	corpus := propositionTokens("fetch the manifest with limit")
	// A single short shared term is noise, not use.
	used, matched := hintReflected("The fetch tool retries on transient errors.", corpus)
	require.False(t, used)
	require.Empty(t, matched)
	// Everything the proposition asserts appears in the run's work.
	used, matched = hintReflected("The fetch command accepts a limit.", corpus)
	require.True(t, used)
	require.Len(t, matched, 2)
}

func TestValidateExtractionCandidatesContract(t *testing.T) {
	known := map[string]bool{"r/event/000007": true, "r/event/000009": true}
	existing := []existingKnowledge{{ID: "ext/known", Proposition: "The report command writes its output to out/report.txt."}}
	states := map[string]string{"ext/known": "proposed"}

	raw := []extractionCandidate{
		// Valid observation citing real events.
		{Kind: "observation", Proposition: "Deploy fails without the pre-created out directory.", Evidence: []string{"r/event/000007", "r/event/000009"}},
		// Same wording — collapses within the batch.
		{Kind: "observation", Proposition: "Deploy fails without the pre-created out  directory.", Evidence: []string{"r/event/000007"}},
		// Hallucinated evidence refs are dropped; this one keeps one real ref.
		{Kind: "claim", Proposition: "The README overstates the supported flags.", Evidence: []string{"made-up-ref", "r/event/000009"}, Confidence: 0.4},
		// No real evidence at all — dropped entirely (§8).
		{Kind: "claim", Proposition: "Something unverifiable happened during the run.", Evidence: []string{"made-up-ref"}},
		// Unsupported kind — dropped.
		{Kind: "opinion", Proposition: "The build system is unpleasant to work with.", Evidence: []string{"r/event/000007"}},
		// Too short — dropped.
		{Kind: "claim", Proposition: "too short", Evidence: []string{"r/event/000007"}},
		// Duplicate of existing knowledge — counted, skipped.
		{Kind: "observation", Proposition: "The report command writes its output to out/report.txt.", Evidence: []string{"r/event/000007"}},
		// Empty kind defaults to claim.
		{Kind: "", Proposition: "The migration runner requires network access to succeed.", Evidence: []string{"r/event/000009"}},
	}
	accepted, duplicates, invalid := validateExtractionCandidates(raw, known, existing, states, "repo")

	require.Len(t, accepted, 3)
	require.Equal(t, 2, duplicates)
	require.Equal(t, 3, invalid)

	require.Equal(t, "observation", accepted[0].Kind)
	require.Equal(t, extractionKnowledgeID("repo", "Deploy fails without the pre-created out directory."), accepted[0].KnowledgeID)
	require.False(t, accepted[0].Existing)
	require.Equal(t, []string{"r/event/000007", "r/event/000009"}, accepted[0].Evidence)

	require.Equal(t, "claim", accepted[1].Kind)
	require.Equal(t, []string{"r/event/000009"}, accepted[1].Evidence, "hallucinated refs must be filtered out")
	require.Equal(t, 0.4, accepted[1].Confidence)

	require.Equal(t, "claim", accepted[2].Kind, "empty kind defaults to claim")
}

func TestValidateExtractionCandidatesMarksExisting(t *testing.T) {
	proposition := "Deploy requires the out directory to exist."
	id := extractionKnowledgeID("repo", proposition)
	raw := []extractionCandidate{{Kind: "observation", Proposition: proposition, Evidence: []string{"r/event/000001"}}}
	states := map[string]string{id: "confirmed"}
	accepted, _, _ := validateExtractionCandidates(raw, map[string]bool{"r/event/000001": true}, nil, states, "repo")
	require.Len(t, accepted, 1)
	require.True(t, accepted[0].Existing, "already-projected ids must be strengthened, not re-proposed")

	terminal := map[string]string{id: "invalidated"}
	accepted, _, _ = validateExtractionCandidates(raw, map[string]bool{"r/event/000001": true}, nil, terminal, "repo")
	require.Len(t, accepted, 1)
	require.False(t, accepted[0].Existing, "terminal-state ids must not be strengthened")
}

func TestValidateExtractionCandidatesStrengthensExactReDerivation(t *testing.T) {
	proposition := "The fetch command accepts a -limit flag, not --count."
	id := extractionKnowledgeID("repo", proposition)
	known := map[string]bool{"r/event/000001": true}
	states := map[string]string{id: "proposed"}
	raw := []extractionCandidate{{Kind: "observation", Proposition: proposition, Evidence: []string{"r/event/000001"}}}

	// Hint-surfaced project knowledge carries the same deterministic id: the
	// exact restatement must strengthen the node, not count as a duplicate.
	project := []existingKnowledge{{ID: id, Proposition: proposition}}
	accepted, duplicates, _ := validateExtractionCandidates(raw, known, project, states, "repo")
	require.Equal(t, 0, duplicates, "an exact re-derivation is not a duplicate")
	require.Len(t, accepted, 1)
	require.True(t, accepted[0].Existing, "re-derivation must strengthen the existing node")

	// The same restatement recorded by this run (remember) is not independent
	// re-derivation and must stay skipped.
	fromRun := []existingKnowledge{{ID: id, Proposition: proposition, FromRun: true}}
	accepted, duplicates, _ = validateExtractionCandidates(raw, known, fromRun, states, "repo")
	require.Equal(t, 1, duplicates, "this-run restatement must be skipped")
	require.Empty(t, accepted)
}

func TestValidateExtractionCandidatesCapsBatch(t *testing.T) {
	raw := make([]extractionCandidate, 0, 8)
	for i := 0; i < 8; i++ {
		raw = append(raw, extractionCandidate{Kind: "claim", Proposition: fmt.Sprintf("Durable fact number %d about the project tooling.", i), Evidence: []string{"r/event/000001"}})
	}
	accepted, _, _ := validateExtractionCandidates(raw, map[string]bool{"r/event/000001": true}, nil, nil, "repo")
	require.Len(t, accepted, extractionMaxCandidates)
}

func TestValidateExtractionCandidatesContradicts(t *testing.T) {
	known := map[string]bool{"r/event/000001": true}
	// Near-duplicate wording: without contradicts this candidate is dropped as
	// a restatement; with it, it is the replacement fact.
	oldProposition := "The fetch command accepts a -limit flag, not --count."
	newProposition := "The fetch command accepts a --count flag, not -limit."
	oldID := "ext/old"
	existing := []existingKnowledge{{ID: oldID, Proposition: oldProposition}}
	states := map[string]string{oldID: "confirmed"}

	raw := []extractionCandidate{
		{Kind: "observation", Proposition: newProposition, Evidence: []string{"r/event/000001"}, Contradicts: oldID},
		// Hallucinated ref — dropped, candidate survives as a plain proposal.
		{Kind: "observation", Proposition: "The report command writes its output to out/report.txt today.", Evidence: []string{"r/event/000001"}, Contradicts: "ext/missing"},
		// Terminal target — cleared, fact survives.
		{Kind: "observation", Proposition: "The migration runner requires network access to succeed always.", Evidence: []string{"r/event/000001"}, Contradicts: "ext/dead"},
		// Self-contradiction — cleared.
		{Kind: "observation", Proposition: "The indexer skips vendored directories by default entirely.", Evidence: []string{"r/event/000001"}, Contradicts: extractionKnowledgeID("repo", "The indexer skips vendored directories by default entirely.")},
	}
	states["ext/dead"] = "invalidated"
	accepted, duplicates, _ := validateExtractionCandidates(raw, known, existing, states, "repo")
	require.Equal(t, 0, duplicates, "an explicit contradiction must bypass the similarity dedup")
	require.Len(t, accepted, 4)
	require.Equal(t, oldID, accepted[0].Contradicts)
	require.Empty(t, accepted[1].Contradicts)
	require.Empty(t, accepted[2].Contradicts)
	require.Empty(t, accepted[3].Contradicts)
}

func TestAgingCandidatesSelectsStaleProposals(t *testing.T) {
	now := time.Now()
	old := now.Add(-21 * 24 * time.Hour)
	fresh := now.Add(-2 * 24 * time.Hour)
	recentlyUsed := now.Add(-30 * 24 * time.Hour)
	usedAt := now.Add(-1 * 24 * time.Hour)
	knowledge := []observation.Knowledge{
		{ID: "ext/stale", Proposition: "Old unconfirmed fact.", State: "proposed", CreatedAt: old},
		{ID: "ext/fresh", Proposition: "Recent proposal.", State: "proposed", CreatedAt: fresh},
		{ID: "ext/confirmed", Proposition: "Confirmed fact.", State: "confirmed", CreatedAt: old},
		{ID: "ext/used", Proposition: "Used but unconfirmed.", State: "proposed", CreatedAt: recentlyUsed, LastUsedAt: &usedAt},
		{ID: "ext/challenged", Proposition: "Already challenged.", State: "challenged", CreatedAt: old},
	}
	candidates := agingCandidates(knowledge, now)
	require.Len(t, candidates, 1)
	require.Equal(t, "ext/stale", candidates[0].KnowledgeID)
	require.InDelta(t, 21.0, candidates[0].AgeDays, 0.1)
}

func TestAgingCandidatesCapsAndOrdersOldestFirst(t *testing.T) {
	now := time.Now()
	knowledge := make([]observation.Knowledge, 0, agingMaxPerPass+2)
	for i := 0; i < agingMaxPerPass+2; i++ {
		knowledge = append(knowledge, observation.Knowledge{
			ID: fmt.Sprintf("ext/old-%02d", i), State: "proposed",
			CreatedAt: now.Add(time.Duration(-20-i) * 24 * time.Hour),
		})
	}
	candidates := agingCandidates(knowledge, now)
	require.Len(t, candidates, agingMaxPerPass)
	require.Equal(t, "ext/old-11", candidates[0].KnowledgeID, "oldest first")
}

func TestPropositionSimilarConservative(t *testing.T) {
	require.True(t, propositionSimilar(
		"The report command writes its output to out/report.txt.",
		"The report command writes output to out/report.txt.",
	))
	require.False(t, propositionSimilar(
		"The report command writes its output to out/report.txt.",
		"The fetch command accepts a -limit flag.",
	))
}

// extractionJournalStub serves the two journal endpoints extraction needs: the
// run's event stream and the project knowledge projection.
func extractionJournalStub(t *testing.T, events []observation.Event, knowledge []observation.Knowledge) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/observations/events":
			run := r.URL.Query().Get("run")
			var matching []observation.Event
			for _, event := range events {
				if event.Context.Run == run {
					matching = append(matching, event)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"events": matching, "count": len(matching), "next_cursor": ""})
		case "/v1/observations/knowledge":
			_ = json.NewEncoder(w).Encode(map[string]any{"knowledge": knowledge, "count": len(knowledge)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func extractionRunEvents(run string) []observation.Event {
	base := time.Now().Add(-time.Hour)
	event := func(offset int, eventType, eventID string, data map[string]any) observation.Event {
		return observation.Event{
			Schema: observation.Schema, EventID: eventID, OccurredAt: base.Add(time.Duration(offset) * time.Second),
			Source:  observation.Source{ID: "kernel", Integration: "agent-kernel"},
			Context: observation.Context{Project: "repo", Run: run},
			Type:    eventType, Data: data,
		}
	}
	return []observation.Event{
		event(0, "run.started", "r/event/000001", nil),
		event(1, "turn.started", "r/event/000002", map[string]any{"turn": 1}),
		event(2, "model.completed", "r/event/000003", map[string]any{"turn": 1, "total_tokens": 100}),
		event(3, "tool.started", "r/event/000004", map[string]any{"operation_id": "op-1", "tool": "run_command", "arguments": `{"command":["go","run",".","report"]}`}),
		event(4, "tool.completed", "r/event/000005", map[string]any{"operation_id": "op-1", "tool": "run_command", "output": "report failed: out/ directory missing", "exit_code": float64(1)}),
		event(5, "turn.completed", "r/event/000006", map[string]any{"turn": 1}),
		event(6, "run.completed", "r/event/000007", map[string]any{"turns": 1}),
		event(7, "agent.summary", "r/event/000008", map[string]any{"answer": "The report command needs an existing out directory."}),
	}
}

func TestExtractKnowledgeSkipsAlreadyExtractedRun(t *testing.T) {
	events := extractionRunEvents("run-done")
	marker := observation.Event{
		Schema: observation.Schema, EventID: "r/event/000009", OccurredAt: time.Now(),
		Source:  observation.Source{ID: "kernel", Integration: "agent-kernel"},
		Context: observation.Context{Project: "repo", Run: "run-done"},
		Type:    extractionCompletedEvent,
		Data:    map[string]any{"extraction_id": extractionIdentity("run-done"), "candidates_count": 0},
	}
	events = append(events, marker)
	server := extractionJournalStub(t, events, nil)
	activities := &Activities{HTTP: server.Client(), TemporalityURL: server.URL}

	result, err := activities.ExtractKnowledge(context.Background(), KnowledgeExtractRequest{Project: "repo", RunID: "run-done", ExtractionID: extractionIdentity("run-done")})
	require.NoError(t, err)
	require.True(t, result.Skipped)
	require.Empty(t, result.Candidates)
}

func TestExtractKnowledgeProposesCandidatesFromTrajectory(t *testing.T) {
	events := extractionRunEvents("run-1")
	llmStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := `{"choices":[{"message":{"content":"{\"candidates\":[{\"kind\":\"observation\",\"proposition\":\"The report command requires a pre-existing out directory and fails with exit 1 otherwise.\",\"evidence\":[\"r/event/000005\"],\"confidence\":0.9}]}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(llmStub.Close)
	journal := extractionJournalStub(t, events, nil)
	activities := &Activities{
		HTTP:           journal.Client(),
		TemporalityURL: journal.URL,
		Model:          llm.New(llm.Config{BaseURL: llmStub.URL, Model: "stub", Timeout: 10 * time.Second}),
	}

	result, err := activities.ExtractKnowledge(context.Background(), KnowledgeExtractRequest{Project: "repo", RunID: "run-1", Prompt: "run the report", ExtractionID: extractionIdentity("run-1")})
	require.NoError(t, err)
	require.False(t, result.Skipped)
	require.Len(t, result.Candidates, 1)

	candidate := result.Candidates[0]
	require.Equal(t, "observation", candidate.Kind)
	require.Equal(t, extractionKnowledgeID("repo", candidate.Proposition), candidate.KnowledgeID)
	require.Equal(t, []string{"r/event/000005"}, candidate.Evidence)
	require.False(t, candidate.Existing)
	require.Equal(t, 0.9, candidate.Confidence)
	require.GreaterOrEqual(t, result.DurationMs, int64(0))
}

func TestExtractKnowledgeDropsDuplicatesOfExistingKnowledge(t *testing.T) {
	events := extractionRunEvents("run-2")
	duplicate := "The report command requires a pre-existing out directory and fails with exit 1 otherwise."
	llmStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, _ := json.Marshal(map[string]any{"candidates": []map[string]any{{"kind": "observation", "proposition": duplicate, "evidence": []string{"r/event/000005"}}}})
		reply, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": string(content)}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	}))
	t.Cleanup(llmStub.Close)
	knowledge := []observation.Knowledge{{ID: "ext/old", Proposition: duplicate, State: "confirmed"}}
	journal := extractionJournalStub(t, events, knowledge)
	activities := &Activities{
		HTTP:           journal.Client(),
		TemporalityURL: journal.URL,
		Model:          llm.New(llm.Config{BaseURL: llmStub.URL, Model: "stub", Timeout: 10 * time.Second}),
	}

	result, err := activities.ExtractKnowledge(context.Background(), KnowledgeExtractRequest{Project: "repo", RunID: "run-2", Prompt: "run the report", ExtractionID: extractionIdentity("run-2")})
	require.NoError(t, err)
	require.Empty(t, result.Candidates)
	require.Equal(t, 1, result.Duplicates)
}

func TestRenderExtractionContextCitesEventIDs(t *testing.T) {
	trajectory := ExtractTrajectory(toEventLikes(extractionRunEvents("run-ctx")))
	rendered := renderExtractionContext(KnowledgeExtractRequest{Project: "repo", RunID: "run-ctx", Prompt: "run the report"}, trajectory, nil, nil)
	require.Contains(t, rendered, "run_command [failed] r/event/000004")
	require.Contains(t, rendered, "report failed: out/ directory missing")
	require.Contains(t, rendered, "Final answer:")
	require.Contains(t, rendered, "The report command needs an existing out directory.")

	existing := []existingKnowledge{{ID: "ext/known", Proposition: "The report command writes its output to out/report.txt."}}
	rendered = renderExtractionContext(KnowledgeExtractRequest{Project: "repo", RunID: "run-ctx"}, trajectory, existing, nil)
	require.Contains(t, rendered, "- [ext/known] The report command writes its output to out/report.txt.")
	require.Contains(t, rendered, "contradicts")
}

func TestParseExtractionCandidatesReadsContradicts(t *testing.T) {
	payload := `{"candidates":[{"kind":"observation","proposition":"The fetch command accepts a --count flag, not -limit.","evidence":["r/event/000005"],"confidence":0.9,"contradicts":"ext/old"}]}`
	parsed, err := parseExtractionCandidates(payload)
	require.NoError(t, err)
	require.Len(t, parsed.Candidates, 1)
	require.Equal(t, "ext/old", parsed.Candidates[0].Contradicts)
}

func TestValidateHintOutcomesKeepsOnlyRealOffers(t *testing.T) {
	offered := []offeredHint{
		{HintID: "h1", KnowledgeID: "ext/1", Proposition: "The fetch command accepts a -limit flag."},
		{HintID: "h2", KnowledgeID: "ext/2", Proposition: "The indexer skips vendored directories."},
	}
	raw := []extractionHintOutcome{
		{HintID: "h1", Outcome: "helpful"},
		// Same hint judged twice — the second verdict is dropped.
		{HintID: "h1", Outcome: "harmful"},
		// A hint this run never saw: hallucinated id, must not survive.
		{HintID: "h-ghost", Outcome: "helpful"},
		// Not a verdict the contract knows.
		{HintID: "h2", Outcome: "neutral"},
		{HintID: "h2", Outcome: "harmful"},
	}
	outcomes := validateHintOutcomes(raw, offered)
	require.Len(t, outcomes, 2)
	require.Equal(t, HintOutcome{HintID: "h1", KnowledgeID: "ext/1", Outcome: "helpful"}, outcomes[0])
	require.Equal(t, HintOutcome{HintID: "h2", KnowledgeID: "ext/2", Outcome: "harmful"}, outcomes[1])

	// No offers means nothing to judge — even a full list of verdicts.
	require.Empty(t, validateHintOutcomes(raw, nil))
	require.Empty(t, validateHintOutcomes(nil, offered))
}

func TestParseExtractionCandidatesReadsHintOutcomes(t *testing.T) {
	payload := `{"candidates":[],"hint_outcomes":[{"hint_id":"h1","outcome":"helpful"},{"hint_id":"h2","outcome":"harmful"}]}`
	parsed, err := parseExtractionCandidates("```json\n" + payload + "\n```")
	require.NoError(t, err)
	require.Len(t, parsed.HintOutcomes, 2)
	require.Equal(t, "h1", parsed.HintOutcomes[0].HintID)
	require.Equal(t, "helpful", parsed.HintOutcomes[0].Outcome)
}

func TestRenderExtractionContextListsOfferedHints(t *testing.T) {
	request := KnowledgeExtractRequest{Project: "repo-a", RunID: "run-h", Prompt: "fetch the manifest"}
	trajectory := Trajectory{Summary: TrajectorySummary{Answer: "done"}}
	offered := []offeredHint{
		{HintID: "h1", KnowledgeID: "ext/1", Proposition: "The fetch command accepts a -limit flag."},
	}
	rendered := renderExtractionContext(request, trajectory, nil, offered)
	require.Contains(t, rendered, "Hints offered to this run")
	require.Contains(t, rendered, "[h1] The fetch command accepts a -limit flag.")

	// Without offers the section is absent — most runs see no hints.
	require.NotContains(t, renderExtractionContext(request, trajectory, nil, nil), "Hints offered")
}
