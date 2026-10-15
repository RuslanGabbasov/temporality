package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/temporality-project/temporality/observation"
)

// Artifact tools (docs/artifact-registry.md §6): read-only navigation over the
// reference document registry. Bodies never enter the prompt implicitly — the
// agent pulls one chunk at a time with artifact_read, when the task matches
// the artifact's applicability description.
const (
	artifactReadChunk  = 32768   // 32 KiB per artifact_read call
	artifactFetchLimit = 2 << 20 // same 2 MiB cap as builtin bodies
	artifactCacheTTL   = 15 * time.Minute
)

// cachedArtifact is one fetched url-provider body. The hash travels with the
// content so artifact.read events stay honest about what the agent consumed.
type cachedArtifact struct {
	content   string
	hash      string
	mimeType  string
	fetchedAt time.Time
}

func (a *Activities) handleArtifactSearch(ctx context.Context, request ToolRequest) (ToolResult, error) {
	// Artifacts are workspace-global; the list endpoint ships metadata rows
	// without bodies, so the search payload stays small at any registry size.
	endpoint := fmt.Sprintf("%s/v1/workspace/artifacts", a.WorkspaceURL)
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: artifact search failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	query, _ := request.Arguments["query"].(string)
	if query == "" {
		return ToolResult{Content: compactJSON(body, 4000)}, nil
	}
	var page struct {
		Artifacts []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Provider    string `json:"provider"`
			SizeBytes   int64  `json:"size_bytes"`
			Status      string `json:"status"`
			Kind        string `json:"kind"`
			Version     int    `json:"version"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	// Same matching contract as skill_search: per-keyword occurrence over the
	// searchable fields, ranked by coverage, registry order on ties.
	keywords := strings.Fields(strings.ToLower(query))
	type scoredMatch struct {
		item  map[string]any
		score int
	}
	var scored []scoredMatch
	for _, artifact := range page.Artifacts {
		haystack := strings.ToLower(artifact.ID + " " + artifact.Title + " " + artifact.Description)
		score := 0
		for _, kw := range keywords {
			if strings.Contains(haystack, kw) {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredMatch{item: map[string]any{
				"id": artifact.ID, "title": artifact.Title, "description": artifact.Description,
				"provider": artifact.Provider, "size_bytes": artifact.SizeBytes,
				"status": artifact.Status, "kind": artifact.Kind, "version": artifact.Version,
			}, score: score})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	var matches []map[string]any
	for _, m := range scored {
		matches = append(matches, m.item)
	}
	encoded, _ := json.Marshal(map[string]any{"artifacts": matches, "count": len(matches)})
	return ToolResult{Content: string(encoded)}, nil
}

func (a *Activities) handleArtifactRead(ctx context.Context, request ToolRequest) (ToolResult, error) {
	artifactID, _ := request.Arguments["artifact_id"].(string)
	if artifactID == "" {
		return ToolResult{Content: "error: artifact_id is required"}, nil
	}
	offset := 0
	if raw, ok := request.Arguments["offset"].(float64); ok && raw > 0 {
		offset = int(raw)
	}
	endpoint := fmt.Sprintf("%s/v1/workspace/artifacts/%s?offset=%d&limit=%d",
		a.WorkspaceURL, url.PathEscape(artifactID), offset, artifactReadChunk)
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status == 404 {
		return ToolResult{Content: fmt.Sprintf("error: artifact %q not found; use artifact_search to list ids", artifactID)}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: artifact read failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	var page struct {
		Artifact struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Provider    string `json:"provider"`
			ExternalRef string `json:"external_ref"`
			Content     string `json:"content"`
			ContentHash string `json:"content_hash"`
			SizeBytes   int64  `json:"size_bytes"`
			MimeType    string `json:"mime_type"`
			Status      string `json:"status"`
			Kind        string `json:"kind"`
			Version     int    `json:"version"`
		} `json:"artifact"`
		Offset     int  `json:"offset"`
		NextOffset int  `json:"next_offset"`
		Truncated  bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	// url artifacts keep their body in an external system: the kernel fetches
	// it once and serves chunks from cache (egress stays here — the sandbox
	// never dials out, docs/artifact-registry.md §10 phase 2).
	if page.Artifact.Provider == "url" {
		doc, fetchedNow, err := a.cachedExternalArtifact(ctx, page.Artifact.ID, page.Artifact.ExternalRef)
		if err != nil {
			return ToolResult{Content: fmt.Sprintf("error: external document unavailable (artifact %s): %v", page.Artifact.ID, err)}, nil
		}
		content, next := chunkUTF8(doc.content, offset, artifactReadChunk)
		truncated := next < len(doc.content)
		_ = a.emitArtifactReadEvent(ctx, request, page.Artifact.ID, page.Artifact.Title,
			doc.hash, offset, len(content))
		encoded, _ := json.Marshal(map[string]any{
			"artifact_id": page.Artifact.ID, "title": page.Artifact.Title,
			"content": content, "offset": offset, "next_offset": next,
			"truncated": truncated, "checksum": doc.hash, "size_bytes": len(doc.content),
			"mime_type":    doc.mimeType,
			"external_ref": page.Artifact.ExternalRef,
			"status":       page.Artifact.Status, "kind": page.Artifact.Kind, "version": page.Artifact.Version,
			"cached_until": doc.fetchedAt.Add(artifactCacheTTL).Format(time.RFC3339),
			"refetched":    fetchedNow,
		})
		return ToolResult{Content: string(encoded)}, nil
	}
	// Audit trail (docs/artifact-registry.md §7): artifact.read lands in the
	// journal best-effort — a logging failure must not cost the agent the
	// document. Retries are harmless: the event id embeds UnixNano.
	_ = a.emitArtifactReadEvent(ctx, request, page.Artifact.ID, page.Artifact.Title,
		page.Artifact.ContentHash, page.Offset, len(page.Artifact.Content))
	encoded, _ := json.Marshal(map[string]any{
		"artifact_id": page.Artifact.ID, "title": page.Artifact.Title,
		"content": page.Artifact.Content, "offset": page.Offset,
		"next_offset": page.NextOffset, "truncated": page.Truncated,
		"checksum": page.Artifact.ContentHash, "size_bytes": page.Artifact.SizeBytes,
		"mime_type": page.Artifact.MimeType,
		"status":    page.Artifact.Status, "kind": page.Artifact.Kind, "version": page.Artifact.Version,
	})
	return ToolResult{Content: string(encoded)}, nil
}

// cachedExternalArtifact returns the url-provider body from cache when fresh,
// or fetches it (reporting fetchedNow). The fetch honors the tool context and
// the builtin 2 MiB cap: an oversized external document is an error, not a
// silent truncation — the agent must know the body it sees is partial only
// when chunked, never because the fetch gave up.
func (a *Activities) cachedExternalArtifact(ctx context.Context, artifactID, externalRef string) (cachedArtifact, bool, error) {
	a.artifactCacheMu.Lock()
	if entry, ok := a.artifactCache[artifactID]; ok && time.Since(entry.fetchedAt) < artifactCacheTTL {
		a.artifactCacheMu.Unlock()
		return entry, false, nil
	}
	a.artifactCacheMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, externalRef, nil)
	if err != nil {
		return cachedArtifact{}, false, fmt.Errorf("invalid external_ref: %w", err)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return cachedArtifact{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return cachedArtifact{}, false, fmt.Errorf("external system answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, artifactFetchLimit+1))
	if err != nil {
		return cachedArtifact{}, false, err
	}
	if len(body) > artifactFetchLimit {
		return cachedArtifact{}, false, fmt.Errorf("body exceeds %d bytes", artifactFetchLimit)
	}
	sum := sha256.Sum256(body)
	entry := cachedArtifact{
		content:   string(body),
		hash:      hex.EncodeToString(sum[:]),
		mimeType:  strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]),
		fetchedAt: time.Now().UTC(),
	}
	if a.artifactCache == nil {
		a.artifactCache = make(map[string]cachedArtifact)
	}
	a.artifactCacheMu.Lock()
	a.artifactCache[artifactID] = entry
	a.artifactCacheMu.Unlock()
	return entry, true, nil
}

// chunkUTF8 slices content bytes [offset, offset+limit) backing the cut off
// to a rune boundary, and returns the next offset (whole body when limit<=0).
func chunkUTF8(content string, offset, limit int) (string, int) {
	if offset > len(content) {
		offset = len(content)
	}
	rest := content[offset:]
	if limit <= 0 || limit >= len(rest) {
		return rest, len(content)
	}
	cut := rest[:limit]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut, offset + len(cut)
}

// emitArtifactReadEvent records artifact.read in the observation journal via
// the same durable outbox as saga events; the caller treats it as best-effort.
func (a *Activities) emitArtifactReadEvent(ctx context.Context, request ToolRequest, artifactID, title, checksum string, offset, bytes int) error {
	if a.Events == nil {
		return fmt.Errorf("event outbox is not configured")
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("%s/artifact.read/%d", request.RunID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: a.SourceID, Integration: "temporality-agent-kernel", Version: "1"},
		Context: observation.Context{
			Project: request.Project, Run: request.RunID, Task: request.TaskID,
			Actor: observation.Actor{ID: a.SourceID, Type: "agent"},
		},
		Type: "artifact.read",
		Data: map[string]any{
			"artifact_id": artifactID, "title": title, "checksum": checksum,
			"offset": offset, "bytes": bytes,
		},
	}
	return a.Events.Enqueue(ctx, event)
}
