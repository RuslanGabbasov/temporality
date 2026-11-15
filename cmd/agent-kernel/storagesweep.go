package main

// Manual sweep, retention policies and the background sweeper
// (docs/sandbox-storage.md §5.3 «Ручная уборка», §5.5 события, §6 Sweeper) —
// phase 2 of the storage plan. The journal is the single source of truth
// about deletions: every removed path is a storage.evicted row (capped so a
// 10k-file sweep cannot flood it), every run and every background cycle ends
// in storage.swept, and quota breaches speak as storage.quota_exceeded. The
// sweeper itself is an idempotent background trim of the kernel — never a
// Temporal workflow — and it stays off until an operator flips
// policy.enabled, because automatic deletion is the dangerous half of this
// feature.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/storage"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

const (
	// storagePolicyKey is the workspace_setting row that holds the retention
	// policy as JSON — docs §3.3: policies are installation settings, not code.
	storagePolicyKey = "storage.policy"
	// storageEvictedEventCap bounds the journal per sweep: the first N
	// deletions are journaled individually and the rest are counted into the
	// storage.swept summary («первые 100 событий + итоговые»), so the
	// summary always carries the full totals even when the detail is capped.
	storageEvictedEventCap = 100
	// storageSweepPlanBudget and storageSweepMaxScanned bound every planning
	// walk (docs §11): wall clock plus entry count, so a pathological tree
	// truncates the plan instead of pinning the kernel. A truncated plan is
	// partial but honest — the sweep is idempotent, the next run continues.
	storageSweepPlanBudget = 10 * time.Minute
	storageSweepMaxScanned = 5_000_000
	// storagePolicyMaxHours caps every hours field: it keeps the
	// time.Duration cutoff math from overflowing and no sane retention
	// outlives a century. Zero stays legal in the policy («0 = выкл»), but
	// an explicit sweep request must name a positive age — "everything
	// older than now" is never an implicit default.
	storagePolicyMaxHours = 24 * 365 * 100
	// auditStorageSwept is the access-audit action of a real (non-dry) manual
	// sweep — docs §5.3: «Аудит + событие», docs §4: «ручная уборка — только
	// operator и только с аудитом».
	auditStorageSwept = "storage.swept"
)

// storageSweepStore is the settings/audit surface the sweep routes need.
// *workspace.Store satisfies it; the interface keeps the handlers testable
// without a database, the same pattern as sandboxProfileStore.
type storageSweepStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
}

// storagePolicy is the retention policy (docs/sandbox-storage.md §3.3).
// Enabled gates only the background sweeper — manual sweeps are an explicit
// operator action and never consult it. CacheTTLHours and WorkspaceStaleHours
// are the age thresholds; zero disables that area's automatic cleaning, and
// QuotaBytes zero means no quota events.
type storagePolicy struct {
	Enabled             bool  `json:"enabled"`
	CacheTTLHours       int   `json:"cache_ttl_hours"`
	WorkspaceStaleHours int   `json:"workspace_stale_hours"`
	QuotaBytes          int64 `json:"quota_bytes"`
}

// defaultStoragePolicy is what GET answers before any record exists: auto-
// deletion off, cache retention at the documented 7 days (§3.3), workspace
// cleaning and quota off. The conservative defaults mean an installation
// that never opened the Storage page can never lose a file to the sweeper.
func defaultStoragePolicy() storagePolicy {
	return storagePolicy{
		Enabled:             false,
		CacheTTLHours:       168,
		WorkspaceStaleHours: 0,
		QuotaBytes:          0,
	}
}

// loadStoragePolicy reads the policy row; a missing or empty row is the
// default, a corrupt row is an error — callers must not delete files under a
// policy they could not parse (the sweeper skips the cycle, GET answers 500
// so the operator sees the damage and can PUT over it).
func loadStoragePolicy(ctx context.Context, ws storageSweepStore) (storagePolicy, error) {
	raw, err := ws.GetSetting(ctx, storagePolicyKey)
	if errors.Is(err, workspace.ErrNotFound) {
		return defaultStoragePolicy(), nil
	}
	if err != nil {
		return storagePolicy{}, err
	}
	if strings.TrimSpace(raw) == "" {
		return defaultStoragePolicy(), nil
	}
	var policy storagePolicy
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return storagePolicy{}, fmt.Errorf("decode %s: %w", storagePolicyKey, err)
	}
	return policy, nil
}

// policyAgeFor maps an area onto its retention field: the toolchain cache
// ages by CacheTTLHours, the workspace by WorkspaceStaleHours (0 = выкл).
func policyAgeFor(policy storagePolicy, area string) int {
	if area == storage.AreaCache {
		return policy.CacheTTLHours
	}
	return policy.WorkspaceStaleHours
}

// storageSweepRequest is the POST .../sweep body. OlderThanHours is a
// pointer so "absent" (use the per-area policy default) stays distinct from
// an explicit 0 (rejected — see storagePolicyMaxHours).
type storageSweepRequest struct {
	DryRun         bool     `json:"dry_run"`
	Areas          []string `json:"areas"`
	OlderThanHours *int     `json:"older_than_hours"`
	Paths          []string `json:"paths"`
}

// storageSweepPlanEntry is one row of the dry-run plan. Path is relative to
// the sandbox root ("cache/<project>/…", "projects/<project>/…") so cache
// and workspace rows never collide in one list and the journal can reuse the
// same rendering; the request's paths stay area-relative like the file
// routes' ?path=.
type storageSweepPlanEntry struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// storageSweepResponse is the contract the Storage page renders. Plan is a
// pointer so dry runs always carry a list (even an empty one) while real
// runs omit the key entirely — «plan только при dry_run».
type storageSweepResponse struct {
	Removed    int                      `json:"removed"`
	FreedBytes int64                    `json:"freed_bytes"`
	Plan       *[]storageSweepPlanEntry `json:"plan,omitempty"`
	DryRun     bool                     `json:"dry_run"`
}

// storageSweepTarget is one planned removal: the absolute host path the
// executor unlinks and the root-relative path plans and events report.
type storageSweepTarget struct {
	Abs   string
	Path  string
	Bytes int64
}

// storageSweepExecuteMu serializes real executions. Two overlapping sweeps
// would double-count bytes and journal the same paths twice — RemoveAll on a
// vanished path still succeeds, so the Lstat guard inside the executor is
// only honest when executions do not interleave. Dry runs take no lock: they
// delete nothing and a stale plan costs nothing.
var storageSweepExecuteMu sync.Mutex

// storageEventSeq disambiguates event ids within one nanosecond: a sweep can
// emit a hundred evicted rows back to back and the journal keys on the id.
var storageEventSeq atomic.Uint64

// emitStorageEvent is the shared journal write for all three sweep event
// types, shaped like emitStorageSnapshotEvent (storageapi.go).
func emitStorageEvent(ctx context.Context, events sandboxProfileOutbox, idPrefix, eventType string, pc observation.Context, data map[string]any) {
	if events == nil {
		return
	}
	item := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("%s/%d/%d", idPrefix, time.Now().UnixNano(), storageEventSeq.Add(1)),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: "workspace", Integration: "agent-kernel", Version: "1"},
		Context:    pc,
		Type:       eventType,
		Data:       data,
	}
	if err := events.Enqueue(ctx, item); err != nil {
		slog.Warn("emit storage event", "type", eventType, "error", err)
	}
}

func emitStorageEvictedEvent(ctx context.Context, events sandboxProfileOutbox, pc observation.Context, data map[string]any) {
	emitStorageEvent(ctx, events, "storage/evicted", "storage.evicted", pc, data)
}

func emitStorageSweptEvent(ctx context.Context, events sandboxProfileOutbox, pc observation.Context, data map[string]any) {
	emitStorageEvent(ctx, events, "storage/swept", "storage.swept", pc, data)
}

func emitStorageQuotaExceededEvent(ctx context.Context, events sandboxProfileOutbox, pc observation.Context, data map[string]any) {
	emitStorageEvent(ctx, events, "storage/quota-exceeded", "storage.quota_exceeded", pc, data)
}

// storageEviction names the policy context one deletion ran under.
type storageEviction struct {
	project string
	area    string
	policy  string // "manual" | "cache_ttl_hours" | "workspace_stale_hours"
	hours   int    // age threshold used (0 in selective mode)
}

// storageEvictionJournal caps storage.evicted per execution: the first
// storageEvictedEventCap deletions are journaled one by one, the rest only
// increment suppressed, which the storage.swept summary reports next to the
// logged count so no sweep silently loses its tail.
type storageEvictionJournal struct {
	events     sandboxProfileOutbox
	trigger    string // "manual" | "background"
	actor      observation.Actor
	logged     int
	suppressed int
}

func (j *storageEvictionJournal) record(ctx context.Context, ev storageEviction, targetPath string, bytes int64) {
	if j.events == nil {
		return
	}
	if j.logged >= storageEvictedEventCap {
		j.suppressed++
		return
	}
	emitStorageEvictedEvent(ctx, j.events, observation.Context{Project: ev.project, Actor: j.actor}, map[string]any{
		"project":          ev.project,
		"area":             ev.area,
		"path":             targetPath,
		"bytes":            bytes,
		"policy":           ev.policy,
		"trigger":          j.trigger,
		"older_than_hours": ev.hours,
	})
	j.logged++
}

// storageSweepRun is the policy context one execution runs under: which
// retention rule authorizes the deletions and where their events go.
type storageSweepRun struct {
	policy  string
	hours   int
	journal *storageEvictionJournal
}

// sweepWalkState bounds one planning walk: wall clock, entry count and the
// caller's context (docs §11). exceeded() flips and remembers truncated, so
// every nested loop unwinds on the same condition instead of re-checking
// three fields.
type sweepWalkState struct {
	ctx       context.Context
	deadline  time.Time
	scanned   int
	truncated bool
}

func (s *sweepWalkState) exceeded() bool {
	if s.truncated {
		return true
	}
	if s.ctx.Err() != nil || time.Now().After(s.deadline) || s.scanned >= storageSweepMaxScanned {
		s.truncated = true
		return true
	}
	return false
}

// sweepDisplayPath renders a target for plans and journal rows: relative to
// the sandbox root, so "cache/alpha/x" and "projects/alpha/x" never collide
// and the journal stays host-independent.
func sweepDisplayPath(area, project, rel string) string {
	areaRoot := "projects"
	if area == storage.AreaCache {
		areaRoot = "cache"
	}
	return path.Join(areaRoot, project, rel)
}

// storageSweepSkipFor returns the age-mode protection predicate for an area:
// uploads/ and inbox/ are the user's files and .git is never touched
// (docs §3.3), everything else in the workspace becomes fair game once past
// the cutoff. A protected entry also blocks collapsing any ancestor —
// RemoveAll on a stale parent would take the protected child with it — so
// the planner propagates "protected" upward and keeps such directories as
// walk-throughs instead of deletion units.
func storageSweepSkipFor(area string) func(rel string, isDir bool) bool {
	return func(rel string, isDir bool) bool {
		if isDir && path.Base(rel) == ".git" {
			return true
		}
		if area == storage.AreaWorkspace && !strings.Contains(rel, "/") && (rel == "uploads" || rel == "inbox") {
			return true
		}
		return false
	}
}

// planAgeTargets walks the area and selects what is older than cutoff: a
// file joins when its own mtime is past, a directory collapses into a single
// target when everything inside it (newest mtime wins, directory mtimes
// included) is past — so a cold toolchain cache plans as one RemoveAll
// instead of 50k files, while a mixed directory yields only its stale
// children. Symlinks and special entries are never selected and they block
// collapsing their ancestors: age mode deletes regular files and
// directories only (docs §4), and RemoveAll on a collapsed parent would
// take the unselectable entry with it. The same blocking applies to the
// skip-protected paths (uploads/, inbox/, .git/). A missing area (the
// project never ran) plans nothing; an unreadable area root fails the walk.
func planAgeTargets(project, area, base string, cutoff time.Time, skip func(rel string, isDir bool) bool, state *sweepWalkState) ([]storageSweepTarget, error) {
	var walk func(dir, rel string) (bytes int64, newest time.Time, targets []storageSweepTarget, blocked bool, err error)
	walk = func(dir, rel string) (int64, time.Time, []storageSweepTarget, bool, error) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return 0, time.Time{}, nil, false, nil
			}
			if rel == "" {
				return 0, time.Time{}, nil, false, err // the area root must be readable
			}
			slog.Warn("storage sweep read dir failed", "dir", rel, "error", err)
			return 0, time.Time{}, nil, false, nil
		}
		var (
			total   int64
			newest  time.Time
			targets []storageSweepTarget
			blocked bool
		)
		for _, entry := range entries {
			if state.exceeded() {
				break
			}
			state.scanned++
			childRel := path.Join(rel, entry.Name())
			if skip(childRel, entry.IsDir()) {
				blocked = true // user data in this subtree: ancestors may not collapse through it
				continue
			}
			abs := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				info, infoErr := entry.Info()
				if infoErr != nil {
					continue
				}
				subBytes, subNewest, subTargets, subBlocked, walkErr := walk(abs, childRel)
				if walkErr != nil {
					return 0, time.Time{}, nil, false, walkErr
				}
				total += subBytes
				childNewest := subNewest
				if info.ModTime().After(childNewest) {
					childNewest = info.ModTime()
				}
				if childNewest.After(newest) {
					newest = childNewest
				}
				blocked = blocked || subBlocked
				if childNewest.Before(cutoff) && !subBlocked {
					// Wholly stale and clean subtree: one target for the directory.
					targets = append(targets, storageSweepTarget{
						Abs:   abs,
						Path:  sweepDisplayPath(area, project, childRel),
						Bytes: subBytes,
					})
					continue
				}
				targets = append(targets, subTargets...)
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			if !info.Mode().IsRegular() {
				// Symlinks, sockets, devices: age mode never deletes them, and
				// a collapsed parent would — so keep the ancestors walk-through.
				blocked = true
				continue
			}
			total += info.Size()
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
			if info.ModTime().Before(cutoff) {
				targets = append(targets, storageSweepTarget{
					Abs:   abs,
					Path:  sweepDisplayPath(area, project, childRel),
					Bytes: info.Size(),
				})
			}
		}
		return total, newest, targets, blocked, nil
	}
	// The area root's own blocked flag is discarded: the root is never a
	// target, so a protected top-level entry only means "skip it", which the
	// loop above already did.
	_, _, targets, _, err := walk(base, "")
	if err != nil {
		return nil, err
	}
	return targets, nil
}

// measureDir sums the regular-file bytes under abs for an explicit path
// selection. WalkDir does not follow symlinks, so a link inside the chosen
// directory contributes nothing here — RemoveAll will unlink it without
// touching its target, and freed_bytes stays an honest count of file data.
func measureDir(abs string, state *sweepWalkState) int64 {
	var total int64
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // vanished mid-measure: count what is there
		}
		if state.exceeded() {
			return fs.SkipAll
		}
		state.scanned++
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return nil // vanished, or a symlink/special entry: not file data
		}
		total += info.Size()
		return nil
	})
	return total
}

// planPathTargets resolves the operator's explicit selection: each path is
// anchored in each requested area under the same guard as the file routes
// (storage.ResolveAreaPath does Clean + escape refusal, and the strict
// prefix check below keeps the target under <root>/projects/<id>/ or
// <root>/cache/<id>/ — never the area root itself). Missing targets are
// skipped so a re-run after a partial sweep stays idempotent; a malformed
// or escaping path fails the whole request — that is a client bug worth a
// 400, not a silent skip. Explicit paths bypass the age-mode skip list:
// uploads/ and .git/ are visible in the tree, and naming one is the
// operator's audited intent.
func planPathTargets(root, project, area, base string, paths []string, state *sweepWalkState) ([]storageSweepTarget, error) {
	var targets []storageSweepTarget
	seen := make(map[string]bool, len(paths))
	for _, raw := range paths {
		if state.exceeded() {
			break
		}
		abs, err := storage.ResolveAreaPath(root, project, area, raw)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(abs, base+string(filepath.Separator)) {
			return nil, fmt.Errorf("path %q addresses the area root; sweep only what is under it", raw)
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		info, err := os.Lstat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				slog.Debug("storage sweep path missing, skipping", "area", area, "path", raw)
				continue
			}
			slog.Warn("storage sweep stat failed", "area", area, "path", raw, "error", err)
			continue
		}
		var bytes int64
		if info.IsDir() {
			bytes = measureDir(abs, state)
		} else {
			bytes = info.Size()
		}
		rel := path.Clean(filepath.ToSlash(raw))
		targets = append(targets, storageSweepTarget{
			Abs:   abs,
			Path:  sweepDisplayPath(area, project, rel),
			Bytes: bytes,
		})
	}
	return targets, nil
}

// executeStorageSweep removes the planned targets and journals each success
// through the capped journal. The Lstat guard keeps overlapping runs honest:
// a target another sweep already took is skipped, not counted and not
// journaled, because RemoveAll on a missing path would otherwise report a
// deletion that freed nothing.
func executeStorageSweep(ctx context.Context, project, area string, run *storageSweepRun, targets []storageSweepTarget) (removed int, freed int64) {
	storageSweepExecuteMu.Lock()
	defer storageSweepExecuteMu.Unlock()
	for _, target := range targets {
		if _, err := os.Lstat(target.Abs); err != nil {
			if os.IsNotExist(err) {
				slog.Debug("storage sweep target already gone", "path", target.Path)
				continue
			}
			slog.Warn("storage sweep stat failed", "path", target.Path, "error", err)
			continue
		}
		if err := os.RemoveAll(target.Abs); err != nil {
			slog.Warn("storage sweep remove failed", "path", target.Path, "error", err)
			continue
		}
		removed++
		freed += target.Bytes
		run.journal.record(ctx, storageEviction{project: project, area: area, policy: run.policy, hours: run.hours}, target.Path, target.Bytes)
		slog.Debug("storage sweep removed", "path", target.Path, "bytes", target.Bytes)
	}
	return removed, freed
}

// registerStorageSweep mounts the phase-2 sweep surface under the same
// /v1/workspace/storage prefix as the inventory and file routes: the
// per-project manual sweep (operator, audited) and the installation-wide
// retention policy (GET reader, PUT writer).
func registerStorageSweep(mux *http.ServeMux, gate *controlplane.Gate, visibleProjects func(*http.Request) (map[string][]string, error), root string, ws storageSweepStore, events sandboxProfileOutbox) {
	mux.HandleFunc("POST /v1/workspace/storage/projects/{projectID}/sweep", func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		if root == "" {
			writeError(w, 503, errors.New("storage sweep requires KERNEL_SANDBOX_ROOT to be configured"))
			return
		}
		if ws == nil {
			writeError(w, 503, errors.New("storage sweep requires the workspace store"))
			return
		}
		if events == nil {
			// The journal is the single source of truth about deletions
			// (docs §4): without an outbox every storage.evicted row would
			// be silently lost, so refuse to delete instead.
			writeError(w, 503, errors.New("storage sweep requires the event journal"))
			return
		}
		project := r.PathValue("projectID")
		if err := storage.ValidProjectID(project); err != nil {
			writeError(w, 400, err)
			return
		}
		// The file-route guard ladder, one role higher: a sweep deletes more
		// than a single file, so operator + project scope, then visibility —
		// invisible and unknown projects answer the same 404.
		if !gate.Allow(w, r, controlplane.RoleOperator, project) {
			return
		}
		if visibleProjects != nil {
			visible, err := visibleProjects(r)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			if _, known := visible[project]; !known {
				writeError(w, 404, fmt.Errorf("project %q not found", project))
				return
			}
		}
		var req storageSweepRequest
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, 400, err)
			return
		}
		if len(req.Areas) == 0 {
			writeError(w, 422, errors.New("areas is required (cache, workspace or both)"))
			return
		}
		areas := make([]string, 0, len(req.Areas))
		seenArea := make(map[string]bool, len(req.Areas))
		for _, area := range req.Areas {
			if err := storage.ValidArea(area); err != nil {
				writeError(w, 400, err)
				return
			}
			if !seenArea[area] {
				seenArea[area] = true
				areas = append(areas, area)
			}
		}
		if req.OlderThanHours != nil && (*req.OlderThanHours < 1 || *req.OlderThanHours > storagePolicyMaxHours) {
			writeError(w, 422, fmt.Errorf("older_than_hours must be between 1 and %d", storagePolicyMaxHours))
			return
		}
		mode := "age"
		if len(req.Paths) > 0 {
			// Explicit selection: only the named paths go, age is not
			// consulted (docs §5.3 contract for the multi-select UI).
			mode = "selective"
		}
		// The policy is needed only for age mode without an explicit age —
		// a corrupt row then fails the request instead of guessing.
		var policy storagePolicy
		if mode == "age" && req.OlderThanHours == nil {
			loaded, err := loadStoragePolicy(r.Context(), ws)
			if err != nil {
				writeError(w, 500, err)
				return
			}
			policy = loaded
		}
		journal := &storageEvictionJournal{
			events:  events,
			trigger: "manual",
			actor:   actorFromRequest(r),
		}
		state := &sweepWalkState{ctx: r.Context(), deadline: time.Now().Add(storageSweepPlanBudget)}
		type areaPlan struct {
			area    string
			hours   int
			targets []storageSweepTarget
		}
		plans := make([]areaPlan, 0, len(areas))
		for _, area := range areas {
			base, err := storage.ResolveAreaPath(root, project, area, "")
			if err != nil {
				writeError(w, 500, err)
				return
			}
			plan := areaPlan{area: area}
			if mode == "selective" {
				plan.targets, err = planPathTargets(root, project, area, base, req.Paths, state)
				if err != nil {
					writeError(w, 400, err)
					return
				}
			} else {
				if req.OlderThanHours != nil {
					plan.hours = *req.OlderThanHours
				} else {
					plan.hours = policyAgeFor(policy, area)
				}
				if plan.hours < 1 {
					// 0 = выкл: the policy has no retention for this area,
					// and "everything older than now" is never implicit.
					slog.Info("storage sweep area skipped: retention disabled in policy", "project", project, "area", area)
					continue
				}
				cutoff := time.Now().Add(-time.Duration(plan.hours) * time.Hour)
				plan.targets, err = planAgeTargets(project, area, base, cutoff, storageSweepSkipFor(area), state)
				if err != nil {
					writeError(w, 500, err)
					return
				}
			}
			sort.Slice(plan.targets, func(i, j int) bool { return plan.targets[i].Path < plan.targets[j].Path })
			slog.Info("storage sweep planned", "project", project, "area", area, "mode", mode, "older_than_hours", plan.hours, "targets", len(plan.targets))
			plans = append(plans, plan)
		}
		if state.truncated {
			slog.Warn("storage sweep plan truncated by budget", "project", project, "scanned", state.scanned)
		}
		sweptData := func(dryRun bool, removed int, freed int64) map[string]any {
			return map[string]any{
				"trigger":            "manual",
				"project":            project,
				"areas":              areas,
				"mode":               mode,
				"dry_run":            dryRun,
				"removed":            removed,
				"freed_bytes":        freed,
				"duration_ms":        time.Since(started).Milliseconds(),
				"evicted_logged":     journal.logged,
				"evicted_suppressed": journal.suppressed,
				"truncated":          state.truncated,
			}
		}
		if req.DryRun {
			// The plan IS the answer: what would go, how much it frees.
			// No lock, no deletions, no evicted rows — but the run still
			// leaves a swept summary so dry runs are observable too.
			removed := 0
			var freed int64
			entries := make([]storageSweepPlanEntry, 0)
			for _, plan := range plans {
				for _, target := range plan.targets {
					removed++
					freed += target.Bytes
					entries = append(entries, storageSweepPlanEntry{Path: target.Path, Bytes: target.Bytes})
				}
			}
			emitStorageSweptEvent(r.Context(), events, observation.Context{Project: project, Actor: actorFromRequest(r)}, sweptData(true, removed, freed))
			slog.Info("storage sweep dry run", "project", project, "mode", mode, "targets", removed, "freed_bytes", freed, "duration_ms", time.Since(started).Milliseconds())
			writeJSON(w, 200, storageSweepResponse{Removed: removed, FreedBytes: freed, Plan: &entries, DryRun: true})
			return
		}
		removed := 0
		var freed int64
		for _, plan := range plans {
			if len(plan.targets) == 0 {
				continue
			}
			run := &storageSweepRun{policy: "manual", hours: plan.hours, journal: journal}
			done, bytes := executeStorageSweep(r.Context(), project, plan.area, run, plan.targets)
			removed += done
			freed += bytes
		}
		emitStorageSweptEvent(r.Context(), events, observation.Context{Project: project, Actor: actorFromRequest(r)}, sweptData(false, removed, freed))
		// Best-effort audit: the journal already carries the deletions, and a
		// failed audit row must not fail a sweep that already happened.
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), auditStorageSwept, "storage", project, map[string]any{
			"areas":              areas,
			"mode":               mode,
			"older_than_hours":   req.OlderThanHours,
			"paths":              len(req.Paths),
			"removed":            removed,
			"freed_bytes":        freed,
			"evicted_suppressed": journal.suppressed,
		})
		slog.Info("storage sweep done", "project", project, "mode", mode, "removed", removed, "freed_bytes", freed, "evicted_logged", journal.logged, "evicted_suppressed", journal.suppressed, "duration_ms", time.Since(started).Milliseconds())
		writeJSON(w, 200, storageSweepResponse{Removed: removed, FreedBytes: freed, DryRun: false})
	})

	// Retention policy (docs §3.3): installation settings in
	// workspace_setting. Reading needs only the storage page's reader role;
	// writing needs a writer because enabled=true is what lets the
	// background sweeper start deleting files.
	mux.HandleFunc("GET /v1/workspace/storage/policies", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		if ws == nil {
			writeError(w, 503, errors.New("storage policy store is unavailable"))
			return
		}
		policy, err := loadStoragePolicy(r.Context(), ws)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, policy)
	})
	mux.HandleFunc("PUT /v1/workspace/storage/policies", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleWriter) {
			return
		}
		if ws == nil {
			writeError(w, 503, errors.New("storage policy store is unavailable"))
			return
		}
		var policy storagePolicy
		if err := decodeJSON(w, r, &policy); err != nil {
			writeError(w, 400, err)
			return
		}
		// Zero stays legal («0 = выкл»); negatives are nonsense and hours
		// past the cap would overflow the cutoff arithmetic.
		if policy.CacheTTLHours < 0 || policy.CacheTTLHours > storagePolicyMaxHours ||
			policy.WorkspaceStaleHours < 0 || policy.WorkspaceStaleHours > storagePolicyMaxHours {
			writeError(w, 422, fmt.Errorf("cache_ttl_hours and workspace_stale_hours must be between 0 and %d", storagePolicyMaxHours))
			return
		}
		if policy.QuotaBytes < 0 {
			writeError(w, 422, errors.New("quota_bytes must be >= 0"))
			return
		}
		raw, err := json.Marshal(policy)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if err := ws.SetSetting(r.Context(), storagePolicyKey, string(raw)); err != nil {
			writeError(w, 500, err)
			return
		}
		_ = ws.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditSettingChanged, "setting", storagePolicyKey, map[string]any{
			"enabled":               policy.Enabled,
			"cache_ttl_hours":       policy.CacheTTLHours,
			"workspace_stale_hours": policy.WorkspaceStaleHours,
			"quota_bytes":           policy.QuotaBytes,
		})
		slog.Info("storage policy updated", "actor", requestUserID(r), "enabled", policy.Enabled, "cache_ttl_hours", policy.CacheTTLHours, "workspace_stale_hours", policy.WorkspaceStaleHours, "quota_bytes", policy.QuotaBytes)
		writeJSON(w, 200, policy)
	})
}

// startStorageSweeper runs the hourly retention cycle (docs §6): the same
// env gate as the snapshot loop, re-gated by policy.enabled inside every
// cycle — auto-deletion stays off until an operator turns it on. Unlike the
// snapshot loop's boot walk (which exists to warm the dashboard), the first
// sweep waits one full tick: a fresh boot is the worst moment to start
// unlinking, and an operator who wants bytes freed now has the manual
// endpoint.
func startStorageSweeper(root string, cache *storageSnapshotCache, ws storageSweepStore, events sandboxProfileOutbox) {
	if root == "" || ws == nil || !storageSweeperEnabled() {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			runStorageSweepCycle(context.Background(), root, cache, ws, events)
		}
	}()
}

// runStorageSweepCycle is one background pass in the documented order:
// snapshot → quotas (storage.quota_exceeded) → cache age → workspace age →
// storage.swept. Every project failure is logged and skipped so one broken
// tree cannot stall the rest of the cycle (docs §6), and the cycle journals
// its summary even when it removed nothing — that is how an operator sees
// the sweeper is alive.
func runStorageSweepCycle(ctx context.Context, root string, cache *storageSnapshotCache, ws storageSweepStore, events sandboxProfileOutbox) {
	if root == "" || ws == nil || cache == nil || events == nil {
		// Unattended deletion without a journal would break the docs §4
		// invariant («ничего не удаляется молча») — a nil outbox is a
		// wiring bug, and the safe reaction is to delete nothing.
		return
	}
	policy, err := loadStoragePolicy(ctx, ws)
	if err != nil {
		slog.Warn("storage sweeper policy load", "error", err)
		return
	}
	if !policy.Enabled {
		slog.Debug("storage sweeper idle: policy disabled")
		return
	}
	started := time.Now()
	// force=false: the hourly snapshot loop keeps the cache fresh and §6
	// says quota decisions read the snapshot cache — a second full walk per
	// hour over millions of files would double the IO for no new information.
	inv, err := cache.get(ctx, root, false)
	if err != nil {
		slog.Warn("storage sweeper snapshot", "error", err)
		return
	}
	state := &sweepWalkState{ctx: ctx, deadline: started.Add(storageSweepPlanBudget)}
	journal := &storageEvictionJournal{
		events:  events,
		trigger: "background",
		actor:   observation.Actor{ID: "workspace", Type: "system"},
	}
	quotaExceeded := 0
	if policy.QuotaBytes > 0 {
		for _, p := range inv.Projects {
			used := p.Workspace.Bytes + p.Cache.Bytes
			if used <= policy.QuotaBytes {
				continue
			}
			quotaExceeded++
			slog.Warn("storage quota exceeded", "project", p.Project, "used_bytes", used, "quota_bytes", policy.QuotaBytes)
			emitStorageQuotaExceededEvent(ctx, events, observation.Context{Project: p.Project, Actor: journal.actor}, map[string]any{
				"project":         p.Project,
				"quota_bytes":     policy.QuotaBytes,
				"used_bytes":      used,
				"workspace_bytes": p.Workspace.Bytes,
				"cache_bytes":     p.Cache.Bytes,
			})
		}
	}
	removed := 0
	var freed int64
	sweep := func(area string, hours int, policyName string) {
		if hours < 1 {
			return // 0 = выкл for this area
		}
		cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
		for _, p := range inv.Projects {
			run := &storageSweepRun{policy: policyName, hours: hours, journal: journal}
			done, bytes := sweepProjectArea(ctx, root, p.Project, area, cutoff, run, storageSweepSkipFor(area), state)
			removed += done
			freed += bytes
		}
	}
	sweep(storage.AreaCache, policy.CacheTTLHours, "cache_ttl_hours")
	sweep(storage.AreaWorkspace, policy.WorkspaceStaleHours, "workspace_stale_hours")
	emitStorageSweptEvent(ctx, events, observation.Context{Actor: journal.actor}, map[string]any{
		"trigger":            "background",
		"dry_run":            false,
		"removed":            removed,
		"freed_bytes":        freed,
		"duration_ms":        time.Since(started).Milliseconds(),
		"evicted_logged":     journal.logged,
		"evicted_suppressed": journal.suppressed,
		"quota_exceeded":     quotaExceeded,
		"truncated":          state.truncated,
	})
	slog.Info("storage sweeper cycle done", "removed", removed, "freed_bytes", freed, "quota_exceeded", quotaExceeded, "evicted_logged", journal.logged, "evicted_suppressed", journal.suppressed, "duration_ms", time.Since(started).Milliseconds(), "truncated", state.truncated)
}

// sweepProjectArea plans and executes one project's area sweep for the
// background cycle. A missing area (the project never ran there) is a no-op
// and a failed plan is logged and skipped — the cycle must keep going.
func sweepProjectArea(ctx context.Context, root, project, area string, cutoff time.Time, run *storageSweepRun, skip func(rel string, isDir bool) bool, state *sweepWalkState) (int, int64) {
	base, err := storage.ResolveAreaPath(root, project, area, "")
	if err != nil {
		slog.Warn("storage sweep area path", "project", project, "area", area, "error", err)
		return 0, 0
	}
	targets, err := planAgeTargets(project, area, base, cutoff, skip, state)
	if err != nil {
		slog.Warn("storage sweep plan failed", "project", project, "area", area, "error", err)
		return 0, 0
	}
	if len(targets) == 0 {
		return 0, 0
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Path < targets[j].Path })
	removed, freed := executeStorageSweep(ctx, project, area, run, targets)
	if removed > 0 {
		slog.Info("storage sweep area cleaned", "project", project, "area", area, "removed", removed, "freed_bytes", freed)
	}
	return removed, freed
}
