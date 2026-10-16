package main

// Storage inventory API (docs/sandbox-storage.md §5) — phase 1, read-only:
// the sandbox-root snapshot (cached in memory, ?refresh=1 forces a rewalk),
// the per-project drill-down tree and host health. Nothing here deletes or
// mutates the filesystem; sweeps and quota enforcement arrive with phase 2.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/storage"
	"github.com/temporality-project/temporality/observation"
)

// storageCacheTTL bounds how long a snapshot serves before the next GET
// rewalks. The hourly loop keeps it warm in practice.
const storageCacheTTL = time.Hour

// defaultStorageQuotaBytes is the phase-1 soft quota every project is shown
// against (50 GiB); per-project overrides land with phase 2 workspace
// settings. KERNEL_STORAGE_DEFAULT_QUOTA_BYTES overrides the default.
const defaultStorageQuotaBytes = 50 << 30

func storageQuotaBytes() int64 {
	if raw := strings.TrimSpace(os.Getenv("KERNEL_STORAGE_DEFAULT_QUOTA_BYTES")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultStorageQuotaBytes
}

// storageSweeperEnabled gates the background snapshot loop (and, in phase 2,
// the sweeper): KERNEL_STORAGE_SWEEPER defaults to on.
func storageSweeperEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KERNEL_STORAGE_SWEEPER"))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

// storageSnapshotCache memoizes the walk so dashboard refreshes never
// trigger filesystem storms. A rewalk serializes behind the mutex —
// concurrent callers block once, then share the fresh snapshot.
type storageSnapshotCache struct {
	mu  sync.Mutex
	inv *storage.Inventory
}

func (c *storageSnapshotCache) get(ctx context.Context, root string, force bool) (*storage.Inventory, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.inv != nil && time.Since(c.inv.GeneratedAt) < storageCacheTTL {
		return c.inv, nil
	}
	inv, err := storage.Snapshot(ctx, root, storage.WalkOptions{})
	if err != nil {
		// A failing rewalk (transient IO) still serves the last good numbers.
		if c.inv != nil {
			slog.Warn("storage snapshot rewalk failed, serving stale", "error", err)
			return c.inv, nil
		}
		return nil, err
	}
	c.inv = inv
	return inv, nil
}

type storageTopPathView struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type storageProjectView struct {
	Project        string               `json:"project"`
	WorkspaceBytes int64                `json:"workspace_bytes"`
	WorkspaceFiles int64                `json:"workspace_files"`
	CacheBytes     int64                `json:"cache_bytes"`
	QuotaBytes     int64                `json:"quota_bytes"`
	QuotaUsed      float64              `json:"quota_used"`
	TopPaths       []storageTopPathView `json:"top_paths"`
	LastActivityAt *time.Time           `json:"last_activity_at,omitempty"`
}

type storageSnapshotView struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Root        string               `json:"root"`
	Disk        storage.DiskUsage    `json:"disk"`
	Truncated   bool                 `json:"truncated"`
	Projects    []storageProjectView `json:"projects"`
}

func newStorageProjectView(p storage.ProjectUsage, quota int64) storageProjectView {
	view := storageProjectView{
		Project:        p.Project,
		WorkspaceBytes: p.Workspace.Bytes,
		WorkspaceFiles: p.Workspace.Files,
		CacheBytes:     p.Cache.Bytes,
		QuotaBytes:     quota,
		TopPaths:       []storageTopPathView{},
	}
	if quota > 0 {
		view.QuotaUsed = float64(p.Workspace.Bytes+p.Cache.Bytes) / float64(quota)
	}
	last := p.Workspace.ModAt
	if p.Cache.ModAt.After(last) {
		last = p.Cache.ModAt
	}
	if !last.IsZero() {
		at := last.UTC()
		view.LastActivityAt = &at
	}
	for _, path := range p.TopPaths {
		view.TopPaths = append(view.TopPaths, storageTopPathView(path))
	}
	return view
}

type storageProjectTreeView struct {
	Project   string               `json:"project"`
	Root      string               `json:"root"`
	Depth     int                  `json:"depth"`
	Truncated bool                 `json:"truncated"`
	Workspace *storage.TreeNode    `json:"workspace"`
	Cache     *storage.TreeNode    `json:"cache"`
	TopFiles  []storageTopPathView `json:"top_files"`
}

type storageHealthView struct {
	storage.HostHealth
	Docker storage.DockerFootprint `json:"docker"`
}

// RegisterStorageRoutes mounts the read-only storage inventory and host
// health API. visibleProjects resolves the caller's project visibility (the
// same ladder as the runs list); an empty root means the kernel runs without
// a sandbox root and the storage routes answer 503.
func RegisterStorageRoutes(mux *http.ServeMux, gate *controlplane.Gate, visibleProjects func(*http.Request) (map[string][]string, error), root, dockerBinary string, cache *storageSnapshotCache) {
	mux.HandleFunc("GET /v1/workspace/storage", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		if root == "" {
			writeError(w, 503, errors.New("storage inventory requires KERNEL_SANDBOX_ROOT to be configured"))
			return
		}
		visible, err := visibleProjects(r)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		inv, err := cache.get(r.Context(), root, r.URL.Query().Get("refresh") == "1")
		if err != nil {
			writeError(w, 500, err)
			return
		}
		quota := storageQuotaBytes()
		view := storageSnapshotView{
			GeneratedAt: inv.GeneratedAt,
			Root:        inv.Root,
			Disk:        inv.Disk,
			Truncated:   inv.Truncated,
			Projects:    make([]storageProjectView, 0, len(inv.Projects)),
		}
		for _, p := range inv.Projects {
			if _, ok := visible[p.Project]; !ok {
				continue
			}
			view.Projects = append(view.Projects, newStorageProjectView(p, quota))
		}
		writeJSON(w, 200, view)
	})

	mux.HandleFunc("GET /v1/workspace/storage/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		if root == "" {
			writeError(w, 503, errors.New("storage inventory requires KERNEL_SANDBOX_ROOT to be configured"))
			return
		}
		id := r.PathValue("id")
		visible, err := visibleProjects(r)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		// Invisible and unknown projects answer identically — the storage
		// layout must not leak which project ids exist on disk.
		if _, ok := visible[id]; !ok {
			writeError(w, 404, fmt.Errorf("project %q not found", id))
			return
		}
		depth := 4
		if raw := r.URL.Query().Get("depth"); raw != "" {
			if n, parseErr := strconv.Atoi(raw); parseErr == nil {
				depth = n
			}
		}
		if depth < 1 {
			depth = 1
		}
		if depth > 8 {
			depth = 8
		}
		tree, err := storage.ProjectTreeSnapshot(r.Context(), root, id, storage.TreeOptions{Depth: depth})
		if err != nil {
			if errors.Is(err, storage.ErrInvalidProject) {
				writeError(w, 400, err)
				return
			}
			writeError(w, 500, err)
			return
		}
		view := storageProjectTreeView{
			Project:   tree.Project,
			Root:      root,
			Depth:     depth,
			Truncated: tree.Truncated,
			Workspace: tree.Workspace,
			Cache:     tree.Cache,
			TopFiles:  make([]storageTopPathView, 0, len(tree.TopFiles)),
		}
		for _, file := range tree.TopFiles {
			view.TopFiles = append(view.TopFiles, storageTopPathView(file))
		}
		writeJSON(w, 200, view)
	})

	mux.HandleFunc("GET /v1/system/health", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		paths := []string{"/"}
		if root != "" {
			paths = append(paths, root)
		}
		health := storage.ReadHostHealth(paths...)
		docker := storage.ReadDockerFootprint(r.Context(), dockerBinary)
		writeJSON(w, 200, storageHealthView{HostHealth: health, Docker: docker})
	})
}

// emitStorageSnapshotEvent journals the hourly footprint (docs/sandbox-storage.md
// §5.5) — the read-only phase-1 companion of the future sweeper events.
func emitStorageSnapshotEvent(ctx context.Context, events sandboxProfileOutbox, inv *storage.Inventory) {
	if events == nil || inv == nil {
		return
	}
	projects := make([]map[string]any, 0, len(inv.Projects))
	for _, p := range inv.Projects {
		projects = append(projects, map[string]any{
			"project":         p.Project,
			"workspace_bytes": p.Workspace.Bytes,
			"cache_bytes":     p.Cache.Bytes,
		})
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("storage/snapshot/%d", time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: "workspace", Type: "system"}},
		Type:       "storage.snapshot",
		Data: map[string]any{
			"root":             inv.Root,
			"disk_total_bytes": inv.Disk.TotalBytes,
			"disk_free_bytes":  inv.Disk.FreeBytes,
			"truncated":        inv.Truncated,
			"projects":         projects,
		},
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit storage snapshot event", "error", err)
	}
}

// startStorageSnapshotLoop refreshes the snapshot cache hourly and journals
// the footprint (docs/sandbox-storage.md §6; phase 1 — no deletions). The
// first walk runs at boot so the dashboard is warm from the start.
func startStorageSnapshotLoop(root string, cache *storageSnapshotCache, events sandboxProfileOutbox) {
	if root == "" || !storageSweeperEnabled() {
		return
	}
	go func() {
		snapshot := func() {
			inv, err := cache.get(context.Background(), root, true)
			if err != nil {
				slog.Warn("storage snapshot walk", "error", err)
				return
			}
			emitStorageSnapshotEvent(context.Background(), events, inv)
		}
		snapshot()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			snapshot()
		}
	}()
}
