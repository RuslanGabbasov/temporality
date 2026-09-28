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
	Signals      []Signal `json:"signals"`
	KnowledgeIDs []string `json:"knowledge_ids"`
	ActiveCount  int      `json:"active_count"`
	InvalidCount int      `json:"invalid_count"`
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
	GeneratedAt    time.Time           `json:"generated_at"`
}

// Config controls priming behavior.
type Config struct {
	MaxPatterns int     // max patterns to return (default 7)
	MinScore    float64 // minimum score threshold (default 0.1)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{MaxPatterns: 7, MinScore: 0.1}
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

	patterns := groupPatterns(knowledge)

	for i := range patterns {
		patterns[i].Score = scorePattern(patterns[i], task)
	}

	sort.Slice(patterns, func(i, j int) bool { return patterns[i].Score > patterns[j].Score })
	filtered := patterns[:0]
	for _, pat := range patterns {
		if pat.Score >= p.config.MinScore {
			filtered = append(filtered, pat)
		}
	}
	if len(filtered) > p.config.MaxPatterns {
		filtered = filtered[:p.config.MaxPatterns]
	}

	return PrimingResult{
		Project:        project,
		Task:           task,
		Patterns:       filtered,
		TotalKnowledge: len(knowledge),
		Primed:         len(filtered),
		GeneratedAt:    time.Now().UTC(),
	}, nil
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
func groupPatterns(knowledge []observation.Knowledge) []ExperiencePattern {
	byScope := map[string][]observation.Knowledge{}
	for _, k := range knowledge {
		scope := primaryScope(k)
		byScope[scope] = append(byScope[scope], k)
	}

	var patterns []ExperiencePattern
	for scope, items := range byScope {
		pat := ExperiencePattern{
			ID:    scope,
			Scope: scope,
			Title: scopeTitle(scope, items),
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

func scorePattern(pat ExperiencePattern, task string) float64 {
	score := 0.0

	activeRatio := float64(pat.ActiveCount) / math.Max(1, float64(pat.ActiveCount+pat.InvalidCount))
	score += activeRatio * 2.0

	score += math.Min(float64(len(pat.KnowledgeIDs)), 10) * 0.3

	for _, sig := range pat.Signals {
		score += sig.Weight * 0.1
	}

	taskTokens := tokenize(task)
	scopeTokens := tokenize(pat.Scope + " " + pat.Title)
	overlap := 0
	for t := range taskTokens {
		if scopeTokens[t] {
			overlap++
		}
	}
	if len(taskTokens) > 0 {
		score += float64(overlap) / float64(len(taskTokens)) * 3.0
	}

	return score
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
