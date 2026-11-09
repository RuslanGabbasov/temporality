package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
)

// Describe-only mode (docs/harness-settings.md): whatever a tool attached,
// the result carries text instead of pixels. When the inspection itself
// fails (no vision model configured, provider error) the block is STILL
// dropped — the mode's contract is that base64 never reaches the
// conversation.
func TestDescribeToolImagesDropsBlocksOnFailure(t *testing.T) {
	a := &Activities{}
	result := a.describeToolImages(context.Background(), ToolResult{
		Content: "image attached: image/png 4x4",
		Images:  []llm.ImageBlock{{MIMEType: "image/png", Data: []byte("not-a-real-image")}},
	})
	if len(result.Images) != 0 {
		t.Fatal("describe-only mode must never keep vision blocks")
	}
	if !strings.Contains(result.Content, "describe-only mode") {
		t.Fatalf("the result must say the blocks were not kept, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "inspection failed") {
		t.Fatalf("a failed inspection must be named, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "image attached: image/png 4x4") {
		t.Fatal("the tool's own metadata must survive")
	}
}

func TestDescribeToolImagesNoopWithoutImages(t *testing.T) {
	a := &Activities{}
	result := a.describeToolImages(context.Background(), ToolResult{Content: "ok"})
	if result.Content != "ok" {
		t.Fatalf("text-only results must pass through, got %q", result.Content)
	}
}
