// Package priming implements Experience Priming: before a run, the agent
// receives a small set of high-value experience signals instead of raw
// memories. The unit is an ExperiencePattern — a group of related knowledge
// items — not an individual memory.
//
// First implementation is maximally simple and predominantly deterministic;
// LLM is used only for compact cue formation (future).
package priming

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// ExperiencePattern is a group of related knowledge items that share a
// semantic scope or topic.
type ExperiencePattern struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	Scope        string   `json:"scope"`
	Score        float64  `json:"score"`
	Cues         []string `json:"cues,omitempty"`
	Signals      []Signal `json:"signals,omitempty"`
	KnowledgeIDs []string `json:"knowledge_ids"`
	ActiveCount  int      `json:"active_count"`
	InvalidCount int      `json:"invalid_count"`

	// items holds the source knowledge items behind the pattern; it is
	// unexported scoring state and does not affect the JSON shape.
	items []observation.Knowledge
}

// Signal is a single actionable cue derived from experience.
type Signal struct {
	Text     string  `json:"text"`
	Kind     string  `json:"kind"`
	Weight   float64 `json:"weight"`
	SourceID string  `json:"source_id,omitempty"`
}

// PrimingResult is the output of Experience Priming.
type PrimingResult struct {
	Project        string              `json:"project"`
	Task           string              `json:"task"`
	Patterns       []ExperiencePattern `json:"patterns"`
	TotalKnowledge int                 `json:"total_knowledge"`
	Primed         int                 `json:"primed"`
	HiddenPatterns int                 `json:"hidden_patterns"`
	TokensEstimate int                 `json:"tokens_estimate"`
	GeneratedAt    time.Time           `json:"generated_at"`
}

// Config controls priming behavior.
type Config struct {
	MaxPatterns    int     // max patterns to return (default 7)
	MinScore       float64 // minimum score threshold (default 0.1)
	MaxTokens      int     // token budget for the rendered patterns (default 600)
	CuesPerPattern int     // cue lines rendered per pattern (default 3)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{MaxPatterns: 7, MinScore: 0.1, MaxTokens: 600, CuesPerPattern: 3}
}

// Primer generates experience priming.
type Primer struct {
	config     Config
	http       *http.Client
	journalURL string
	token      string
}

// NewPrimer creates a primer.
func NewPrimer(config Config, journalURL, token string) *Primer {
	return &Primer{
		config:     config,
		http:       &http.Client{Timeout: 15 * time.Second},
		journalURL: journalURL,
		token:      token,
	}
}

// Prime retrieves knowledge, groups into patterns, scores, and returns
// the most relevant experience cues for the given task context.
func (p *Primer) Prime(ctx context.Context, project, task string) (PrimingResult, error) {
	knowledge, err := p.retrieveKnowledge(ctx, project)
	if err != nil {
		return PrimingResult{}, fmt.Errorf("retrieve knowledge: %w", err)
	}

	ranked := rankPatterns(knowledge, task, p.config)
	patterns, hidden, tokens := renderBudget(ranked, p.config)

	return PrimingResult{
		Project:        project,
		Task:           task,
		Patterns:       patterns,
		TotalKnowledge: len(knowledge),
		Primed:         len(patterns),
		HiddenPatterns: hidden,
		TokensEstimate: tokens,
		GeneratedAt:    time.Now().UTC(),
	}, nil
}

// rankPatterns runs the full ranking pipeline: grouping, scoring,
// deterministic ordering, threshold filtering and truncation.
func rankPatterns(knowledge []observation.Knowledge, task string, config Config) []ExperiencePattern {
	patterns := groupPatterns(knowledge)
	fallback := len(tokenize(task)) == 0

	for i := range patterns {
		patterns[i].Score = scorePattern(patterns[i], task)
	}
	sortPatterns(patterns)

	// MinScore applies only to task-aware scoring; the fallback ranks by
	// validity and reuse and must not be filtered away.
	if !fallback {
		filtered := patterns[:0]
		for _, pat := range patterns {
			if pat.Score >= config.MinScore {
				filtered = append(filtered, pat)
			}
		}
		patterns = filtered
	}
	if len(patterns) > config.MaxPatterns {
		patterns = patterns[:config.MaxPatterns]
	}
	return patterns
}

// sortPatterns orders patterns by score descending; ties are broken by
// pattern ID in byte order, so equal scores keep a deterministic order.
func sortPatterns(patterns []ExperiencePattern) {
	sort.Slice(patterns, func(i, j int) bool {
		if patterns[i].Score != patterns[j].Score {
			return patterns[i].Score > patterns[j].Score
		}
		return patterns[i].ID < patterns[j].ID
	})
}

// estimateTokens approximates the LLM token count of a string as
// ceil(runes/4); prompts average roughly four characters per token.
func estimateTokens(s string) int {
	return int(math.Ceil(float64(len([]rune(s))) / 4))
}

// patternBudget is the token cost of rendering a pattern: its title,
// every cue line and its ID.
func patternBudget(title string, cues []string, id string) int {
	total := estimateTokens(title) + estimateTokens(id)
	for _, cue := range cues {
		total += estimateTokens(cue)
	}
	return total
}

// patternCues renders the compact cue lines of a pattern: the top
// CuesPerPattern items by validity weight, then reuse count, then ID.
func patternCues(pat ExperiencePattern, config Config) []string {
	items := make([]observation.Knowledge, len(pat.items))
	copy(items, pat.items)
	sort.Slice(items, func(i, j int) bool {
		wi, wj := validityWeight(items[i].State), validityWeight(items[j].State)
		if wi != wj {
			return wi > wj
		}
		if items[i].ReuseCount != items[j].ReuseCount {
			return items[i].ReuseCount > items[j].ReuseCount
		}
		return items[i].ID < items[j].ID
	})
	if len(items) > config.CuesPerPattern {
		items = items[:config.CuesPerPattern]
	}
	cues := make([]string, 0, len(items))
	for _, k := range items {
		cues = append(cues, formatCue(k))
	}
	return cues
}

// formatCue renders one knowledge item as a single compact cue line:
// "<knowledge_id>: <proposition> — <state>, reused <N>"; the reuse
// suffix is omitted when the item was never reused.
func formatCue(k observation.Knowledge) string {
	state := k.State
	if state == "" {
		state = "unknown"
	}
	cue := fmt.Sprintf("%s: %s — %s", k.ID, truncate(k.Proposition, 80), state)
	if k.ReuseCount > 0 {
		cue += fmt.Sprintf(", reused %d", k.ReuseCount)
	}
	return cue
}

// renderBudget selects patterns in rank order until the token budget is
// exhausted, rendering each as a compact title+cues form. The top-ranked
// pattern is always rendered with at least one cue, even when it alone
// exceeds the budget; every pattern left out is counted as hidden. The
// verbose per-item Signals are dropped here: the compact cues replace
// them in the external form.
func renderBudget(ranked []ExperiencePattern, config Config) ([]ExperiencePattern, int, int) {
	total := 0
	kept := make([]ExperiencePattern, 0, len(ranked))
	for i := range ranked {
		title := truncate(ranked[i].Title, 80)
		cues := patternCues(ranked[i], config)
		cost := patternBudget(title, cues, ranked[i].ID)
		if i > 0 && total+cost > config.MaxTokens {
			break
		}
		out := ranked[i]
		out.Title = title
		out.Cues = cues
		out.Signals = nil
		kept = append(kept, out)
		total += cost
	}
	return kept, len(ranked) - len(kept), total
}

func (p *Primer) retrieveKnowledge(ctx context.Context, project string) ([]observation.Knowledge, error) {
	url := fmt.Sprintf("%s/v1/observations/knowledge?project=%s", p.journalURL, project)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("journal returned %d", resp.StatusCode)
	}
	var page struct {
		Knowledge []observation.Knowledge `json:"knowledge"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, err
	}
	return page.Knowledge, nil
}

// groupPatterns clusters knowledge into experience patterns by scope.
// Scopes are assembled in sorted order so the resulting slice — and with
// it any downstream ordering — is deterministic for identical input.
func groupPatterns(knowledge []observation.Knowledge) []ExperiencePattern {
	byScope := map[string][]observation.Knowledge{}
	for _, k := range knowledge {
		scope := primaryScope(k)
		byScope[scope] = append(byScope[scope], k)
	}

	scopes := make([]string, 0, len(byScope))
	for scope := range byScope {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)

	var patterns []ExperiencePattern
	for _, scope := range scopes {
		items := byScope[scope]
		pat := ExperiencePattern{
			ID:    scope,
			Scope: scope,
			Title: scopeTitle(scope, items),
			items: items,
		}
		for _, k := range items {
			pat.KnowledgeIDs = append(pat.KnowledgeIDs, k.ID)
			if k.State == "invalidated" || k.State == "superseded" || k.State == "corrected" {
				pat.InvalidCount++
			} else {
				pat.ActiveCount++
			}
			pat.Signals = append(pat.Signals, signalsFromKnowledge(k)...)
		}
		patterns = append(patterns, pat)
	}
	return patterns
}

func primaryScope(k observation.Knowledge) string {
	if len(k.Topics) > 0 {
		return k.Topics[0]
	}
	if len(k.Entities) > 0 {
		return k.Entities[0]
	}
	words := strings.Fields(k.Proposition)
	if len(words) > 2 {
		return strings.ToLower(words[0] + "-" + words[1])
	}
	return "misc"
}

func scopeTitle(scope string, items []observation.Knowledge) string {
	if len(items) == 1 {
		return items[0].Proposition
	}
	return fmt.Sprintf("%s (%d items)", scope, len(items))
}

func signalsFromKnowledge(k observation.Knowledge) []Signal {
	var signals []Signal
	switch k.State {
	case "confirmed":
		signals = append(signals, Signal{
			Text:   fmt.Sprintf("%s — confirmed (%d reuses)", truncate(k.Proposition, 60), k.ReuseCount),
			Kind:   "success",
			Weight: 1.0 + float64(k.ReuseCount)*0.1,
		})
	case "proposed":
		signals = append(signals, Signal{
			Text:   fmt.Sprintf("%s — proposed, not yet confirmed", truncate(k.Proposition, 60)),
			Kind:   "convention",
			Weight: 0.3,
		})
	case "invalidated", "superseded":
		signals = append(signals, Signal{
			Text:   fmt.Sprintf("%s — %s", truncate(k.Proposition, 60), k.State),
			Kind:   "failure",
			Weight: 0.5,
		})
	case "corrected":
		signals = append(signals, Signal{
			Text:   fmt.Sprintf("%s — corrected", truncate(k.Proposition, 60)),
			Kind:   "conflict",
			Weight: 0.7,
		})
	case "challenged":
		signals = append(signals, Signal{
			Text:   fmt.Sprintf("%s — challenged", truncate(k.Proposition, 60)),
			Kind:   "conflict",
			Weight: 0.6,
		})
	}
	if k.AtRisk {
		signals = append(signals, Signal{
			Text:   fmt.Sprintf("%s — at risk", truncate(k.Proposition, 60)),
			Kind:   "stale",
			Weight: 0.4,
		})
	}
	return signals
}

// validityWeight maps a knowledge state to how much the state is worth
// when pooling the experience of a pattern. Unknown states are treated
// as unverified guesses.
func validityWeight(state string) float64 {
	switch state {
	case "confirmed":
		return 1.0
	case "proposed":
		return 0.7
	case "challenged", "corrected":
		return 0.5
	case "invalidated", "superseded":
		return 0.2
	default:
		return 0.5
	}
}

// itemTokens is the token set of everything an item asserts: its
// proposition plus its topics and entities.
func itemTokens(k observation.Knowledge) map[string]bool {
	return tokenize(k.Proposition + " " + strings.Join(k.Topics, " ") + " " + strings.Join(k.Entities, " "))
}

// overlap is the fraction of task tokens found in the given token set:
// 0 when the task carries no tokens.
func overlap(taskTokens map[string]bool, tokens map[string]bool) float64 {
	if len(taskTokens) == 0 {
		return 0
	}
	matched := 0
	for t := range taskTokens {
		if tokens[t] {
			matched++
		}
	}
	return float64(matched) / float64(len(taskTokens))
}

// scorePattern scores a pattern against the task. With task tokens
// present it combines per-item and scope relevance (each normalized to
// [0,1]) with pattern validity. Without task tokens (empty task) it
// falls back to ranking by validity and accumulated reuse.
func scorePattern(pat ExperiencePattern, task string) float64 {
	taskTokens := tokenize(task)

	validity := 0.0
	reuse := 0
	for _, k := range pat.items {
		validity += validityWeight(k.State)
		reuse += k.ReuseCount
	}
	if n := len(pat.items); n > 0 {
		validity /= float64(n)
	}

	if len(taskTokens) == 0 {
		// Fallback: no task context, rank by validity and reuse.
		return validity + 0.05*math.Min(float64(reuse), 10)
	}

	overlaps := make([]float64, len(pat.items))
	weights := make([]float64, len(pat.items))
	maxOverlap := 0.0
	for i, k := range pat.items {
		overlaps[i] = overlap(taskTokens, itemTokens(k))
		weights[i] = validityWeight(k.State)
		if overlaps[i] > maxOverlap {
			maxOverlap = overlaps[i]
		}
	}
	weightedSum, weightTotal := 0.0, 0.0
	for i := range overlaps {
		weightedSum += overlaps[i] * weights[i]
		weightTotal += weights[i]
	}
	weightedMean := 0.0
	if weightTotal > 0 {
		weightedMean = weightedSum / weightTotal
	}

	relevance := 0.7*maxOverlap + 0.3*weightedMean + 0.2*overlap(taskTokens, tokenize(pat.Scope))
	return relevance * (0.3 + validity)
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "but": true,
	"not": true, "you": true, "all": true, "can": true, "had": true,
	"her": true, "was": true, "one": true, "our": true, "out": true,
	"has": true, "how": true, "its": true, "may": true, "new": true,
	"now": true, "old": true, "see": true, "way": true, "who": true,
	"did": true, "get": true, "got": true, "let": true, "say": true,
	"she": true, "too": true, "use": true, "this": true, "that": true,
	"with": true, "from": true, "into": true, "each": true, "just": true,
	"been": true, "have": true, "will": true, "your": true, "than": true,
	"then": true, "them": true, "what": true, "when": true, "which": true,
}

func tokenize(s string) map[string]bool {
	tokens := map[string]bool{}
	for _, word := range strings.Fields(strings.ToLower(s)) {
		cleaned := strings.Trim(word, ".,;:!?\"'()[]{}")
		if len(cleaned) > 3 && !stopWords[cleaned] {
			tokens[cleaned] = true
		}
	}
	return tokens
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// Handler serves the priming API.
func (p *Primer) Handler(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	task := r.URL.Query().Get("task")
	if project == "" {
		http.Error(w, `{"error":"project is required"}`, 400)
		return
	}
	result, err := p.Prime(r.Context(), project, task)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
