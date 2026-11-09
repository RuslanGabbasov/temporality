package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/harness"
	"github.com/temporality-project/temporality/kernel/llm"
)

func TestBoundedToolOutput(t *testing.T) {
	budgets := harness.DefaultBudgets()
	small := "git status: clean"
	if got := boundedToolOutput(small, budgets.ToolOutputMaxBytes, budgets.ToolOutputTailBytes); got != small {
		t.Fatalf("small output must pass through unchanged, got %q", got)
	}

	// A multi-megabyte output (the web/artifact fetch caps are 2 MiB) must
	// shrink to the budget with head, tail and an explicit notice — unbounded
	// outputs are what pushed the scheduler upload over its blob limit.
	head := strings.Repeat("H", budgets.ToolOutputMaxBytes)
	tail := strings.Repeat("T", budgets.ToolOutputTailBytes)
	big := head + strings.Repeat("M", 3<<20) + tail
	got := boundedToolOutput(big, budgets.ToolOutputMaxBytes, budgets.ToolOutputTailBytes)
	if len(got) >= len(big) {
		t.Fatalf("bounded output %d bytes, want << %d", len(got), len(big))
	}
	wantHead := budgets.ToolOutputMaxBytes - budgets.ToolOutputTailBytes
	if !strings.HasPrefix(got, strings.Repeat("H", wantHead)) {
		t.Fatal("bounded output must keep the head")
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatal("bounded output must keep the tail")
	}
	if !strings.Contains(got, "tool output truncated") {
		t.Fatal("bounded output must carry an explicit truncation notice")
	}
	if len(got) > budgets.ToolOutputMaxBytes+256 {
		t.Fatalf("bounded output %d bytes, want <= budget plus notice", len(got))
	}
}

func TestBoundedToolOutputStaysDeterministic(t *testing.T) {
	budgets := harness.DefaultBudgets()
	big := strings.Repeat("x", budgets.ToolOutputMaxBytes+1)
	first := boundedToolOutput(big, budgets.ToolOutputMaxBytes, budgets.ToolOutputTailBytes)
	if first != boundedToolOutput(big, budgets.ToolOutputMaxBytes, budgets.ToolOutputTailBytes) {
		t.Fatal("two calls over the same content must produce identical bytes")
	}
}

func TestModelRequestBytesCountsContentAndImages(t *testing.T) {
	base := ModelRequest{Model: "m", RunID: "r", Turn: 1, Messages: []llm.Message{{Role: "user", Content: strings.Repeat("a", 1000)}}}
	baseSize := modelRequestBytes(base)
	if baseSize <= 1000 {
		t.Fatalf("request bytes = %d, want content counted", baseSize)
	}

	withImage := base
	withImage.Messages = []llm.Message{{
		Role:    "user",
		Content: strings.Repeat("a", 1000),
		Images:  []llm.ImageBlock{{MIMEType: "image/png", Data: make([]byte, 3000)}},
	}}
	imageSize := modelRequestBytes(withImage)
	// base64 expansion of 3000 bytes is 4000 — the estimate must reflect the
	// WIRE size, not the raw bytes.
	if imageSize < baseSize+4000 {
		t.Fatalf("image request bytes = %d, want >= %d (base64 expansion counted)", imageSize, baseSize+4000)
	}
}

// The guard turns an unschedulable request into an honest failure instead of
// a TMPRL1103 upload loop. modelRequestBytes is the seam it branches on.
func TestModelRequestBytesFlagsOversizedRequest(t *testing.T) {
	budgets := harness.DefaultBudgets()
	huge := ModelRequest{Model: "m", RunID: "r", Turn: 58, Messages: []llm.Message{{Role: "tool", Content: strings.Repeat("z", budgets.MaxModelRequestBytes+1)}}}
	if size := modelRequestBytes(huge); size <= budgets.MaxModelRequestBytes {
		t.Fatalf("oversized request measured %d, want > %d", size, budgets.MaxModelRequestBytes)
	}
}

// Screenshots accumulate across turns and can cross the payload limit alone
// (the 2026-11-08 wedge). Elision sheds the oldest vision blocks first and
// never touches the stored conversation.
func TestElideRequestImagesKeepsNewest(t *testing.T) {
	image := make([]byte, 100<<10)
	messages := []llm.Message{
		{Role: "user", Content: "shot 1", Images: []llm.ImageBlock{{MIMEType: "image/png", Data: image}}},
		{Role: "user", Content: "shot 2", Images: []llm.ImageBlock{{MIMEType: "image/png", Data: image}}},
		{Role: "user", Content: "shot 3", Images: []llm.ImageBlock{{MIMEType: "image/png", Data: image}}},
	}
	request := ModelRequest{Model: "m", RunID: "r", Turn: 3, Messages: messages}
	budget := 160 << 10 // fits the text and one base64 image, not three
	result := elideRequestImages(request, budget)

	if modelRequestBytes(result) > budget {
		t.Fatalf("elided request is %d bytes, want <= %d", modelRequestBytes(result), budget)
	}
	if len(result.Messages[0].Images) != 0 || len(result.Messages[1].Images) != 0 {
		t.Fatal("the two oldest image blocks must be elided")
	}
	if len(result.Messages[2].Images) != 1 {
		t.Fatal("the newest image block must survive")
	}
	if !strings.Contains(result.Messages[0].Content, "elided") || !strings.Contains(result.Messages[1].Content, "elided") {
		t.Fatal("elided messages must carry an explicit notice")
	}
	// The conversation state is append-only: elision must not touch it.
	if len(request.Messages[0].Images) != 1 || len(request.Messages[1].Images) != 1 || len(request.Messages[2].Images) != 1 {
		t.Fatal("elision must not mutate the stored messages")
	}
}

func TestElideRequestImagesNoOpUnderBudget(t *testing.T) {
	request := ModelRequest{Model: "m", Messages: []llm.Message{{
		Role: "user", Content: "shot", Images: []llm.ImageBlock{{MIMEType: "image/png", Data: make([]byte, 1024)}},
	}}}
	result := elideRequestImages(request, 1<<20)
	if len(result.Messages[0].Images) != 1 {
		t.Fatal("a request under budget must keep its images")
	}
	if elideRequestImages(request, 1<<20).Messages[0].Content != result.Messages[0].Content {
		t.Fatal("elision must be deterministic")
	}
}

// Text alone over the budget cannot be shed by elision — the guard's honest
// failure is the right outcome there.
func TestElideRequestImagesLeavesTextOnlyRequest(t *testing.T) {
	request := ModelRequest{Model: "m", Messages: []llm.Message{{Role: "tool", Content: strings.Repeat("z", 300<<10)}}}
	result := elideRequestImages(request, 100<<10)
	if result.Messages[0].Content != request.Messages[0].Content {
		t.Fatal("a text-only request has nothing to elide")
	}
}

// The vision keep-window (agent config): at most keep image-bearing messages
// keep their vision blocks in the request; the stored conversation is never
// touched and each message's state flips exactly once, so the provider prompt
// cache is invalidated once per evicted screenshot.
func TestEvictRequestImagesKeepsWindow(t *testing.T) {
	shot := make([]byte, 1024)
	mk := func(i int) llm.Message {
		return llm.Message{Role: "user", Content: fmt.Sprintf("shot %d", i), Images: []llm.ImageBlock{{MIMEType: "image/png", Data: shot}}}
	}
	messages := []llm.Message{{Role: "system", Content: "SYS"}, mk(1), mk(2), mk(3), mk(4)}
	request := ModelRequest{Model: "m", Messages: messages}

	result := evictRequestImages(request, 2)
	if len(result.Messages[1].Images) != 0 || len(result.Messages[2].Images) != 0 {
		t.Fatal("the two oldest image messages must be evicted")
	}
	if len(result.Messages[3].Images) != 1 || len(result.Messages[4].Images) != 1 {
		t.Fatal("the newest keep-window messages must keep their vision blocks")
	}
	if !strings.Contains(result.Messages[1].Content, "image keep-window") {
		t.Fatal("evicted messages must carry an explicit notice")
	}
	// The stored conversation stays append-only.
	if len(messages[1].Images) != 1 || len(messages[2].Images) != 1 {
		t.Fatal("the window must not mutate the stored messages")
	}
	// Under the window nothing changes; keep=0 disables the policy.
	if len(evictRequestImages(request, 4).Messages[1].Images) != 1 || len(evictRequestImages(request, 0).Messages[1].Images) != 1 {
		t.Fatal("a fitting window must not evict anything")
	}
}

func TestVisionWindowCostCountsFlipPoint(t *testing.T) {
	shot := make([]byte, 1000)
	mk := func(i int) llm.Message {
		return llm.Message{Role: "user", Content: fmt.Sprintf("shot %d", i), Images: []llm.ImageBlock{{MIMEType: "image/png", Data: shot}}}
	}
	messages := []llm.Message{{Role: "system", Content: "SYS"}, mk(1), mk(2), mk(3)}
	evicted, rewritten := visionWindowCost(messages, 1)
	if evicted != 2 {
		t.Fatalf("evicted = %d, want 2", evicted)
	}
	// The flip point is the message that just left the window (shot 2):
	// everything from it to the end is re-written once.
	if rewritten <= messageBytes(messages[2]) {
		t.Fatalf("rewritten = %d, want the tail from the flip point", rewritten)
	}
	if total := messageBytes(messages[1]) + messageBytes(messages[2]) + messageBytes(messages[3]); rewritten >= total {
		t.Fatalf("rewritten = %d, must not include the already-evicted head", rewritten)
	}
	if evicted, _ := visionWindowCost(messages, 3); evicted != 0 {
		t.Fatalf("a fitting window must cost nothing, got %d evicted", evicted)
	}
}
