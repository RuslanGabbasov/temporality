package memory

import (
	"fmt"
	"sort"
	"strings"
)

// Weights are explicit and logged so every injected hint is explainable.
// See pivot doc §14: ranking must be explainable, deterministic, logged and
// easily replaceable.
type RankWeights struct {
	Scope      float64
	Similarity float64
	Confidence float64
	Temporal   float64
}

// DefaultWeights favour scope first (§3: semantic scope is the first filter).
var DefaultWeights = RankWeights{Scope: 0.40, Similarity: 0.25, Confidence: 0.15, Temporal: 0.20}

// Threshold is the minimum score for injection (§15: keep hints few and
// relevant; context pollution is risk 4).
const Threshold = 0.35

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "for": true, "is": true, "are": true, "on": true,
	"with": true, "by": true, "its": true, "it": true, "this": true, "that": true,
	"from": true, "using": true, "use": true, "api": true, "report": true,
}

// Tokenize lowercases, splits on non-alphanumerics and drops stopwords.
func Tokenize(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 'а' && r <= 'я' || r == 'ё')
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) <= 1 || stopwords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

func tokenSet(tokens []string) map[string]bool {
	set := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		set[t] = true
	}
	return set
}

// Jaccard is the deterministic similarity measure for v1: token-set overlap.
func Jaccard(a, b []string) float64 {
	sa, sb := tokenSet(a), tokenSet(b)
	if len(sa) == 0 && len(sb) == 0 {
		return 0
	}
	intersection := 0
	for t := range sa {
		if sb[t] {
			intersection++
		}
	}
	union := len(sa) + len(sb) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// RankRequest describes the current context a recall happens in.
type RankRequest struct {
	TaskText     string
	Service      string // "" when unknown
	Resource     string // intended target for pre-action activation; "" at task start
	Environment  string // e.g. "prod", "staging"
	Version      string // current API version of the target service ("" when unversioned)
	SessionIndex int    // 1-based, used for session-recency
}

// Scored is one ranked candidate.
type Scored struct {
	Asset *Asset
	Score float64
	Why   map[string]any
}

// WhySelected builds the explainability payload (§14).
func WhySelected(scopeMatch, similarity, temporal, recency, envMatch, confidence float64, status string, versionMatch bool) map[string]any {
	return map[string]any{
		"scope_match":         round(scopeMatch),
		"semantic_similarity": round(similarity),
		"temporal":            round(temporal),
		"recency_sessions":    round(recency),
		"environment_match":   round(envMatch),
		"version_match":       versionMatch,
		"confidence":          round(confidence),
		"status":              status,
	}
}

func round(v float64) float64 {
	return float64(int(v*1000)) / 1000
}

// Rank scores every candidate. semanticOnly=true implements arm B (similarity
// ranking without temporal/confidence weighting); false implements the full
// pipeline of arms C/D.
func Rank(candidates []*Asset, req RankRequest, weights RankWeights, semanticOnly bool) []Scored {
	taskTokens := Tokenize(req.TaskText)
	scored := make([]Scored, 0, len(candidates))
	for _, asset := range candidates {
		if asset.Status == StatusArchived {
			continue
		}
		if req.Resource != "" && asset.Recommendation.Resource != "" && asset.Recommendation.Resource != req.Resource {
			// Pre-action recall: the hint must concern the intended call.
			continue
		}
		scopeMatch := asset.ScopeMatch(req.Service)
		similarity := Jaccard(taskTokens, Tokenize(asset.Text()))
		envMatch := 1.0
		if req.Environment != "" && asset.Environment != "" && req.Environment != asset.Environment {
			envMatch = 0.25
			if !semanticOnly {
				// Full pipeline (§6): environments partition the world; a memory
				// confirmed in another environment is not a candidate at all, no
				// matter how similar it looks. Semantic-only retrieval (arm B) keeps
				// it — that is precisely the conflict failure mode it exists to show.
				continue
			}
		}
		versionMatch := true
		if req.Version != "" && asset.VersionContext != "" && req.Version != asset.VersionContext {
			versionMatch = false
		}
		recency := 1.0 / float64(1+max(0, req.SessionIndex-asset.LastConfirmedSession))
		temporal := 0.6*recency + 0.4*envMatch
		statusFactor := 1.0
		if asset.Status == StatusStale {
			statusFactor = 0.4
		}
		if !versionMatch {
			// Same environment, different API version: the memory was confirmed
			// against another version of the world, so it is a weaker candidate
			// but not foreign (unlike an environment mismatch).
			statusFactor *= 0.5
		}
		score := 0.0
		if semanticOnly {
			// Arm B: semantic retrieval per §3 — scope match IS part of semantics.
			// No temporal, confidence, environment, version or status weighting.
			score = 0.6*scopeMatch + 0.4*similarity
		} else {
			score = (weights.Scope*scopeMatch + weights.Similarity*similarity +
				weights.Confidence*asset.Confidence + weights.Temporal*temporal) * statusFactor
		}
		scored = append(scored, Scored{
			Asset: asset,
			Score: round(score),
			Why:   WhySelected(scopeMatch, similarity, temporal, recency, envMatch, asset.Confidence, asset.Status, versionMatch),
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Asset.DedupKey < scored[j].Asset.DedupKey
	})
	return scored
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// FormatScore renders a one-line why-selected summary for logs.
func FormatScore(s Scored) string {
	return fmt.Sprintf("score=%.3f %v", s.Score, s.Why)
}
