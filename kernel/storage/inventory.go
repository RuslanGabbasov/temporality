// Package storage inventories the agent sandbox root (docs/sandbox-storage.md):
// per-project workspace and cache footprints, project trees and host health.
// Phase 1 is strictly read-only — nothing here deletes or mutates the
// filesystem; eviction arrives with phase 2 and must keep the event contract.
package storage

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TopPath is a size-ranked path inside a project workspace, relative to the
// workspace root and at most TopDepth components deep.
type TopPath struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// AreaUsage aggregates one storage area (workspace or cache).
type AreaUsage struct {
	Bytes int64     `json:"bytes"`
	Files int64     `json:"files"`
	ModAt time.Time `json:"mod_at"` // latest file mtime seen; zero when empty
}

// ProjectUsage is one project's footprint across both areas.
type ProjectUsage struct {
	Project   string
	Workspace AreaUsage
	Cache     AreaUsage
	TopPaths  []TopPath
}

// DiskUsage is statfs-level information for one path.
type DiskUsage struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}

// Inventory is a full read-only snapshot of the sandbox root.
type Inventory struct {
	GeneratedAt time.Time
	Root        string
	Disk        DiskUsage
	Projects    []ProjectUsage
	Truncated   bool // the walk hit its time budget; the numbers are partial
}

// WalkOptions bounds the inventory walk: TopDepth limits top-path bucketing,
// TopN caps the per-project top-path list, Budget bounds wall time.
type WalkOptions struct {
	TopDepth int
	TopN     int
	Budget   time.Duration
}

func (o WalkOptions) withDefaults() WalkOptions {
	if o.TopDepth <= 0 {
		o.TopDepth = 3
	}
	if o.TopN <= 0 {
		o.TopN = 5
	}
	if o.Budget <= 0 {
		o.Budget = 60 * time.Second
	}
	return o
}

// Snapshot walks <root>/projects/* and <root>/cache/* and aggregates sizes,
// file counts and latest mtimes per project. A missing or non-directory root
// is an error; missing areas simply yield no projects. The walk respects ctx
// cancellation and its time budget — exceeding either marks Truncated instead
// of failing, so operators still get partial numbers.
func Snapshot(ctx context.Context, root string, opts WalkOptions) (*Inventory, error) {
	opts = opts.withDefaults()
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fs.ErrInvalid
	}
	inv := &Inventory{
		GeneratedAt: time.Now().UTC(),
		Root:        abs,
	}
	if inv.Disk, err = diskUsage(abs); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(opts.Budget)
	names := areaProjectNames(abs)
	for _, name := range names {
		usage := ProjectUsage{Project: name}
		buckets := map[string]int64{}
		usage.Workspace = walkArea(ctx, filepath.Join(abs, "projects", name), buckets, opts.TopDepth, deadline, &inv.Truncated)
		usage.Cache = walkArea(ctx, filepath.Join(abs, "cache", name), nil, 0, deadline, &inv.Truncated)
		usage.TopPaths = topPaths(buckets, opts.TopN)
		inv.Projects = append(inv.Projects, usage)
	}
	return inv, nil
}

// areaProjectNames unions the directory names under projects/ and cache/ —
// a project may have only one of the two areas on disk.
func areaProjectNames(root string) []string {
	seen := map[string]bool{}
	for _, area := range []string{"projects", "cache"} {
		entries, err := os.ReadDir(filepath.Join(root, area))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				seen[e.Name()] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// walkArea aggregates one directory tree. When buckets is non-nil, every
// regular file also feeds the top-path buckets at each ancestor prefix up to
// topDepth components. Missing dir is a zero usage, not an error.
func walkArea(ctx context.Context, dir string, buckets map[string]int64, topDepth int, deadline time.Time, truncated *bool) AreaUsage {
	var usage AreaUsage
	_, err := os.Stat(dir)
	if err != nil {
		return usage
	}
	checks := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable entries (permissions, races with cleanup) must not
			// fail the whole inventory — skip and keep counting the rest.
			return nil
		}
		if checks++; checks%64 == 0 {
			if ctx.Err() != nil || time.Now().After(deadline) {
				*truncated = true
				return fs.SkipAll
			}
		}
		if d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		usage.Bytes += info.Size()
		usage.Files++
		if info.ModTime().After(usage.ModAt) {
			usage.ModAt = info.ModTime()
		}
		if buckets != nil {
			if rel, relErr := filepath.Rel(dir, path); relErr == nil {
				rel = filepath.ToSlash(rel)
				for depth := 1; depth <= topDepth; depth++ {
					prefix := prefixAt(rel, depth)
					if prefix == "" {
						break
					}
					buckets[prefix] += info.Size()
				}
			}
		}
		return nil
	})
	return usage
}

// prefixAt returns the first depth slash-separated components, or "" when the
// path has fewer components.
func prefixAt(rel string, depth int) string {
	if rel == "" {
		return ""
	}
	parts := strings.Split(rel, "/")
	if depth > len(parts) {
		return ""
	}
	return strings.Join(parts[:depth], "/")
}

// topPaths picks the largest buckets, skipping nested descendants of already
// selected entries so the list never overlaps (a child of a reported dir adds
// no information and crowds out other consumers of space).
func topPaths(buckets map[string]int64, topN int) []TopPath {
	type kv struct {
		path  string
		bytes int64
	}
	flat := make([]kv, 0, len(buckets))
	for p, b := range buckets {
		flat = append(flat, kv{p, b})
	}
	sort.Slice(flat, func(i, j int) bool {
		if flat[i].bytes != flat[j].bytes {
			return flat[i].bytes > flat[j].bytes
		}
		return flat[i].path < flat[j].path
	})
	var out []TopPath
	for _, item := range flat {
		if len(out) >= topN {
			break
		}
		nested := false
		for _, picked := range out {
			if strings.HasPrefix(item.path, picked.Path+"/") {
				nested = true
				break
			}
		}
		if nested {
			continue
		}
		out = append(out, TopPath{Path: item.path, Bytes: item.bytes})
	}
	return out
}
