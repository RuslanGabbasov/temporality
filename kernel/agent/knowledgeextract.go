// knowledgeextract turns a completed run's trajectory into candidate knowledge
// (docs/knowledge-extraction.md). After run.completed the kernel replays the
// run's events through one model call and returns 0..N candidates; the workflow
// emits them as knowledge.proposed with evidence refs pointing at real
// trajectory events. Extraction is best-effort: a failure is recorded as
// knowledge.extraction.failed and never affects the run's own result.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// ExtractorVersion identifies the extraction prompt and heuristics. Bumping it
// changes extractionIdentity, so every run becomes eligible for re-extraction
// under the new version (docs/knowledge-extraction.md §27).
const ExtractorVersion = "knowledge-extractor.v2"

// Extraction observability events. They are markers on the run's trajectory,
// not knowledge lifecycle transitions, so the projection skips them.
const (
	extractionStartedEvent   = "knowledge.extraction.started"
	extractionCompletedEvent = "knowledge.extraction.completed"
	extractionFailedEvent    = "knowledge.extraction.failed"
)

// ExtractionReverificationRule marks a knowledge.confirmed event produced by an
// independent extraction that re-derived an existing proposal.
const ExtractionReverificationRule = "extraction-reverification.v1"

// ExtractionReuseRule marks a knowledge.used event produced by an extraction
// that re-derived already-confirmed knowledge.
const ExtractionReuseRule = "extraction-reuse.v1"

// ExtractionContradictionRule marks a knowledge.challenged event produced by an
// extraction whose run directly disproved an existing knowledge item
// (docs/knowledge-evolution.md, scenario C).
const ExtractionContradictionRule = "extraction-contradiction.v1"

// AgingRule marks a knowledge.challenged event produced by the aging sweep:
// a proposal that stayed unconfirmed and unused past AgingThreshold.
const AgingRule = "aging.v1"

// AgingThreshold is how long a proposal may stay unconfirmed and unused before
// the sweep marks it challenged (at risk). Any real use or a later
// re-derivation confirms it again, so aging is reversible.
const AgingThreshold = 14 * 24 * time.Hour

// agingMaxPerPass bounds the aging sweep so one extraction pass cannot flood
// the journal with challenge events.
const agingMaxPerPass = 10

const (
	extractionMaxCandidates = 5
	extractionMaxToolSteps  = 60
	extractionMaxExisting   = 12
	extractionPromptLimit   = 2 * 1024
	extractionAnswerLimit   = 2 * 1024
	extractionArgsLimit     = 120
	extractionOutputLimit   = 300
)

// KnowledgeExtractRequest identifies the finished run to analyse.
type KnowledgeExtractRequest struct {
	Project      string
	RunID        string
	TaskID       string
	ActorID      string
	Prompt       string
	ExtractionID string
	Model        string
}

// KnowledgeCandidate is one validated knowledge proposal ready to be emitted.
// Existing marks candidates whose knowledge_id is already projected: the
// workflow strengthens the node (confirm/use) instead of re-proposing it.
// Contradicts names an existing knowledge item this run disproved: the
// workflow proposes the corrected fact and challenges the stale one.
type KnowledgeCandidate struct {
	KnowledgeID string
	Kind        string // observation | claim
	Proposition string
	Confidence  float64
	Evidence    []string // real event ids from the analysed run
	Existing    bool
	Contradicts string // existing knowledge id disproved by this run, if any
}

// AgingCandidate is one stale proposal selected by the aging sweep.
type AgingCandidate struct {
	KnowledgeID string
	Proposition string
	AgeDays     float64
}

// HintFeedback reports whether a hint offered to this run was actually
// reflected in the run's work (docs/knowledge-evolution.md §2 Hint: applied
// context and influence must be determinable).
type HintFeedback struct {
	HintID      string
	KnowledgeID string
	Proposition string
	Used        bool
	MatchedBy   []string
}

// KnowledgeExtractResult reports what the extractor produced.
type KnowledgeExtractResult struct {
	ExtractionID string
	Candidates   []KnowledgeCandidate
	Duplicates   int              // dropped as restatements of existing knowledge
	Invalid      int              // dropped by validation (no real evidence, bad kind, ...)
	Aging        []AgingCandidate // stale unconfirmed proposals to challenge
	HintFeedback []HintFeedback   // offered hints classified as used/ignored by this run
	Model        string
	DurationMs   int64
	Skipped      bool // this run was already extracted with this version
}

// extractionIdentity is the stable id of one extraction pass over a run.
func extractionIdentity(runID string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + ExtractorVersion))
	return "extraction-" + hex.EncodeToString(digest[:6])
}

// extractionKnowledgeID is the project-scoped identity of a candidate. It is
// derived from the raw (whitespace-collapsed) proposition — never a normalized
// one — because the journal projection rejects re-proposals of the same id with
// different text. Two extractions of the same wording land on the same node;
// near-duplicates are caught by the similarity check instead.
func extractionKnowledgeID(project, proposition string) string {
	collapsed := strings.Join(strings.Fields(proposition), " ")
	digest := sha256.Sum256([]byte(project + "\x00" + collapsed))
	return "ext/" + hex.EncodeToString(digest[:12])
}

type existingKnowledge struct {
	ID          string
	Proposition string
	// FromRun marks knowledge the run itself recorded (remember tool): an
	// extraction re-statement of it is not independent re-derivation.
	FromRun bool
}

// ExtractKnowledge is the activity behind run.completed extraction: it loads
// the run's events from the journal, builds the compact trajectory context,
// retrieves relevant existing knowledge, and asks the model for candidates.
func (a *Activities) ExtractKnowledge(ctx context.Context, request KnowledgeExtractRequest) (KnowledgeExtractResult, error) {
	started := time.Now()
	if request.ExtractionID == "" {
		request.ExtractionID = extractionIdentity(request.RunID)
	}
	result := KnowledgeExtractResult{ExtractionID: request.ExtractionID, Model: request.Model}
	events, err := a.fetchRunEvents(ctx, request.Project, request.RunID)
	if err != nil {
		return result, fmt.Errorf("load run events: %w", err)
	}
	// Idempotency (§26): a recorded completed marker for this extraction id
	// means the pass already ran; replaying must not propose items twice.
	for _, event := range events {
		if event.Type == extractionCompletedEvent && stringField(event.Data, "extraction_id") == request.ExtractionID {
			result.Skipped = true
			return result, nil
		}
	}
	if a.Model == nil {
		return result, errors.New("model client is not configured")
	}
	trajectory := ExtractTrajectory(toEventLikes(events))
	knownEvents := make(map[string]bool, len(events))
	for _, event := range events {
		knownEvents[event.EventID] = true
	}
	// One knowledge load serves both the dedup/contradiction context and the
	// aging sweep. Best-effort: without it extraction sees less context.
	knowledge, _ := a.projectKnowledge(ctx, request.Project)
	existing, states := extractionContext(request, trajectory, knowledge)
	user := renderExtractionContext(request, trajectory, existing)
	completion, err := completeParsed(ctx, a.Model, []llm.Message{
		{Role: "system", Content: extractionSystemPrompt},
		{Role: "user", Content: user},
	}, parseExtractionCandidates)
	if err != nil {
		return result, err
	}
	accepted, duplicates, invalid := validateExtractionCandidates(completion.Candidates, knownEvents, existing, states, request.Project)
	result.Candidates = accepted
	result.Duplicates = duplicates
	result.Invalid = invalid
	result.Aging = agingCandidates(knowledge, time.Now())
	result.HintFeedback = hintUsageFeedback(events, trajectory)
	result.DurationMs = time.Since(started).Milliseconds()
	return result, nil
}

// extractionContext assembles the dedup and contradiction context: knowledge
// recorded during the run itself plus relevant existing knowledge (§19-20).
// The project projection arrives pre-loaded by the caller.
func extractionContext(request KnowledgeExtractRequest, trajectory Trajectory, knowledge []observation.Knowledge) ([]existingKnowledge, map[string]string) {
	var existing []existingKnowledge
	for _, item := range trajectory.Knowledge {
		if item.Proposition != "" {
			existing = append(existing, existingKnowledge{ID: item.KnowledgeID, Proposition: item.Proposition, FromRun: true})
		}
	}
	states := map[string]string{}
	for _, item := range knowledge {
		states[item.ID] = item.State
	}
	if len(knowledge) > 0 {
		query := strings.TrimSpace(request.Prompt + "\n" + trajectory.Summary.Answer)
		for _, hint := range observation.FindHints(knowledge, observation.HintQuery{Text: query, Limit: 8}) {
			existing = append(existing, existingKnowledge{ID: hint.KnowledgeID, Proposition: hint.Proposition})
		}
	}
	return existing, states
}

// projectKnowledge loads the full knowledge projection for a project. Unlike
// the hints endpoint it records no hint events, so extraction does not pollute
// hint telemetry.
func (a *Activities) projectKnowledge(ctx context.Context, project string) ([]observation.Knowledge, error) {
	target := fmt.Sprintf("%s/v1/observations/knowledge?project=%s", a.TemporalityURL, url.QueryEscape(project))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if a.APIToken != "" {
		request.Header.Set("Authorization", "Bearer "+a.APIToken)
	}
	response, err := a.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("journal knowledge returned HTTP %d", response.StatusCode)
	}
	var reply struct {
		Knowledge []observation.Knowledge `json:"knowledge"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return nil, err
	}
	return reply.Knowledge, nil
}

// fetchRunEvents loads the complete event stream of one run from the journal.
func (a *Activities) fetchRunEvents(ctx context.Context, project, runID string) ([]observation.Event, error) {
	var events []observation.Event
	cursor := ""
	for {
		query := fmt.Sprintf("project=%s&run=%s&limit=500", url.QueryEscape(project), url.QueryEscape(runID))
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.TemporalityURL+"/v1/observations/events?"+query, nil)
		if err != nil {
			return nil, err
		}
		if a.APIToken != "" {
			request.Header.Set("Authorization", "Bearer "+a.APIToken)
		}
		response, err := a.HTTP.Do(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("journal events returned HTTP %d", response.StatusCode)
		}
		var page struct {
			Events     []observation.Event `json:"events"`
			NextCursor string              `json:"next_cursor"`
		}
		err = json.NewDecoder(response.Body).Decode(&page)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		events = append(events, page.Events...)
		if page.NextCursor == "" || len(page.Events) == 0 {
			return events, nil
		}
		cursor = page.NextCursor
	}
}

// toEventLikes adapts journal events to the trajectory extraction input.
func toEventLikes(events []observation.Event) []EventLike {
	likes := make([]EventLike, 0, len(events))
	for _, event := range events {
		like := EventLike{EventID: event.EventID, OccurredAt: event.OccurredAt, Type: event.Type, Data: event.Data}
		like.Context.Run = event.Context.Run
		like.Context.Project = event.Context.Project
		likes = append(likes, like)
	}
	return likes
}

const extractionSystemPrompt = `You are the Temporality knowledge extractor.
You read one completed agent run and decide which durable facts are worth remembering for future runs in the same project.

Answer with a single JSON object and nothing else. No markdown fences, no commentary.
Schema:
{"candidates": [{"kind": "observation|claim", "proposition": "...", "evidence": ["event id from the trajectory"], "confidence": 0.0-1.0, "contradicts": "optional: existing knowledge id this run disproves"}]}

What qualifies:
- Facts that will help a future run in this project: how it is built, tested, deployed; non-obvious tool or CLI behavior; environment quirks; verified workarounds; documentation that turned out to be stale.
- Negative results are valuable: what does NOT work, which documented instruction is wrong.
- "observation" = directly seen in a tool result; "claim" = conclusion inferred from several observations.

Rules:
- Every candidate MUST cite real event ids from the trajectory as evidence. No evidence, no candidate.
- Do not restate what successful verification and build commands already record automatically (for example "tests pass").
- Do not record the run's reasoning process, only its outcomes.
- Do not restate items of the existing knowledge listed in the context unless this run independently re-verified them; a verified repeat is valuable — state it again as a candidate citing this run's evidence.
- If the run directly disproves an item of the existing knowledge listed in the context, set contradicts to that item's id (shown in brackets) and state the corrected fact as the proposition.
- One self-contained sentence per proposition, in the language of the run.
- At most 5 candidates. An empty list is a valid answer: most runs teach nothing durable.`

// renderExtractionContext builds the compact extraction context (§7): the
// task, the tool trajectory with citable event ids, the final answer, and the
// existing knowledge the model restates on re-verification or contradicts on
// disproof.
func renderExtractionContext(request KnowledgeExtractRequest, trajectory Trajectory, existing []existingKnowledge) string {
	var b strings.Builder
	b.WriteString("Project: " + request.Project + "\nRun: " + request.RunID + "\n\n")
	b.WriteString("Task:\n" + truncateRunes(strings.TrimSpace(request.Prompt), extractionPromptLimit) + "\n")
	b.WriteString("\nTrajectory (event ids are citable as evidence):\n")
	// Completed steps (with outcomes) live on turns; trajectory.Tools only
	// carries the started snapshot.
	steps := 0
	for _, turn := range trajectory.Turns {
		for _, step := range turn.Tools {
			steps++
			if steps > extractionMaxToolSteps {
				b.WriteString(fmt.Sprintf("... %d more tool steps skipped\n", trajectory.Summary.TotalTools-steps+1))
				break
			}
			status := "ok"
			if !step.Success {
				status = "failed"
			}
			line := "- " + step.Tool + " [" + status + "] " + step.EventID
			if step.Arguments != "" {
				line += " args=" + truncateRunes(step.Arguments, extractionArgsLimit)
			}
			if step.Output != "" {
				line += " -> " + strings.ReplaceAll(truncateRunes(step.Output, extractionOutputLimit), "\n", " ")
			}
			b.WriteString(line + "\n")
		}
	}
	if trajectory.Summary.FailedTools > 0 {
		b.WriteString(fmt.Sprintf("Failed tool steps: %d\n", trajectory.Summary.FailedTools))
	}
	if answer := strings.TrimSpace(trajectory.Summary.Answer); answer != "" {
		b.WriteString("\nFinal answer:\n" + truncateRunes(answer, extractionAnswerLimit) + "\n")
	}
	if len(existing) > 0 {
		b.WriteString("\nExisting knowledge (ids in brackets; set contradicts when the run disproves an item, restate an item only when this run re-verified it):\n")
		limit := len(existing)
		if limit > extractionMaxExisting {
			limit = extractionMaxExisting
		}
		for _, item := range existing[:limit] {
			b.WriteString("- [" + item.ID + "] " + truncateRunes(item.Proposition, 200) + "\n")
		}
	}
	b.WriteString("\nAnswer with the JSON object only.")
	return b.String()
}

// extractionCompletion is the strict JSON shape the model must answer with.
type extractionCompletion struct {
	Candidates []extractionCandidate `json:"candidates"`
}

type extractionCandidate struct {
	Kind        string   `json:"kind"`
	Proposition string   `json:"proposition"`
	Evidence    []string `json:"evidence"`
	Confidence  float64  `json:"confidence"`
	Contradicts string   `json:"contradicts"`
}

// parseExtractionCandidates tolerates reasoning blocks, fences and prose
// around the JSON payload (see modeljson.go).
func parseExtractionCandidates(content string) (extractionCompletion, error) {
	payload, _ := extractJSONObject(content)
	var parsed extractionCompletion
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return extractionCompletion{}, fmt.Errorf("model returned invalid JSON: %w (model output: %.200s)", err, strings.TrimSpace(content))
	}
	return parsed, nil
}

// validateExtractionCandidates enforces the extractor contract (§8): supported
// kind, self-contained proposition, and evidence refs that point at real
// events of this run — hallucinated refs are dropped, and a candidate left
// without any valid evidence is dropped entirely. Near-duplicates of existing
// knowledge are counted and skipped (§14); candidates whose id already exists
// as prior project knowledge are marked Existing so the caller strengthens the
// node instead of re-proposing it, while restatements recorded by this run
// itself (remember) stay skipped — they are not independent re-derivation.
func validateExtractionCandidates(raw []extractionCandidate, knownEvents map[string]bool, existing []existingKnowledge, states map[string]string, project string) (accepted []KnowledgeCandidate, duplicates, invalid int) {
	seen := make(map[string]bool, len(raw))
	existingIDs := make(map[string]bool, len(existing))
	for _, item := range existing {
		existingIDs[item.ID] = true
	}
	for _, item := range raw {
		proposition := strings.Join(strings.Fields(strings.TrimSpace(item.Proposition)), " ")
		runes := utf8.RuneCountInString(proposition)
		if proposition == "" || runes < 12 || runes > 600 {
			invalid++
			continue
		}
		kind := item.Kind
		if kind == "" {
			kind = "claim"
		}
		if kind != "observation" && kind != "claim" {
			invalid++
			continue
		}
		evidence := make([]string, 0, len(item.Evidence))
		for _, ref := range item.Evidence {
			ref = strings.TrimSpace(ref)
			if ref != "" && knownEvents[ref] && !containsStr(evidence, ref) {
				evidence = append(evidence, ref)
			}
		}
		if len(evidence) == 0 {
			// Knowledge without verifiable origin is not a result (§8-9).
			invalid++
			continue
		}
		id := extractionKnowledgeID(project, proposition)
		contradicts := strings.TrimSpace(item.Contradicts)
		if contradicts != "" && (contradicts == id || !existingIDs[contradicts] || terminalKnowledgeState(states[contradicts])) {
			// Self-contradiction, a hallucinated id, or a terminal node the
			// projection would refuse to challenge: keep the fact, drop the ref.
			contradicts = ""
		}
		duplicate := false
		for _, item := range existing {
			if contradicts == item.ID {
				// An explicit contradiction carries the corrected fact — it is
				// the replacement, not a restatement.
				continue
			}
			if id == item.ID && !item.FromRun {
				// Exact re-derivation of prior project knowledge is not a
				// duplicate: the Existing flag below routes it to strengthening
				// so re-confirmation reinforces the node (§14).
				continue
			}
			if id == item.ID || propositionSimilar(proposition, item.Proposition) {
				// A this-run restatement (the run already recorded it via
				// remember) or a near-duplicate: skipped, no second node.
				duplicate = true
				break
			}
		}
		if duplicate {
			duplicates++
			continue
		}
		if seen[id] {
			duplicates++
			continue
		}
		if len(accepted) >= extractionMaxCandidates {
			continue
		}
		seen[id] = true
		confidence := item.Confidence
		if confidence < 0 || confidence > 1 {
			confidence = 0
		}
		accepted = append(accepted, KnowledgeCandidate{
			KnowledgeID: id,
			Kind:        kind,
			Proposition: proposition,
			Confidence:  confidence,
			Evidence:    evidence,
			Existing:    states[id] != "" && states[id] != "invalidated" && states[id] != "superseded" && states[id] != "corrected",
			Contradicts: contradicts,
		})
	}
	return accepted, duplicates, invalid
}

// terminalKnowledgeState reports whether a knowledge state admits no further
// lifecycle transitions.
func terminalKnowledgeState(state string) bool {
	return state == "invalidated" || state == "superseded" || state == "corrected"
}

// agingCandidates selects stale proposals for the aging sweep (rule aging.v1):
// knowledge still in "proposed" that has neither been confirmed nor used for
// AgingThreshold. Hint offers do not reset the clock — an item that keeps being
// offered but never used is exactly the noise the sweep retires. Oldest first.
func agingCandidates(knowledge []observation.Knowledge, now time.Time) []AgingCandidate {
	var candidates []AgingCandidate
	for _, item := range knowledge {
		if item.State != "proposed" {
			continue
		}
		anchor := item.CreatedAt
		if item.LastUsedAt != nil && item.LastUsedAt.After(anchor) {
			anchor = *item.LastUsedAt
		}
		age := now.Sub(anchor)
		if age < AgingThreshold {
			continue
		}
		candidates = append(candidates, AgingCandidate{KnowledgeID: item.ID, Proposition: item.Proposition, AgeDays: age.Hours() / 24})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].AgeDays > candidates[j].AgeDays })
	if len(candidates) > agingMaxPerPass {
		candidates = candidates[:agingMaxPerPass]
	}
	return candidates
}

// hintUsageFeedback classifies every hint offered to this run as used or
// ignored by matching each proposition's distinctive terms against what the
// run actually produced (docs/knowledge-evolution.md §2 Hint: applied context
// and influence must stay determinable). Deterministic and cheap, it grounds
// reuse accounting in observed work instead of the bare fact of injection.
func hintUsageFeedback(events []observation.Event, trajectory Trajectory) []HintFeedback {
	var feedback []HintFeedback
	seen := make(map[string]bool)
	corpus := usageCorpusTokens(trajectory)
	for _, event := range events {
		if event.Type != "hint.offered" {
			continue
		}
		hintID := stringField(event.Data, "hint_id")
		knowledgeID := stringField(event.Data, "knowledge_id")
		if hintID == "" || knowledgeID == "" || seen[hintID] {
			continue
		}
		seen[hintID] = true
		proposition := stringField(event.Data, "proposition")
		used, matched := hintReflected(proposition, corpus)
		feedback = append(feedback, HintFeedback{HintID: hintID, KnowledgeID: knowledgeID, Proposition: proposition, Used: used, MatchedBy: matched})
	}
	return feedback
}

// usageCorpusTokens gathers the token set of everything the run produced: the
// final answer plus every tool invocation's name and arguments. Tool output is
// deliberately excluded — echoes of retrieved documents would mark every
// consulted hint as used.
func usageCorpusTokens(trajectory Trajectory) map[string]bool {
	var b strings.Builder
	b.WriteString(trajectory.Summary.Answer)
	for _, turn := range trajectory.Turns {
		for _, step := range turn.Tools {
			b.WriteString(" " + step.Tool + " " + step.Arguments)
		}
	}
	return propositionTokens(b.String())
}

// hintReflected decides whether a hint's proposition is reflected in the run's
// output tokens: two or more distinctive shared terms count as use, and a
// single long rare term (≥8 runes, e.g. a unique service or CLI name) counts
// alone. Matched terms come back prefixed like hint.offered's matched_by so the
// UI renders both the same way.
func hintReflected(proposition string, corpus map[string]bool) (bool, []string) {
	tokens := propositionTokens(proposition)
	matched := make([]string, 0, len(tokens))
	for token := range tokens {
		if corpus[token] {
			matched = append(matched, token)
		}
	}
	sort.Strings(matched)
	if len(matched) > 8 {
		matched = matched[:8]
	}
	used := len(matched) >= 2 || (len(matched) == 1 && len([]rune(matched[0])) >= 8)
	if !used {
		return false, nil
	}
	return true, prefixedTerms(matched)
}

func prefixedTerms(values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = "term:" + value
	}
	return result
}

// propositionSimilar reports whether two propositions are near-duplicates.
// Conservative Jaccard over content tokens with a minimum shared-token floor:
// better to let a near-duplicate through than to silently swallow a new fact.
func propositionSimilar(a, b string) bool {
	ta, tb := propositionTokens(a), propositionTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	shared := 0
	for token := range ta {
		if tb[token] {
			shared++
		}
	}
	if shared < 3 {
		return false
	}
	union := len(ta) + len(tb) - shared
	return float64(shared)/float64(union) >= 0.75
}

// propositionTokens splits text into lowercase content tokens, mirroring the
// journal's term matching so extractor dedup agrees with hint retrieval.
func propositionTokens(value string) map[string]bool {
	const stopwords = "a an and are as at be by for from in into is it of on or the to with this that was were как для или это что при по из на с и к от не но же бы за"
	ignored := make(map[string]bool)
	for _, word := range strings.Fields(stopwords) {
		ignored[word] = true
	}
	result := make(map[string]bool)
	var builder strings.Builder
	flush := func() {
		word := builder.String()
		if len([]rune(word)) > 1 && !ignored[word] {
			result[word] = true
		}
		builder.Reset()
	}
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			builder.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return result
}
