package presentation

import (
	"strings"
)

// Declarative presentations in the final answer (docs/presentation-engine.md
// §10.1: simple components must not require a separate tool call). An agent
// may embed a fenced block in its answer:
//
//	```presentation
//	{ ...Presentation IR... }
//	```
//
// The runtime validates and publishes every such block as a presentation
// attached to the run, while the answer text itself stays untouched — the
// fenced source remains the honest text fallback for channels without
// rendering (§2.1: text messages keep working unchanged).

// FenceLanguage is the fenced block language that carries Presentation IR.
const FenceLanguage = "presentation"

// maxAnswerPresentations bounds one answer: an agent flooding fifty diagrams
// into one reply is a bug, and each block becomes a stored document.
const maxAnswerPresentations = 10

// ExtractFencedPresentations finds ```presentation blocks in an answer and
// returns their raw JSON payloads in document order. Unterminated fences are
// ignored (the answer is mid-write or malformed — either way, not a
// presentation). Fences with a trailing info string ("presentation chart")
// are accepted.
func ExtractFencedPresentations(answer string) []string {
	var out []string
	lines := strings.Split(answer, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "```") {
			continue
		}
		info := strings.TrimSpace(strings.TrimPrefix(lines[i], "```"))
		if !fenceLanguageMatches(info) {
			continue
		}
		start := i + 1
		end := -1
		for j := start; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "```") {
				end = j
				break
			}
		}
		if end < 0 {
			break // unterminated fence: no presentation, no scan past it
		}
		payload := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
		if payload != "" {
			out = append(out, payload)
			if len(out) >= maxAnswerPresentations {
				return out
			}
		}
		i = end
	}
	return out
}

// fenceLanguageMatches accepts exactly the presentation language token.
func fenceLanguageMatches(info string) bool {
	if info == FenceLanguage {
		return true
	}
	// "presentation ru" etc: first token decides.
	token, _, _ := strings.Cut(info, " ")
	return token == FenceLanguage
}

// StripFences removes presentation fenced blocks from an answer text. Text
// channels receive the stripped answer (the raw JSON fence is an internal
// transport convention — a channel user must not read JSON); the visuals
// reach them as adapted static renders instead (docs/presentation-engine.md
// §9.3 strategy table). The fence is replaced by a short marker line so the
// surrounding prose keeps its flow.
func StripFences(answer string) string {
	lines := strings.Split(answer, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	for _, line := range lines {
		if !skipping && strings.HasPrefix(line, "```") {
			info := strings.TrimSpace(strings.TrimPrefix(line, "```"))
			if fenceLanguageMatches(info) {
				skipping = true
				out = append(out, "[structured answer: rendered in the Temporality UI]")
			}
			continue
		}
		if skipping {
			if strings.HasPrefix(line, "```") {
				skipping = false
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
