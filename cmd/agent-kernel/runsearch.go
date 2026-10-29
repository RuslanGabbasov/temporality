package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// Run trace analysis for agents (run_search / trace_read tools): an agent may
// inspect recent runs — but only through the org-structure lens of its own
// initiator. The initiator sees runs they started, runs of their unit's
// members and of subordinate units (the subtree below them), runs of ancestor
// units (installation-wide resources) and runs without an org assignment
// (transition semantics). Admins see everything; org-less initiators see only
// their own runs. Enforcement happens here, server-side: the tool's HTTP
// caller is the kernel itself, and the actor parameter names the run's
// initiator — the endpoint is gated to the kernel identity and admins for
// exactly that reason.

const runSearchEventsPage = 300

func registerRunSearchRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store, observationURL string, visibleUnits func(*http.Request) []string, journalToken string) {
	allowedActors := func(r *http.Request, actor string) ([]string, error) {
		users, err := ws.ListUsers(r.Context())
		if err != nil {
			return nil, err
		}
		var me *workspace.User
		for i := range users {
			if strings.EqualFold(users[i].ID, actor) || strings.EqualFold(users[i].Name, actor) {
				me = &users[i]
				break
			}
		}
		if me == nil || !me.Active {
			return []string{actor}, nil // no org profile: only their own runs
		}
		if me.Role == "admin" {
			ids := make([]string, 0, len(users))
			for _, u := range users {
				ids = append(ids, u.ID)
			}
			return ids, nil
		}
		// Subtree: units whose path hangs below mine (self path prefix);
		// ancestors: the unit chain above me. Org-less users are
		// installation-wide (transition semantics), so their runs stay
		// visible to everyone.
		allowedUnits := map[string]bool{}
		if me.OrgUnitID != "" {
			unit, err := ws.GetOrgUnit(r.Context(), me.OrgUnitID)
			if err == nil {
				self := unit.SelfPath()
				for _, u := range orgUnitsOf(ws) {
					if u.Path == "" || u.ID == me.OrgUnitID {
						continue
					}
					if strings.HasPrefix(u.Path, self+".") {
						allowedUnits[u.ID] = true
					}
				}
			}
			for _, ancestor := range unit.Ancestors() {
				allowedUnits[ancestor] = true
			}
		}
		ids := []string{me.ID}
		for _, u := range users {
			if !u.Active {
				continue
			}
			if u.OrgUnitID == "" || allowedUnits[u.OrgUnitID] {
				ids = append(ids, u.ID)
			}
		}
		return ids, nil
	}

	// GET /v1/agent/run-search?actor=…&project=…&status=…&limit=…
	mux.HandleFunc("GET /v1/agent/run-search", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := controlplane.FromContext(r.Context())
		if !ok || (principal.Subject != "kernel-internal" && principal.Role < controlplane.RoleAdmin) {
			writeError(w, 403, errors.New("run-search is a kernel tool surface"))
			return
		}
		actor := strings.TrimSpace(r.URL.Query().Get("actor"))
		if actor == "" {
			writeError(w, 422, errors.New("actor is required"))
			return
		}
		allowed, err := allowedActors(r, actor)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		limit := 15
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 50 {
				limit = parsed
			}
		}
		statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
		projectFilter := strings.TrimSpace(r.URL.Query().Get("project"))
		sinceDays := 14
		if raw := r.URL.Query().Get("since_days"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 90 {
				sinceDays = parsed
			}
		}
		since := time.Now().UTC().Add(-time.Duration(sinceDays) * 24 * time.Hour).Format(time.RFC3339Nano)

		started, err := fetchJournalEvents(r.Context(), observationURL, journalToken, url.Values{
			"type": {"run.started"}, "since": {since}, "limit": {strconv.Itoa(runSearchEventsPage)},
		})
		if err != nil {
			writeError(w, 500, err)
			return
		}
		allowedSet := make(map[string]bool, len(allowed))
		for _, id := range allowed {
			allowedSet[strings.ToLower(id)] = true
		}
		type runInfo struct {
			ID, Title, Actor, Project, Status string
			StartedAt                         string
		}
		seen := map[string]bool{}
		runs := []runInfo{}
		for _, event := range started {
			id, _ := event["run"].(string)
			if id == "" || seen[id] {
				continue
			}
			actorID, _ := event["actor"].(string)
			if !allowedSet[strings.ToLower(actorID)] {
				continue
			}
			seen[id] = true
			run, err := ws.GetRun(context.Background(), id)
			status := "running"
			if err == nil && run.Status != "" {
				status = run.Status
			}
			if statusFilter != "" && status != statusFilter {
				continue
			}
			project, _ := event["project"].(string)
			if projectFilter != "" && project != projectFilter {
				continue
			}
			title, _ := event["title"].(string)
			occurred, _ := event["occurred_at"].(string)
			runs = append(runs, runInfo{ID: id, Title: title, Actor: actorID, Project: project, Status: status, StartedAt: occurred})
			if len(runs) >= limit {
				break
			}
		}
		sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt > runs[j].StartedAt })
		writeJSON(w, 200, map[string]any{"runs": runs, "count": len(runs), "visible_actors": len(allowedSet)})
	})

	// GET /v1/agent/trace?actor=…&run=…
	mux.HandleFunc("GET /v1/agent/trace", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := controlplane.FromContext(r.Context())
		if !ok || (principal.Subject != "kernel-internal" && principal.Role < controlplane.RoleAdmin) {
			writeError(w, 403, errors.New("trace read is a kernel tool surface"))
			return
		}
		actor := strings.TrimSpace(r.URL.Query().Get("actor"))
		runID := strings.TrimSpace(r.URL.Query().Get("run"))
		if actor == "" || runID == "" {
			writeError(w, 422, errors.New("actor and run are required"))
			return
		}
		allowed, err := allowedActors(r, actor)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		allowedSet := make(map[string]bool, len(allowed))
		for _, id := range allowed {
			allowedSet[strings.ToLower(id)] = true
		}
		started, err := fetchJournalEvents(r.Context(), observationURL, journalToken, url.Values{
			"run": {runID}, "type": {"run.started"}, "limit": {"1"},
		})
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if len(started) == 0 {
			writeError(w, 404, errors.New("run not found"))
			return
		}
		startActor, _ := started[0]["actor"].(string)
		if !allowedSet[strings.ToLower(startActor)] {
			writeError(w, 403, fmt.Errorf("run %q was started outside your org scope", runID))
			return
		}
		events, err := fetchJournalEvents(r.Context(), observationURL, journalToken, url.Values{
			"run": {runID}, "order": {"desc"}, "limit": {strconv.Itoa(120)},
		})
		if err != nil {
			writeError(w, 500, err)
			return
		}
		trace := renderTraceTail(events, 60)
		writeJSON(w, 200, map[string]any{"run": runID, "actor": startActor, "trace": trace})
	})
}

func orgUnitsOf(ws *workspace.Store) []workspace.OrgUnit {
	units, err := ws.ListOrgUnits(context.Background())
	if err != nil {
		return nil
	}
	return units
}

// fetchJournalEvents GETs one page of journal events as raw maps.
func fetchJournalEvents(ctx context.Context, observationURL, journalToken string, params url.Values) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, observationURL+"/v1/observations/events?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if journalToken != "" {
		req.Header.Set("Authorization", "Bearer "+journalToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("journal events returned %d: %s", resp.StatusCode, truncateForLog(string(body), 200))
	}
	var page struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}
	return page.Events, nil
}

// renderTraceTail compacts a journal event list (newest first) into the
// model-readable trace block.
func renderTraceTail(events []map[string]any, cap int) string {
	lines := make([]string, 0, len(events))
	shown := 0
	for _, event := range events {
		if shown >= cap {
			break
		}
		eventType, _ := event["type"].(string)
		if eventType == "run.started" {
			continue
		}
		data, _ := event["data"].(map[string]any)
		line := "- " + eventType
		if data != nil {
			if tool, ok := data["tool"].(string); ok && tool != "" {
				line += " · " + tool
			}
			if errMsg, ok := data["error"].(string); ok && errMsg != "" {
				line += " — error: " + truncateForLog(errMsg, 200)
			} else if summary, ok := data["answer"].(string); ok && summary != "" {
				line += " — " + truncateForLog(summary, 300)
			} else if status, ok := data["status"].(string); ok && status != "" {
				line += " · " + status
			}
		}
		if at, ok := event["occurred_at"].(string); ok && len(at) >= 19 {
			line += " (" + at[11:19] + ")"
		}
		lines = append(lines, line)
		shown++
	}
	if len(lines) == 0 {
		return "(no journaled events)"
	}
	return strings.Join(lines, "\n")
}
