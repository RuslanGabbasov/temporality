package observation

import (
	"strings"
)

// HintsAlgorithmVersion identifies the hint selection algorithm (matching,
// admission, ranking, diversification) so events and runs can be compared
// across versions. v2 adds exact/Jaccard deduplication and per-topic capping.
const HintsAlgorithmVersion = "lexical-entity-topic.v2"

// RenderOptions caps the compact prior-knowledge block injected into a prompt.
type RenderOptions struct {
	MaxTokens    int // token budget for the whole block, header included
	MaxCueTokens int // token cap for a single cue line
	MaxHints     int // maximum number of cue lines
}

// DefaultRenderOptions holds the production caps agreed in
// docs/plan-priming-relevance.md §8: ~600 tokens total, ~120 per cue, 8 hints.
func DefaultRenderOptions() RenderOptions {
	return RenderOptions{MaxTokens: 600, MaxCueTokens: 120, MaxHints: 8}
}

// RenderResult reports the rendered block and how its budget was spent.
type RenderResult struct {
	Block   string
	Tokens  int
	Kept    int
	Dropped int
}

// hintBlockHeader teaches the model the cue contract in one line: a rule with
// its status, why it matched, and the knowledge_id handle for follow-up.
const hintBlockHeader = "Temporality prior knowledge — each line: rule [state] (why matched) knowledge_id; use the knowledge_lookup tool with a knowledge_id for grounds and history:"

// minCueTokens is the smallest cue worth its budget: below this a truncated
// rule carries no usable signal, so the hint is dropped instead.
const minCueTokens = 10

// minRuleRunes keeps a maximally squeezed cue from degenerating into noise.
const minRuleRunes = 16

// RenderHintBlock renders ranked hints as one compact deterministic block.
// Cues are taken in rank order while the budget lasts; a cue never exceeds the
// per-cue cap and the block never exceeds opts.MaxTokens. With no hints — or
// none that fit — the block is empty: an honest zero instead of noise.
func RenderHintBlock(hints []Hint, opts RenderOptions) RenderResult {
	defaults := DefaultRenderOptions()
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = defaults.MaxTokens
	}
	if opts.MaxCueTokens <= 0 {
		opts.MaxCueTokens = defaults.MaxCueTokens
	}
	if opts.MaxHints <= 0 {
		opts.MaxHints = defaults.MaxHints
	}
	capped := hints
	if len(capped) > opts.MaxHints {
		capped = capped[:opts.MaxHints]
	}
	dropped := len(hints) - len(capped)
	lines := make([]string, 0, len(capped))
	remaining := opts.MaxTokens - cueTokens(hintBlockHeader)
	for i, hint := range capped {
		cap := opts.MaxCueTokens
		if cap > remaining {
			cap = remaining
		}
		if cap < minCueTokens {
			dropped += len(capped) - i
			break
		}
		line := renderCue(hint, cap)
		if line == "" {
			dropped++
			continue
		}
		lines = append(lines, line)
		remaining -= cueTokens(line)
	}
	if len(lines) == 0 {
		return RenderResult{Dropped: dropped}
	}
	block := hintBlockHeader + "\n" + strings.Join(lines, "\n")
	return RenderResult{Block: block, Tokens: cueTokens(block), Kept: len(lines), Dropped: dropped}
}

// renderCue renders one hint as a single cue line within capTokens:
// "- rule [state] (match: term:x) knowledge_id=id; caution: …". It returns ""
// when even a maximally truncated rule does not fit the cap.
func renderCue(hint Hint, capTokens int) string {
	suffix := " [" + hint.State + "]"
	if len(hint.MatchedBy) > 0 {
		suffix += " (match: " + strings.Join(boundedStrings(hint.MatchedBy, 3), ", ") + ")"
	}
	suffix += " knowledge_id=" + hint.KnowledgeID
	if hint.Caution != "" {
		suffix += "; caution: " + truncateCueRunes(hint.Caution, 80)
	}
	allowedRunes := capTokens * 4                 // ceil(runes/4) tokens ⇒ runes ≤ 4·cap fits
	overheadRunes := len([]rune("- "+suffix)) + 1 // +1 for a possible ellipsis
	ruleRunes := allowedRunes - overheadRunes
	if ruleRunes < minRuleRunes {
		return ""
	}
	return "- " + truncateCueRunes(hint.Proposition, ruleRunes) + suffix
}

// cueTokens approximates the LLM token count as ceil(runes/4).
func cueTokens(s string) int {
	return (len([]rune(s)) + 3) / 4
}

// truncateCueRunes cuts a string to at most n runes, marking a cut with "…".
func truncateCueRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(runes[:n-1]) + "…"
}
