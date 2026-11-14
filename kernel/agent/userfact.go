package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
)

// userFactOriginAgent marks facts saved by the dialog tool (vs the user
// settings screen); mirrors workspace.UserFactOriginAgent without the import.
const userFactOriginAgent = "agent"

// user_remember accumulates explicit durable facts and preferences about the
// CURRENT sender («предпочитает краткие ответы», «ведёт проект Omni»): the
// accumulated half of the per-user picture. The kernel resolves the sender
// from the run's actor — every entrypoint stamps it, so a channel dialog run
// always knows whose fact it is storing.

// factNormalForm is the exact-dedup key: case-folded, punctuation-stripped
// word runs — «Предпочитает краткие ответы!» and «предпочитает краткие
// ответы» are one fact.
var factPunctuation = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func factNormalForm(s string) string {
	return strings.TrimSpace(factPunctuation.ReplaceAllString(strings.ToLower(s), " "))
}

// factValidationPrompt bounds the supervisor's context: the profile is a live
// list (cap 50 facts), but validation only needs a readable slice of it.
const (
	factValidationFactsMax = 40
	factValidationTimeout  = 15 * time.Second
)

// handleUserRemember stores one fact about the sender via the workspace API.
// Two dedup layers keep the profile from collecting restatements:
//  1. exact normalized text match — free, and the store dedups the same way;
//  2. the supervisor (judge) model validates non-obvious candidates against
//     the existing profile: a restatement is dropped with a reason instead of
//     being saved a second time. Best-effort: a model outage never loses the
//     fact — layer 1 and the store still hold the line.
func (a *Activities) handleUserRemember(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if request.ActorID == "" {
		return ToolResult{Content: "error: no sender resolved for this run — user facts are only stored for identified senders"}, nil
	}
	fact, _ := request.Arguments["fact"].(string)
	fact = strings.TrimSpace(fact)
	if fact == "" {
		return ToolResult{Content: "error: fact is required"}, nil
	}
	if len(fact) > 500 {
		fact = fact[:500]
	}
	existing := a.existingUserFacts(ctx, request.ActorID)
	normalized := factNormalForm(fact)
	for _, known := range existing {
		if factNormalForm(known) == normalized {
			return ToolResult{Content: "Already in the sender's profile (exact duplicate): " + known}, nil
		}
	}
	if duplicate, reason := a.userFactIsDuplicate(ctx, existing, fact); duplicate {
		return ToolResult{Content: "Not saved — the supervisor validated it as a duplicate of an existing profile fact (" + reason + "). Facts accumulate in the user's own settings; do not repeat it."}, nil
	}
	body, status, err := a.workspaceDo(ctx, http.MethodPost,
		fmt.Sprintf("%s/v1/workspace/users/%s/facts", a.WorkspaceURL, url.PathEscape(request.ActorID)),
		map[string]any{"fact": fact, "origin": userFactOriginAgent})
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: user fact failed (HTTP %d): %s", status, truncate(body, 300))}, nil
	}
	return ToolResult{Content: "Saved. The fact now rides in the sender's profile for every future dialog."}, nil
}

// existingUserFacts loads the sender's current profile facts (newest first).
// A lookup failure returns nil — validation degrades to the exact-match layer.
func (a *Activities) existingUserFacts(ctx context.Context, userID string) []string {
	body, status, err := a.workspaceDo(ctx, http.MethodGet,
		fmt.Sprintf("%s/v1/workspace/users/%s/facts", a.WorkspaceURL, url.PathEscape(userID)), nil)
	if err != nil || status >= 400 {
		return nil
	}
	var page struct {
		Facts []struct {
			Fact string `json:"fact"`
		} `json:"facts"`
	}
	if json.Unmarshal([]byte(body), &page) != nil {
		return nil
	}
	facts := make([]string, 0, len(page.Facts))
	for _, item := range page.Facts {
		if text := strings.TrimSpace(item.Fact); text != "" {
			facts = append(facts, text)
		}
		if len(facts) >= factValidationFactsMax {
			break
		}
	}
	return facts
}

// userFactIsDuplicate asks the supervisor (judge) model whether the candidate
// adds anything new to the existing profile. Returns (duplicate, reason);
// any failure returns (false, "") — validation is advisory, saving is not.
func (a *Activities) userFactIsDuplicate(ctx context.Context, existing []string, candidate string) (bool, string) {
	if a.JudgeModel == nil || len(existing) == 0 {
		return false, ""
	}
	client, err := a.JudgeModel(ctx)
	if err != nil || client == nil {
		return false, ""
	}
	var profile strings.Builder
	for _, fact := range existing {
		fmt.Fprintf(&profile, "- %s\n", fact)
	}
	ask := fmt.Sprintf("Existing facts about this user:\n%s\nNew candidate fact: %s\n\nDoes the candidate add anything new, or is it a restatement/duplicate of an existing fact (same meaning, different wording counts as duplicate)?",
		profile.String(), candidate)
	callCtx, cancel := context.WithTimeout(ctx, factValidationTimeout)
	defer cancel()
	completion, err := client.Complete(callCtx, []llm.Message{
		{Role: "system", Content: "You validate a personal-facts profile for duplicates. Answer ONLY with a JSON object: {\"duplicate\": true|false, \"reason\": \"short reason\"}. \"duplicate\" means the candidate conveys no information beyond the existing facts."},
		{Role: "user", Content: ask},
	}, nil)
	if err != nil {
		return false, ""
	}
	var verdict struct {
		Duplicate bool   `json:"duplicate"`
		Reason    string `json:"reason"`
	}
	// Providers wrap JSON in prose or code fences — extract the object first.
	text := completion.Content
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			text = text[start : end+1]
		}
	}
	if json.Unmarshal([]byte(text), &verdict) != nil {
		return false, ""
	}
	reason := strings.TrimSpace(verdict.Reason)
	if reason == "" {
		reason = "no new information"
	}
	return verdict.Duplicate, reason
}
