package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// maxPrimingKnowledgeIDs caps the batch size of the priming knowledge lookup
// endpoint: priming cues reference a handful of items, and the loop below does
// one upstream request per id.
const maxPrimingKnowledgeIDs = 20

// parseKnowledgeIDs parses the comma-separated "ids" query parameter into a
// deduplicated id list preserving first-seen order. Whitespace around ids is
// tolerated, empty segments are dropped.
func parseKnowledgeIDs(raw string) ([]string, error) {
	seen := make(map[string]bool)
	ids := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("ids is required")
	}
	if len(ids) > maxPrimingKnowledgeIDs {
		return nil, fmt.Errorf("too many ids (max %d)", maxPrimingKnowledgeIDs)
	}
	return ids, nil
}

// fetchKnowledgeByIDs resolves knowledge items by id from the Journal's
// knowledge endpoint. Ids the Journal reports as unknown are returned in
// missing (order preserved) instead of failing the whole batch; transport
// errors and non-200/404 responses fail the call.
func fetchKnowledgeByIDs(ctx context.Context, client *http.Client, observationURL, apiToken, project string, ids []string) (found []observation.Knowledge, missing []string, err error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	found = make([]observation.Knowledge, 0, len(ids))
	missing = make([]string, 0)
	for _, id := range ids {
		path := "/v1/observations/knowledge?project=" + url.QueryEscape(project) + "&knowledge_id=" + url.QueryEscape(id)
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, observationURL+path, nil)
		if requestErr != nil {
			return nil, nil, fmt.Errorf("knowledge %s: %w", id, requestErr)
		}
		if apiToken != "" {
			request.Header.Set("Authorization", "Bearer "+apiToken)
		}
		response, requestErr := client.Do(request)
		if requestErr != nil {
			return nil, nil, fmt.Errorf("knowledge %s: %w", id, requestErr)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, nil, fmt.Errorf("knowledge %s: %w", id, readErr)
		}
		switch response.StatusCode {
		case http.StatusOK:
			var payload struct {
				Knowledge []observation.Knowledge `json:"knowledge"`
			}
			if decodeErr := json.Unmarshal(body, &payload); decodeErr != nil {
				return nil, nil, fmt.Errorf("knowledge %s: %w", id, decodeErr)
			}
			found = append(found, payload.Knowledge...)
		case http.StatusNotFound:
			missing = append(missing, id)
		default:
			return nil, nil, fmt.Errorf("knowledge %s: %s", id, strings.TrimSpace(string(body)))
		}
	}
	return found, missing, nil
}
