package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The existing recordingOutbox (sagatools_test.go) doubles as the event sink.

func TestArtifactSearchMatchesAnyKeyword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/artifacts" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"artifacts": []any{
			map[string]any{"id": "coding-standard", "title": "Стандарт кодирования", "description": "code style rules for any Go code", "provider": "builtin", "size_bytes": 12000},
			map[string]any{"id": "release-checklist", "title": "Release Checklist", "description": "checklist for release; includes code review gate", "provider": "builtin", "size_bytes": 800},
			map[string]any{"id": "deploy-runbook", "title": "Deploy Runbook", "description": "how to deploy", "provider": "url", "size_bytes": 0},
		}})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	type page struct {
		Artifacts []struct {
			ID string `json:"id"`
		} `json:"artifacts"`
		Count int `json:"count"`
	}
	run := func(query string) page {
		t.Helper()
		result, err := activities.RunTool(context.Background(), ToolRequest{
			Project: "demo", Name: "artifact_search", Arguments: map[string]any{"query": query},
		})
		if err != nil {
			t.Fatalf("artifact_search(%q): %v", query, err)
		}
		var out page
		if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
			t.Fatalf("artifact_search(%q): parse result: %v (content %q)", query, err, result.Content)
		}
		return out
	}

	// Same contract as skill_search: per-keyword matching, coverage ranking.
	got := run("code review gate")
	if got.Count != 2 {
		t.Fatalf("want 2 artifacts for multi-word query, got %d", got.Count)
	}
	if ids := []string{got.Artifacts[0].ID, got.Artifacts[1].ID}; ids[0] != "release-checklist" || ids[1] != "coding-standard" {
		t.Fatalf("want [release-checklist coding-standard] ordered by coverage, got %v", ids)
	}
	if got := run("deploy"); got.Count != 1 || got.Artifacts[0].ID != "deploy-runbook" {
		t.Fatalf("want deploy-runbook, got %+v", got)
	}
	if got := run("nomatch anywhere"); got.Count != 0 {
		t.Fatalf("want empty result, got %+v", got)
	}
}

func TestArtifactReadChunksAndEmitsBestEffortEvent(t *testing.T) {
	body := strings.Repeat("миграция ", 100) // 17 bytes per group, 1700 total
	outbox := &recordingOutbox{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace/artifacts/coding-standard" {
			http.NotFound(w, r)
			return
		}
		offset := 0
		fmt.Sscan(r.URL.Query().Get("offset"), &offset)
		limit := 0
		fmt.Sscan(r.URL.Query().Get("limit"), &limit)
		chunk := body
		if offset > len(chunk) {
			offset = len(chunk)
		}
		chunk = chunk[offset:]
		truncated := false
		if limit > 0 && limit < len(chunk) {
			// UTF-8-safe cut (not exercised below: the chunk cap exceeds the body).
			cut := limit
			for cut > 0 && !isUTF8Boundary(chunk, cut) {
				cut--
			}
			chunk = chunk[:cut]
			truncated = offset+cut < len(body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"artifact": map[string]any{
				"id": "coding-standard", "title": "Стандарт кодирования", "provider": "builtin",
				"content": chunk, "content_hash": "sha256:abc", "size_bytes": len(body), "mime_type": "text/markdown",
			},
			"offset": offset, "next_offset": offset + len(chunk), "truncated": truncated,
		})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal", Events: outbox}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "run-1", Project: "demo", Name: "artifact_read",
		Arguments: map[string]any{"artifact_id": "coding-standard"},
	})
	if err != nil {
		t.Fatalf("artifact_read: %v", err)
	}
	var page struct {
		Content    string `json:"content"`
		Offset     int    `json:"offset"`
		NextOffset int    `json:"next_offset"`
		Truncated  bool   `json:"truncated"`
		Checksum   string `json:"checksum"`
	}
	if err := json.Unmarshal([]byte(result.Content), &page); err != nil {
		t.Fatalf("parse result: %v (content %q)", err, result.Content)
	}
	// The 32 KiB chunk cap exceeds the 1700-byte body: one call returns it
	// whole and reports the end of the document.
	if page.Offset != 0 || page.Truncated || page.NextOffset != len(body) {
		t.Fatalf("unexpected chunk envelope: %+v", page)
	}
	if page.Checksum != "sha256:abc" {
		t.Fatalf("checksum must round-trip for the audit trail, got %q", page.Checksum)
	}

	// The journal event lands best-effort: artifact_id, checksum, offset, bytes.
	if len(outbox.events) != 1 {
		t.Fatalf("want 1 artifact.read event, got %d", len(outbox.events))
	}
	event := outbox.events[0]
	if event.Type != "artifact.read" || event.Context.Run != "run-1" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if event.Data["artifact_id"] != "coding-standard" || event.Data["checksum"] != "sha256:abc" {
		t.Fatalf("event data: %+v", event.Data)
	}
	if bytes, ok := event.Data["bytes"].(int); !ok || bytes <= 0 {
		t.Fatalf("event must carry the chunk size: %+v", event.Data)
	}
}

func TestArtifactReadURLProviderProxiesThroughKernel(t *testing.T) {
	// The sandbox has no egress: url artifacts are fetched by the kernel once,
	// cached, and served in the same chunk envelope as builtin bodies.
	var fetches int32
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docs/deploy.md" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&fetches, 1)
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Deploy runbook\n"))
	}))
	defer counting.Close()

	outbox := &recordingOutbox{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"artifact": map[string]any{
				"id": "deploy-runbook", "title": "Deploy Runbook", "provider": "url",
				"external_ref": counting.URL + "/docs/deploy.md", "content": "", "size_bytes": 0,
			},
			"offset": 0, "next_offset": 0, "truncated": false,
		})
	}))
	defer server.Close()
	activities := &Activities{HTTP: counting.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal", Events: outbox}

	read := func() (content, checksum, externalRef string) {
		t.Helper()
		result, err := activities.RunTool(context.Background(), ToolRequest{
			RunID: "run-1", Project: "demo", Name: "artifact_read",
			Arguments: map[string]any{"artifact_id": "deploy-runbook"},
		})
		if err != nil {
			t.Fatalf("artifact_read: %v", err)
		}
		var page struct {
			Content     string `json:"content"`
			Checksum    string `json:"checksum"`
			ExternalRef string `json:"external_ref"`
		}
		if err := json.Unmarshal([]byte(result.Content), &page); err != nil {
			t.Fatalf("parse result: %v (content %q)", err, result.Content)
		}
		return page.Content, page.Checksum, page.ExternalRef
	}

	firstContent, firstChecksum, externalRef := read()
	if !strings.Contains(firstContent, "Deploy runbook") {
		t.Fatalf("proxied body must reach the agent, got %q", firstContent)
	}
	if firstChecksum == "" || externalRef == "" {
		t.Fatalf("checksum and external_ref must round-trip (checksum %q, ref %q)", firstChecksum, externalRef)
	}
	_, secondChecksum, _ := read()
	if firstChecksum != secondChecksum {
		t.Fatalf("cached read must be byte-stable: %q vs %q", firstChecksum, secondChecksum)
	}
	if got := atomic.LoadInt32(&fetches); got != 1 {
		t.Fatalf("TTL cache must serve the second read without refetching, fetches=%d", got)
	}

	// Proxied reads emit artifact.read too: the audit trail covers external
	// documents exactly like builtin ones.
	if len(outbox.events) != 2 {
		t.Fatalf("want 2 artifact.read events (one per read), got %d", len(outbox.events))
	}
	if outbox.events[0].Data["checksum"] != firstChecksum {
		t.Fatalf("event checksum: %+v", outbox.events[0].Data)
	}
}

func TestArtifactReadURLProviderUnavailable(t *testing.T) {
	// The unavailable external document is a tagged tool error, not a hang or
	// a crash: the agent sees exactly which artifact failed and why.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL + "/gone.md"
	dead.Close() // port closed: every dial fails

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"artifact": map[string]any{
				"id": "deploy-runbook", "title": "Deploy Runbook", "provider": "url",
				"external_ref": deadURL, "content": "", "size_bytes": 0,
			},
			"offset": 0, "next_offset": 0, "truncated": false,
		})
	}))
	defer server.Close()
	outbox := &recordingOutbox{}
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal", Events: outbox}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "run-1", Project: "demo", Name: "artifact_read",
		Arguments: map[string]any{"artifact_id": "deploy-runbook"},
	})
	if err != nil {
		t.Fatalf("artifact_read: %v", err)
	}
	if !strings.Contains(result.Content, "external document unavailable") || !strings.Contains(result.Content, "deploy-runbook") {
		t.Fatalf("expected a tagged unavailability error, got %q", result.Content)
	}
	if len(outbox.events) != 0 {
		t.Fatalf("failed fetch must not emit artifact.read, got %d", len(outbox.events))
	}
}

func TestArtifactReadMissingID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		Project: "demo", Name: "artifact_read", Arguments: map[string]any{"artifact_id": "missing"},
	})
	if err != nil {
		t.Fatalf("artifact_read: %v", err)
	}
	if !strings.Contains(result.Content, "artifact_search") || !strings.Contains(result.Content, "error:") {
		t.Fatalf("expected a pointer back to artifact_search, got %q", result.Content)
	}
}

func TestArtifactReadWithoutOutboxStillReads(t *testing.T) {
	// Best-effort audit: a missing outbox must not cost the agent the document.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"artifact": map[string]any{
				"id": "coding-standard", "title": "x", "provider": "builtin",
				"content": "правило", "content_hash": "h", "size_bytes": 12, "mime_type": "text/markdown",
			},
			"offset": 0, "next_offset": 12, "truncated": false,
		})
	}))
	defer server.Close()
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.RunTool(context.Background(), ToolRequest{
		RunID: "run-1", Project: "demo", Name: "artifact_read",
		Arguments: map[string]any{"artifact_id": "coding-standard"},
	})
	if err != nil {
		t.Fatalf("artifact_read: %v", err)
	}
	if !strings.Contains(result.Content, "правило") {
		t.Fatalf("document body must reach the agent, got %q", result.Content)
	}
}

// isUTF8Boundary reports whether byte i starts a new rune (or is the end).
func isUTF8Boundary(s string, i int) bool {
	if i == 0 || i >= len(s) {
		return true
	}
	return s[i]&0xC0 != 0x80
}
