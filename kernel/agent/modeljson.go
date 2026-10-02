// modeljson extracts a JSON object from raw model output. Reasoning models
// inline <think>…</think> blocks into content, chatty models surround the
// payload with prose or markdown fences — none of that is worth failing a
// builder wizard over when the object itself is intact.
package agent

import (
	"encoding/json"
	"strings"
)

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
