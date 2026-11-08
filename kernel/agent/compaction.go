// compaction keeps long runs alive and affordable. When the conversation
// outgrows compactionTriggerBytes, the elided middle is replaced with a
// structured state card produced by one auxiliary model call (the cheap
// default model, the same path run titles use) and the recent turn groups
// stay verbatim at the end (docs/context-compaction.md). The card rides every
// later request, so the run view journals it as context.compacted — the user
// can always see what the agent still remembers.
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
)

const (
	// compactionTriggerBytes is the serialized request size that starts a
	// compaction. ~1.2 MB is 400-500k prompt tokens at the measured 2.4-3.0
	// wire bytes per token: far above the normal run (p90 ≈ 150k tokens),
	// far below the 2 MiB scheduler ceiling the payload guard enforces.
	compactionTriggerBytes = 1200 << 10
	// compactionCardMaxBytes bounds the state card. It rides every later
	// request, so it must stay small enough to matter less than a tool
	// output yet dense enough to hold every identifier the work produced.
	compactionCardMaxBytes = 8 << 10
	// compactionDigestMessageHead/Tail bound one message inside the digest
	// sent for summarization: head and tail survive (the verdict usually
	// closes a tool output), the middle is elided. Whole messages, not the
	// whole conversation — the summarizer needs the facts, not every byte.
	compactionDigestMessageHead = 3584
	compactionDigestMessageTail = 512
	// compactionTailBytes and the group bounds limit the verbatim tail kept
	// after the card. Only whole groups are kept: a tool result never splits
	// from its tool_calls message (the API contiguity contract).
	compactionTailBytes     = 256 << 10
	compactionKeepGroupsMin = 2
	compactionKeepGroupsMax = 8
)

// CompactRequest carries what the summarizer needs: the task prompt (the
// goal anchor, verbatim) and the digest of the conversation so far.
type CompactRequest struct {
	Prompt string `json:"prompt"`
	Digest string `json:"digest"`
}

// CompactResult is the state card that replaces the elided middle.
type CompactResult struct {
	Card string `json:"card"`
}

const compactionSystemPrompt = `You maintain the working memory of a long-running agent. The raw transcript of the work is about to be elided; you produce the state card that replaces it. The card is the agent's only record of everything before the recent turns — it must be complete enough to continue the task without the transcript.

Keep EXACTLY these sections, in this order, in the language of the task:

## Task
What the user asked for, one or two sentences.

## Decisions
Every decision made so far, one line each, with its rationale.

## Files and artifacts
Every file path, artifact id, run id, branch, skill version or other identifier the work touched or produced — verbatim, each with a short note.

## Verified
What was already checked or executed and with what result (commands, tests, reviews, measurements) — facts, not intentions.

## Open questions
Blockers, uncertainties, anything the user must decide. Write "-" if none.

## Next step
What remains to be done to finish the task.

Rules:
- Identifiers (paths, ids, hashes, numbers, names) are always verbatim — never paraphrase them.
- Facts only: no greetings, no filler, no promises.
- Prefer one dense line over three thin ones.
- If the goal changed since the start (a follow-up arrived), the Task section reflects the CURRENT goal.
- Reply with the card only: no preamble, no code fences.`

// compactionCardPrefix frames the card inside the rebuilt conversation so the
// model treats it as the record of elided work, not as a new user request.
const compactionCardPrefix = "The earlier transcript of this conversation was compacted into the state card below (the raw messages were elided). Treat the card as the authoritative record of everything before the recent turns; the task continues from its \"Next step\" section.\n\n"

// CompactContext is the activity: one model call over the digest produces
// the state card. Failures are the caller's to handle — the run continues
// uncompacted and the payload guard stays the backstop.
func (a *Activities) CompactContext(ctx context.Context, request CompactRequest) (CompactResult, error) {
	if a.Model == nil {
		return CompactResult{}, fmt.Errorf("context compaction needs the model client")
	}
	completion, err := a.Model.Complete(ctx, []llm.Message{
		{Role: "system", Content: compactionSystemPrompt},
		{Role: "user", Content: "Task prompt (verbatim goal anchor):\n\n" + request.Prompt + "\n\nConversation digest to compact:\n\n" + request.Digest},
	}, nil)
	if err != nil {
		return CompactResult{}, err
	}
	card := strings.TrimSpace(completion.Content)
	card = strings.TrimPrefix(card, "```markdown")
	card = strings.TrimPrefix(card, "```md")
	card = strings.TrimSuffix(card, "```")
	card = strings.TrimSpace(card)
	if card == "" {
		return CompactResult{}, fmt.Errorf("compaction returned an empty card")
	}
	if len(card) > compactionCardMaxBytes {
		card = card[:compactionCardMaxBytes] + "…"
	}
	return CompactResult{Card: card}, nil
}

// compactionDigest renders the conversation for the summarizer: role-labeled
// text blocks with per-message head-and-tail bounds and screenshot counts in
// place of the image bytes (the vision blocks carry no text to summarize).
func compactionDigest(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
		content := m.Content
		if len(content) > compactionDigestMessageHead+compactionDigestMessageTail {
			content = content[:compactionDigestMessageHead] +
				fmt.Sprintf("\n…[%d bytes elided]…\n", len(content)-compactionDigestMessageHead-compactionDigestMessageTail) +
				content[len(content)-compactionDigestMessageTail:]
		}
		if len(m.Images) > 0 {
			content += fmt.Sprintf("\n[%d screenshot(s) attached to this message]", len(m.Images))
		}
		fmt.Fprintf(&b, "### %s\n%s\n\n", m.Role, content)
	}
	return b.String()
}

// compactMessages rebuilds the conversation around a state card: the system
// prompt and the original user prompt stay verbatim (the contract with the
// user), the card replaces the elided middle as one system message, and the
// newest turn groups stay whole at the end. Returns the rebuilt history plus
// kept/dropped message counts for the context.compacted event.
func compactMessages(messages []llm.Message, card string) (out []llm.Message, kept, dropped int) {
	if len(messages) < 3 {
		return messages, len(messages), 0
	}
	// Groups: one leading non-tool message plus the tool replies that follow
	// it. Cutting only between groups preserves the tool-replies-contiguous
	// contract of the provider API.
	type group struct{ start, end int }
	var groups []group
	for i := 2; i < len(messages); {
		start := i
		i++
		for i < len(messages) && messages[i].Role == "tool" {
			i++
		}
		groups = append(groups, group{start: start, end: i})
	}
	// Keep the newest groups whole, bounded by count and by bytes, but never
	// fewer than the minimum (a tail smaller than one model turn is useless).
	bytes := 0
	take := 0
	for g := len(groups) - 1; g >= 0; g-- {
		size := 0
		for _, m := range messages[groups[g].start:groups[g].end] {
			size += len(m.Content)
		}
		if take >= compactionKeepGroupsMin && (take >= compactionKeepGroupsMax || bytes+size > compactionTailBytes) {
			break
		}
		bytes += size
		take++
	}
	tail := groups[len(groups)-take:]
	out = append(out, messages[0], messages[1],
		llm.Message{Role: "system", Content: compactionCardPrefix + card})
	out = append(out, messages[tail[0].start:tail[len(tail)-1].end]...)
	kept = 2 + len(out) - 3
	dropped = len(messages) - kept
	return out, kept, dropped
}
