package agent

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/harness"
	"github.com/temporality-project/temporality/kernel/llm"
)

func TestCompactionDigestCapsMessages(t *testing.T) {
	budgets := harness.DefaultBudgets()
	big := strings.Repeat("x", budgets.CompactionDigestHeadBytes+budgets.CompactionDigestTailBytes+4096)
	digest := compactionDigest([]llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi", Images: []llm.ImageBlock{{MIMEType: "image/png", Data: make([]byte, 128)}}},
		{Role: "tool", Content: big},
	}, budgets.CompactionDigestHeadBytes, budgets.CompactionDigestTailBytes)
	if strings.Contains(digest, big) {
		t.Fatal("digest must cap oversized messages")
	}
	if !strings.Contains(digest, "bytes elided") {
		t.Fatal("digest truncation must be explicit")
	}
	if !strings.Contains(digest, "1 screenshot(s) attached") {
		t.Fatal("digest must note attached screenshots instead of the image bytes")
	}
	if !strings.HasPrefix(digest, "### system\nsys\n\n") {
		t.Fatalf("digest must lead with role-labeled blocks, got %q", digest[:40])
	}
}

// The rebuild must honor the conversation contract: system prompt and the
// original user prompt verbatim, the card as one framed system message, and
// the tail only at whole turn groups (tool replies never split from their
// tool_calls message).
func TestCompactMessagesKeepsContract(t *testing.T) {
	budgets := harness.DefaultBudgets()
	messages := []llm.Message{
		{Role: "system", Content: "SYS"},
		{Role: "user", Content: "the original request"},
	}
	// Ten groups (assistant with a tool call + its tool reply) sized so the
	// tail byte bound forces real elision.
	for i := 0; i < 10; i++ {
		messages = append(messages,
			llm.Message{Role: "assistant", Content: strings.Repeat("a", 30<<10), ToolCalls: []llm.ToolCall{{ID: "c", Name: "run_command"}}},
			llm.Message{Role: "tool", ToolCallID: "c", Content: strings.Repeat("o", 30<<10)},
		)
	}
	out, kept, dropped := compactMessages(messages, "CARD", budgets.CompactionTailBytes, budgets.CompactionKeepGroupsMin, budgets.CompactionKeepGroupsMax)

	if out[0].Content != "SYS" || out[1].Content != "the original request" {
		t.Fatal("system prompt and original request must stay verbatim")
	}
	if out[2].Role != "system" || !strings.HasPrefix(out[2].Content, compactionCardPrefix) || !strings.Contains(out[2].Content, "CARD") {
		t.Fatal("the card must ride as one framed system message")
	}
	// The tail must start at a group boundary: its first message is an
	// assistant message, never an orphaned tool reply.
	if out[3].Role != "assistant" {
		t.Fatalf("tail starts with role %s, want a group boundary", out[3].Role)
	}
	for i := 3; i < len(out); i++ {
		if out[i].Role == "tool" && (i == 3 || out[i-1].Role != "assistant") {
			t.Fatal("a tool reply must never split from its assistant message")
		}
	}
	if kept+dropped != len(messages) {
		t.Fatalf("kept(%d)+dropped(%d) != %d", kept, dropped, len(messages))
	}
	if dropped == 0 {
		t.Fatal("the elided middle must actually be dropped")
	}
	if kept != len(out)-1 {
		t.Fatalf("kept = %d, want %d original messages retained (all but the card)", kept, len(out)-1)
	}
	if len(messages) != 22 {
		t.Fatal("compactMessages must not mutate the input slice")
	}
}

func TestCompactMessagesTailBounds(t *testing.T) {
	budgets := harness.DefaultBudgets()
	messages := []llm.Message{
		{Role: "system", Content: "SYS"},
		{Role: "user", Content: "req"},
	}
	// Ten groups of 100KB each: the byte bound must bite before the count.
	for i := 0; i < 10; i++ {
		messages = append(messages,
			llm.Message{Role: "assistant", Content: strings.Repeat("a", 50<<10), ToolCalls: []llm.ToolCall{{ID: "x", Name: "t"}}},
			llm.Message{Role: "tool", ToolCallID: "x", Content: strings.Repeat("b", 50<<10)},
		)
	}
	out, kept, dropped := compactMessages(messages, "CARD", budgets.CompactionTailBytes, budgets.CompactionKeepGroupsMin, budgets.CompactionKeepGroupsMax)
	tailBytes := 0
	for _, m := range out[3:] {
		tailBytes += len(m.Content)
	}
	if tailBytes > budgets.CompactionTailBytes {
		t.Fatalf("tail is %d bytes, want <= %d", tailBytes, budgets.CompactionTailBytes)
	}
	// Whole groups only: every group is two messages, so the kept tail is an
	// even count and at least the minimum number of groups survives.
	if (len(out)-3)%2 != 0 {
		t.Fatalf("tail of %d messages splits a group", len(out)-3)
	}
	if len(out)-3 < budgets.CompactionKeepGroupsMin*2 {
		t.Fatalf("tail kept %d messages, want at least %d", len(out)-3, budgets.CompactionKeepGroupsMin*2)
	}
	if kept+dropped != len(messages) || dropped == 0 {
		t.Fatalf("kept=%d dropped=%d of %d — elision must account for everything", kept, dropped, len(messages))
	}
}

func TestCompactMessagesShortHistoryUnchanged(t *testing.T) {
	budgets := harness.DefaultBudgets()
	messages := []llm.Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
	out, kept, dropped := compactMessages(messages, "CARD", budgets.CompactionTailBytes, budgets.CompactionKeepGroupsMin, budgets.CompactionKeepGroupsMax)
	if len(out) != 2 || kept != 2 || dropped != 0 {
		t.Fatalf("a short history must pass through, got %d messages (kept=%d dropped=%d)", len(out), kept, dropped)
	}
}
