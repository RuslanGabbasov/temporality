package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/storage"
)

// registerStorageHistory mounts the file-attribution reads (docs/sandbox-storage.md
// §7.2, stage 2): which runs touched this path (the file card's «history»)
// and which paths a run touched (the «files of this run» view). Both are
// file.changed journal events; the kernel's Activities owns the journal
// client (auth, paging), so the routes only validate and shape.
func registerStorageHistory(mux *http.ServeMux, gate *controlplane.Gate, visibleProjects func(*http.Request) (map[string][]string, error), activities *agent.Activities) {
	authorize := func(w http.ResponseWriter, r *http.Request) (project string, ok bool) {
		project = r.PathValue("projectID")
		if err := storage.ValidProjectID(project); err != nil {
			writeError(w, 400, err)
			return "", false
		}
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
			return "", false
		}
		if visibleProjects != nil {
			visible, err := visibleProjects(r)
			if err != nil {
				writeError(w, 500, err)
				return "", false
			}
			if _, known := visible[project]; !known {
				writeError(w, 404, fmt.Errorf("project %q is not visible", project))
				return "", false
			}
		}
		return project, true
	}

	// Per-file change history, newest first. The area disambiguates equal
	// area-relative paths (workspace/notes.md vs cache/notes.md) — the
	// journal filters on data.path, the handler re-checks area client-side
	// for the same pre-upgrade-journal reason as FileChangeHistory.
	mux.HandleFunc("GET /v1/workspace/storage/projects/{projectID}/file/history", func(w http.ResponseWriter, r *http.Request) {
		project, ok := authorize(w, r)
		if !ok {
			return
		}
		area := r.URL.Query().Get("area")
		if err := storage.ValidArea(area); err != nil {
			writeError(w, 400, err)
			return
		}
		path := r.URL.Query().Get("path")
		if path == "" {
			writeError(w, 422, fmt.Errorf("path is required"))
			return
		}
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 {
				writeError(w, 400, fmt.Errorf("limit must be a positive integer"))
				return
			}
			if parsed > 100 {
				parsed = 100
			}
			limit = parsed
		}
		events, err := activities.FileChangeHistory(r.Context(), project, path, limit)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		changes := make([]map[string]any, 0, len(events))
		for _, event := range events {
			if historyString(event.Data, "area") != area {
				continue
			}
			changes = append(changes, map[string]any{
				"occurred_at": event.OccurredAt.UTC().Format(time.RFC3339),
				"run":         event.Context.Run,
				"op":          historyString(event.Data, "op"),
				"bytes":       historyInt(event.Data, "bytes"),
				"operation":   historyString(event.Data, "operation"),
			})
			if len(changes) == limit {
				break
			}
		}
		writeJSON(w, 200, map[string]any{"changes": changes})
	})

	// Every file one run touched, aggregated per (area, path): the storage
	// page's «files of this run» view after clicking a run badge.
	mux.HandleFunc("GET /v1/workspace/storage/projects/{projectID}/file/run-files", func(w http.ResponseWriter, r *http.Request) {
		project, ok := authorize(w, r)
		if !ok {
			return
		}
		run := r.URL.Query().Get("run")
		if run == "" {
			writeError(w, 422, fmt.Errorf("run is required"))
			return
		}
		events, err := activities.RunFileChanges(r.Context(), project, run)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		type fileStat struct {
			path    string
			area    string
			changes int
			lastOp  string
			lastAt  time.Time
		}
		byPath := map[string]*fileStat{}
		for _, event := range events {
			path := historyString(event.Data, "path")
			area := historyString(event.Data, "area")
			if path == "" {
				continue
			}
			key := area + "\x00" + path
			stat := byPath[key]
			if stat == nil {
				stat = &fileStat{path: path, area: area}
				byPath[key] = stat
			}
			stat.changes++
			// The walk is newest-first, so the first sighting carries the
			// latest op/time; later events only bump the counter.
			if stat.lastAt.IsZero() {
				stat.lastOp = historyString(event.Data, "op")
				stat.lastAt = event.OccurredAt
			}
		}
		files := make([]map[string]any, 0, len(byPath))
		for _, stat := range byPath {
			files = append(files, map[string]any{
				"path":    stat.path,
				"area":    stat.area,
				"changes": stat.changes,
				"last_op": stat.lastOp,
				"last_at": stat.lastAt.UTC().Format(time.RFC3339),
			})
		}
		sort.Slice(files, func(i, j int) bool {
			return files[i]["last_at"].(string) > files[j]["last_at"].(string)
		})
		writeJSON(w, 200, map[string]any{"files": files})
	})
}

// historyString/historyInt read the file.changed payload fields without the
// map-type ceremony at every call site.
func historyString(data map[string]any, key string) string {
	if value, _ := data[key].(string); value != "" {
		return value
	}
	return ""
}

func historyInt(data map[string]any, key string) int {
	switch value := data[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	default:
		return 0
	}
}
