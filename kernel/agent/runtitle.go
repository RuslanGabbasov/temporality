// runtitle generates short human-readable titles for agent runs. A run ID
// like "untitled-20260930-062517" tells the user nothing in the Runs list;
// the title is produced by one minimal model call over the task prompt and
// is attached to the run.started event.
package agent

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/temporality-project/temporality/kernel/llm"
)

// titlePromptLimit caps how much of the task prompt is sent to the model:
// a title only needs the opening of the request.
const titlePromptLimit = 800

// titleMaxRunes caps the cleaned title; the UI truncates with ellipsis anyway.
const titleMaxRunes = 64

const titleSystemPrompt = `You name agent runs in a workspace UI.
Read the user's request and reply with a short title for the run.

Rules:
- 2 to 6 words.
- Same language as the request.
- Name the actual task, not a greeting or meta-commentary.
- No quotes, no markdown, no trailing punctuation, no "Title:" prefix.
- Reply with the title only.`

// TitleRequest carries the task prompt for title generation.
type TitleRequest struct {
	Prompt string `json:"prompt"`
}

// GenerateTitle is the activity wrapper. Failures are returned to the
// workflow, which treats them as "no title" and falls back to the run ID.
func (a *Activities) GenerateTitle(ctx context.Context, request TitleRequest) (string, error) {
	return GenerateRunTitle(ctx, a.Model, request.Prompt)
}

// GenerateRunTitle runs the minimal model call. A missing model client is not
// an error: the caller falls back to the run ID.
func GenerateRunTitle(ctx context.Context, model *llm.Client, prompt string) (string, error) {
	if model == nil {
		return "", nil
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", nil
	}
	prompt = truncateRunes(prompt, titlePromptLimit)
	completion, err := model.Complete(ctx, []llm.Message{
		{Role: "system", Content: titleSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return "", err
	}
	return cleanTitle(completion.Content), nil
}

// truncateRunes caps a string by runes so multibyte prompts are never cut
// mid-character.
func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// cleanTitle normalizes a raw model reply into a display title; "" means no
// usable title and the caller falls back to the run ID.
func cleanTitle(raw string) string {
	title := strings.TrimSpace(raw)
	// Reasoning models sometimes prepend a <think>…</think> block.
	if idx := strings.LastIndex(title, "</think>"); idx >= 0 {
		title = title[idx+len("</think>"):]
	}
	// Only the first non-empty line is the answer.
	for _, line := range strings.Split(title, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			title = trimmed
			break
		}
	}
	// Strip an explicit label prefix the model was told to avoid.
	for _, prefix := range []string{"Title:", "title:", "Название:", "название:", "Заголовок:", "заголовок:"} {
		if rest, found := strings.CutPrefix(title, prefix); found {
			title = strings.TrimSpace(rest)
			break
		}
	}
	// Strip symmetric wrapping: quotes and markdown emphasis.
	for _, pair := range [][2]string{{`"`, `"`}, {"'", "'"}, {"«", "»"}, {"“", "”"}, {"**", "**"}, {"`", "`"}} {
		if rest, ok := strings.CutPrefix(title, pair[0]); ok {
			if inner, ok := strings.CutSuffix(rest, pair[1]); ok {
				title = strings.TrimSpace(inner)
			}
		}
	}
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return ""
	}
	if runes := []rune(title); len(runes) > titleMaxRunes {
		title = string(runes[:titleMaxRunes])
	}
	return strings.TrimRight(title, ".,:;!?…—")
}
