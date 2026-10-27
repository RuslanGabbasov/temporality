package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// read_file must stream through the byte cap: an oversized file comes back as
// the capped head plus the exact truncation marker (with the full file size).
func TestFileToolsReadTruncatesOversizedFile(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	payload := bytes.Repeat([]byte("a"), maxFileReadBytes+4096)
	payload[maxFileReadBytes-1] = 'b' // last byte of the returned head
	if err := os.WriteFile(filepath.Join(workspace, "huge.txt"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := activities.RunTool(context.Background(), fileToolRequest("read_file", workspace, false, map[string]any{"path": "huge.txt"}))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !strings.HasPrefix(result.Content, string(payload[:maxFileReadBytes])) {
		t.Fatal("read_file must return the first maxFileReadBytes bytes verbatim")
	}
	wantMarker := fmt.Sprintf("\n[truncated by Agent Kernel: file is %d bytes, returned the first %d]", len(payload), maxFileReadBytes)
	if !strings.HasSuffix(result.Content, wantMarker) {
		t.Fatalf("missing exact truncation marker:\n%q", result.Content[len(result.Content)-200:])
	}
}

// A file of exactly the cap is returned whole, without a truncation marker.
func TestFileToolsReadAtCapNotTruncated(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	payload := bytes.Repeat([]byte("x"), maxFileReadBytes)
	if err := os.WriteFile(filepath.Join(workspace, "cap.txt"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := activities.RunTool(context.Background(), fileToolRequest("read_file", workspace, false, map[string]any{"path": "cap.txt"}))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if len(result.Content) != maxFileReadBytes || strings.Contains(result.Content, "truncated") {
		t.Fatalf("file of exactly the cap must pass through unmarked, got %d bytes", len(result.Content))
	}
}

// Binary detection runs on the leading sample: an oversized binary with a NUL
// byte inside the first 8 KiB is rejected even though the rest is never read.
func TestFileToolsReadDetectsBinaryBySample(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	payload := bytes.Repeat([]byte("text line\n"), (maxFileReadBytes+4096)/10)
	payload[100] = 0x00
	if err := os.WriteFile(filepath.Join(workspace, "blob.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := activities.RunTool(context.Background(), fileToolRequest("read_file", workspace, false, map[string]any{"path": "blob.bin"}))
	kind, message := nonRetryableType(t, err)
	if kind != "InvalidToolArguments" || !strings.Contains(message, "binary") {
		t.Fatalf("oversized binary must be rejected, got %s: %s", kind, message)
	}
}

// The sample bound cuts both ways: a NUL byte beyond the first ~8 KiB is not
// treated as binary (isProbablyBinary only scans the leading sample).
func TestFileToolsReadBinarySampleStopsAtEightKiB(t *testing.T) {
	activities, workspace := newFileToolActivities(t)
	payload := bytes.Repeat([]byte("y"), 10<<10)
	payload[9000] = 0x00
	if err := os.WriteFile(filepath.Join(workspace, "late-nul.txt"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := activities.RunTool(context.Background(), fileToolRequest("read_file", workspace, false, map[string]any{"path": "late-nul.txt"}))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if len(result.Content) != len(payload) {
		t.Fatalf("text beyond the binary sample must pass through, got %d of %d bytes", len(result.Content), len(payload))
	}
}
