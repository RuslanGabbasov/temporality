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
// for JSON mode up front, repairs the common small-model breakage (raw
// control characters inside strings, trailing commas) without a second call,
// and falls back to a corrective retry when parsing still fails: small models
// occasionally answer conversationally despite instructions (and some
// providers ignore response_format altogether).
func completeParsed[T any](ctx context.Context, model *llm.Client, messages []llm.Message, parse func(string) (T, error)) (T, error) {
	var zero T
	completion, err := model.Complete(ctx, messages, nil, llm.JSONMode())
	if err != nil {
		// Some providers answer json_object-mode requests with an EMPTY
		// completion (sporadically, mostly under long prompts). The retry
		// ladder inside Complete exhausts itself on those; a plain call
		// without response_format usually succeeds. One fallback round.
		if strings.Contains(err.Error(), "empty completion") {
			completion, err = model.Complete(ctx, messages, nil)
			if err != nil {
				return zero, fmt.Errorf("model call failed (json mode and plain): %w", err)
			}
			parsed, parseErr := parse(completion.Content)
			if parseErr == nil {
				return parsed, nil
			}
			if repaired, changed := repairJSONLoose(completion.Content); changed {
				if parsed, err := parse(repaired); err == nil {
					return parsed, nil
				}
			}
		} else {
			return zero, fmt.Errorf("model call failed: %w", err)
		}
	}
	// Reasoning-first models under json_object mode sometimes put the whole
	// answer into reasoning_content and leave content empty — the parse then
	// chokes on an empty string. The reasoning text is the model's own answer
	// draft: use it.
	content := completion.Content
	if strings.TrimSpace(content) == "" && strings.TrimSpace(completion.Reasoning) != "" {
		content = completion.Reasoning
	}
	parsed, parseErr := parse(content)
	if parseErr == nil {
		return parsed, nil
	}
	if repaired, changed := repairJSONLoose(content); changed {
		if parsed, err := parse(repaired); err == nil {
			return parsed, nil
		}
	}
	// Corrective retry with the same reasoning fallback.
	retry := append(append(make([]llm.Message, 0, len(messages)+2), messages...),
		llm.Message{Role: "assistant", Content: completion.Content},
		llm.Message{Role: "user", Content: jsonRepairInstruction},
	)
	completion, err = model.Complete(ctx, retry, nil, llm.JSONMode())
	if err != nil {
		return zero, fmt.Errorf("model call failed: %w", err)
	}
	content = completion.Content
	if strings.TrimSpace(content) == "" && strings.TrimSpace(completion.Reasoning) != "" {
		content = completion.Reasoning
	}
	parsed, parseErr = parse(content)
	if parseErr == nil {
		return parsed, nil
	}
	if repaired, changed := repairJSONLoose(content); changed {
		if parsed, err := parse(repaired); err == nil {
			return parsed, nil
		}
	}
	return zero, parseErr
}

// extractJSONObject returns the most likely JSON object payload in content:
// leading/trailing prose, reasoning blocks and markdown fences are dropped.
// The string-aware balanced scan runs FIRST — a fence inside a string value
// (markdown bodies regularly contain them) must not be mistaken for a wrapper.
// Fence-stripping is the fallback for outputs that wrap the whole object in a
// code block. When no balanced object can be located the trimmed content is
// returned and ok is false, so the caller's json.Unmarshal reports a
// meaningful error.
func extractJSONObject(content string) (payload string, ok bool) {
	text := strings.TrimSpace(stripReasoning(content))
	if text == "" {
		return "", false
	}
	if payload, ok := balancedObject(text); ok {
		return payload, true
	}
	// Raw control characters inside string values (small-model breakage) make
	// candidates fail json.Valid — retry the scan over the repaired text.
	if repaired, changed := repairJSONLoose(text); changed {
		if payload, ok := balancedObject(repaired); ok {
			return payload, true
		}
	}
	// Fenced wrapper: the model wrapped the whole object in a code block.
	if start := strings.Index(text, "```"); start >= 0 {
		if nl := strings.IndexByte(text[start:], '\n'); nl > 0 {
			rest := text[start+nl+1:]
			if end := strings.LastIndex(rest, "```"); end >= 0 {
				fenced := strings.TrimSpace(rest[:end])
				if payload, ok := balancedObject(fenced); ok {
					return payload, true
				}
				if repaired, changed := repairJSONLoose(fenced); changed {
					if payload, ok := balancedObject(repaired); ok {
						return payload, true
					}
				}
			}
		}
	}
	return text, false
}

// balancedObject scans for the first balanced {…} whose content passes
// json.Valid — directly, or after the loose repair (raw newlines inside
// strings, trailing commas). String-aware: braces inside string values do
// not affect depth. The repaired candidate is returned so downstream
// unmarshals see valid JSON.
func balancedObject(text string) (string, bool) {
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
			case c < 0x20:
				// A raw control character inside a string poisons every
				// candidate from here on — no point continuing this scan.
				return "", false
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
					if repaired, changed := repairJSONLoose(candidate); changed && json.Valid([]byte(repaired)) {
						return repaired, true
					}
					start = -1
				}
			}
		}
	}
	return "", false
}

// repairJSONLoose normalizes the two breakage modes of small models against
// nested JSON schemas: raw control characters (newlines, tabs) inside string
// values, and trailing commas before a closing brace/bracket. Valid JSON
// passes through unchanged. Structural text outside strings is untouched.
func repairJSONLoose(s string) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	inString, escaped := false, false
	changed := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				b.WriteByte(c)
				continue
			}
			switch c {
			case '\\':
				escaped = true
				b.WriteByte(c)
			case '"':
				inString = false
				b.WriteByte(c)
			case '\n':
				b.WriteString("\\n")
				changed = true
			case '\r':
				b.WriteString("\\r")
				changed = true
			case '\t':
				b.WriteString("\\t")
				changed = true
			default:
				if c < 0x20 {
					b.WriteString(fmt.Sprintf("\\u%04x", c))
					changed = true
				} else {
					b.WriteByte(c)
				}
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			b.WriteByte(c)
		case ',', ']', '}':
			if c == ',' {
				j := i + 1
				for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
					j++
				}
				if j < len(s) && (s[j] == '}' || s[j] == ']') {
					changed = true
					continue // trailing comma: dropped
				}
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), changed
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
