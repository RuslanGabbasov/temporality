package agent

import (
	"strings"
	"testing"

	"github.com/temporality-project/temporality/kernel/llm"
)

func TestBoundedToolOutput(t *testing.T) {
	small := "git status: clean"
	if got := boundedToolOutput(small); got != small {
		t.Fatalf("small output must pass through unchanged, got %q", got)
	}

	// A multi-megabyte output (the web/artifact fetch caps are 2 MiB) must
	// shrink to the budget with head, tail and an explicit notice — unbounded
	// outputs are what pushed the scheduler upload over its blob limit.
	head := strings.Repeat("H", toolOutputMaxBytes)
	tail := strings.Repeat("T", toolOutputTailBytes)
	big := head + strings.Repeat("M", 3<<20) + tail
	got := boundedToolOutput(big)
	if len(got) >= len(big) {
		t.Fatalf("bounded output %d bytes, want << %d", len(got), len(big))
	}
	wantHead := toolOutputMaxBytes - toolOutputTailBytes
	if !strings.HasPrefix(got, strings.Repeat("H", wantHead)) {
		t.Fatal("bounded output must keep the head")
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatal("bounded output must keep the tail")
	}
	if !strings.Contains(got, "tool output truncated") {
		t.Fatal("bounded output must carry an explicit truncation notice")
	}
	if len(got) > toolOutputMaxBytes+256 {
		t.Fatalf("bounded output %d bytes, want <= budget plus notice", len(got))
	}
}

func TestBoundedToolOutputStaysDeterministic(t *testing.T) {
	big := strings.Repeat("x", toolOutputMaxBytes+1)
	if boundedToolOutput(big) != boundedToolOutput(big) {
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
	huge := ModelRequest{Model: "m", RunID: "r", Turn: 58, Messages: []llm.Message{{Role: "tool", Content: strings.Repeat("z", maxModelRequestBytes+1)}}}
	if size := modelRequestBytes(huge); size <= maxModelRequestBytes {
		t.Fatalf("oversized request measured %d, want > %d", size, maxModelRequestBytes)
	}
}
