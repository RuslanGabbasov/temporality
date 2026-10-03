// modeljson extracts a JSON object from raw model output. Reasoning models
// inline <think>…</think> blocks into content, chatty models surround the
// payload with prose or markdown fences — none of that is worth failing a
// builder wizard over when the object itself is intact.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
)

// jsonRepairInstruction is the corrective user turn appended when the model
// answered in prose instead of JSON: showing the failed reply back reliably
// restores the format on the second attempt.
const jsonRepairInstruction = `Your previous reply was not the requested JSON object. Respond again with the single JSON object matching the schema — JSON only, no prose, no markdown fences.`

// completeParsed runs one model call and parses the reply as JSON. It asks
// for JSON mode up front and falls back to one corrective retry when parsing
// still fails: small models occasionally answer conversationally despite
// instructions (and some providers ignore response_format altogether).
func completeParsed[T any](ctx context.Context, model *llm.Client, messages []llm.Message, parse func(string) (T, error)) (T, error) {
	var zero T
	completion, err := model.Complete(ctx, messages, nil, llm.JSONMode())
	if err != nil {
		return zero, fmt.Errorf("model call failed: %w", err)
	}
	parsed, parseErr := parse(completion.Content)
	if parseErr == nil {
		return parsed, nil
	}
	retry := append(append(make([]llm.Message, 0, len(messages)+2), messages...),
		llm.Message{Role: "assistant", Content: completion.Content},
		llm.Message{Role: "user", Content: jsonRepairInstruction},
	)
	completion, err = model.Complete(ctx, retry, nil, llm.JSONMode())
	if err != nil {
		return zero, fmt.Errorf("model call failed: %w", err)
	}
	return parse(completion.Content)
}

// extractJSONObject returns the most likely JSON object payload in content:
// leading/trailing prose, markdown fences and reasoning blocks are dropped.
// When no balanced object can be located the trimmed content is returned and
// ok is false, so the caller's json.Unmarshal reports a meaningful error.
func extractJSONObject(content string) (payload string, ok bool) {
	text := strings.TrimSpace(stripReasoning(content))
	if start := strings.Index(text, "```"); start >= 0 {
		if nl := strings.IndexByte(text[start:], '\n'); nl > 0 {
			rest := text[start+nl+1:]
			if end := strings.LastIndex(rest, "```"); end >= 0 {
				text = strings.TrimSpace(rest[:end])
			}
		}
	}
	if text == "" {
		return "", false
	}
	if strings.HasPrefix(text, "{") && json.Valid([]byte(text)) {
		return text, true
	}
	// Scan for the first balanced, valid object wherever it sits in the text.
	depth, start := 0, -1
	inString, escaped := false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					candidate := text[start : i+1]
					if json.Valid([]byte(candidate)) {
						return candidate, true
					}
					start = -1
				}
			}
		}
	}
	return text, false
}

// stripReasoning removes complete <think>…</think> blocks (some providers do
// not separate reasoning into its own field). An unclosed block is kept: its
// text may still contain the answer object, and the balanced scan above will
// salvage it if it does.
func stripReasoning(content string) string {
	lower := strings.ToLower(content)
	for {
		start := strings.Index(lower, "<think>")
		if start < 0 {
			return content
		}
		end := strings.Index(lower[start:], "</think>")
		if end < 0 {
			return content
		}
		end += start
		content = content[:start] + content[end+len("</think>"):]
		lower = strings.ToLower(content)
	}
}
