package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/temporality-project/temporality/observation"
)

// fileHistoryMaxPages bounds every journal walk on this file: pages arrive
// newest-first, so the panel fills after the first page in practice; the cap
// exists so a journal that ignores the path filter (a pre-upgrade peer) or a
// path with thousands of touches cannot scan forever — the endpoint returns
// a bounded prefix instead of hanging (same contract as toolEvents).
const fileHistoryMaxPages = 20

// pageJournal follows one events listing query through its cursor, calling
// stop with each page to let the caller end early once it collected enough.
// Deliberately separate from reconcile.go's toolEvents: that walk carries
// operations-panel error semantics; this one serves the storage page.
func (a *Activities) pageJournal(ctx context.Context, query string, stop func(events []observation.Event) bool) ([]observation.Event, error) {
	var collected []observation.Event
	cursor := ""
	for page := 0; page < fileHistoryMaxPages; page++ {
		full := query
		if cursor != "" {
			full += "&cursor=" + urlQueryEscape(cursor)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.TemporalityURL+"/v1/observations/events?"+full, nil)
		if err != nil {
			return nil, err
		}
		if a.apiToken() != "" {
			request.Header.Set("Authorization", "Bearer "+a.apiToken())
		}
		response, err := a.HTTP.Do(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
			response.Body.Close()
			return nil, fmt.Errorf("journal event listing (%s) returned HTTP %d: %s", query, response.StatusCode, strings.TrimSpace(string(body)))
		}
		var parsed struct {
			Events     []observation.Event `json:"events"`
			NextCursor string              `json:"next_cursor"`
		}
		err = json.NewDecoder(response.Body).Decode(&parsed)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		collected = append(collected, parsed.Events...)
		if stop != nil && stop(parsed.Events) {
			return collected, nil
		}
		if parsed.NextCursor == "" {
			return collected, nil
		}
		cursor = parsed.NextCursor
	}
	return collected, nil
}

// FileChangeHistory returns the newest file.changed events for one storage
// path — the storage page's «history» panel (docs/sandbox-storage.md §7.2,
// attribution). The journal filters by (project, type, path); the payload
// path is re-checked client-side so a journal that predates the path filter
// (it would ignore the query parameter and return the project's whole file
// traffic) still answers correctly instead of mixing foreign paths in.
func (a *Activities) FileChangeHistory(ctx context.Context, project, path string, limit int) ([]observation.Event, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	query := fmt.Sprintf("project=%s&type=file.changed&path=%s&order=desc&limit=%d",
		urlQueryEscape(project), urlQueryEscape(path), limit*2)
	matched := 0
	events, err := a.pageJournal(ctx, query, func(page []observation.Event) bool {
		for _, event := range page {
			if dataString(event.Data, "path") == path {
				matched++
			}
		}
		return matched >= limit
	})
	if err != nil {
		return nil, err
	}
	result := make([]observation.Event, 0, limit)
	for _, event := range events {
		if dataString(event.Data, "path") == path {
			result = append(result, event)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

// RunFileChanges returns every file.changed event of one run — the storage
// page's «files of this run» view. Grouping into a per-path summary is the
// caller's job (cmd/agent-kernel); the walk stops at the page cap like the
// other listings.
func (a *Activities) RunFileChanges(ctx context.Context, project, runID string) ([]observation.Event, error) {
	query := fmt.Sprintf("project=%s&run=%s&type=file.changed&order=desc&limit=500",
		urlQueryEscape(project), urlQueryEscape(runID))
	return a.pageJournal(ctx, query, nil)
}
