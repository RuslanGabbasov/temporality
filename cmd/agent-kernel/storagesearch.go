package main

// Server-side search over one project's storage areas
// (docs/sandbox-storage.md §5, the «Storage» page): the browser loads
// directories lazily, so a 3.1M-file root has no screen that could search
// itself. The search is a lazy walk bounded by depth, wall time and entry
// counts instead of an index — materializing millions of paths in a snapshot
// would cost more memory than the walk costs latency, and §11 requires every
// filesystem walk under KERNEL_SANDBOX_ROOT to carry limits.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/storage"
)

const (
	// storageSearchBudget bounds one query's walk. The response contract is
	// ≤2s, so the walk stops half a second early and answers with whatever it
	// matched (truncated:true) — a partial page beats a gateway timeout.
	storageSearchBudget = 1500 * time.Millisecond
	// storageSearchDefaultLimit and storageSearchMaxLimit bound ?limit=:
	// the default keeps responses small, the cap keeps one query from
	// serializing an unbounded row set into JSON.
	storageSearchDefaultLimit = 100
	storageSearchMaxLimit     = 500
	// storageSearchMinQuery is the shortest useful q: one-character queries
	// match most of the tree and answer nothing an operator can act on.
	storageSearchMinQuery = 2
	// storageSearchMaxDepth is §11's mandatory depth bound. It reaches deeper
	// than the tree route's 8 because search must find files under
	// node_modules-style nesting, yet stays finite so pathological trees
	// cannot recurse forever. Depth defines the search scope — like the tree
	// route's depth parameter, hitting it is not a truncation.
	storageSearchMaxDepth = 32
	// storageSearchMaxScanned is the deterministic counterpart to the wall
	// clock: the budget alone would let a fast NVMe scan millions of entries
	// per query, while this fixed ceiling bounds the filesystem work no
	// matter how quick the host is.
	storageSearchMaxScanned = 500_000
	// storageSearchMaxMatches caps collected rows: a broad query over 3.1M
	// paths could otherwise allocate hundreds of MB of results inside the
	// budget. Hitting the cap truncates the answer.
	storageSearchMaxMatches = 10_000
	// The three documented ?sort= values.
	storageSearchSortRecent = "recent"
	storageSearchSortSize   = "size"
	storageSearchSortName   = "name"
)

// storageSearchResult is one hit. Path stays relative to its area root —
// the exact shape the dir/file routes accept as ?path= — so the UI can turn
// a row into a browser link without re-parsing anything, and the area field
// disambiguates the two roots (the tree route prefixes cache paths instead;
// here the contract carries the area explicitly).
type storageSearchResult struct {
	Path  string    `json:"path"`
	Area  string    `json:"area"`
	Bytes int64     `json:"bytes"`
	ModAt time.Time `json:"mod_at"`
	Dir   bool      `json:"dir"`
}

// storageSearchView is the search response (docs/sandbox-storage.md §5).
// truncated marks a walk stopped by the budget or the entry/match caps: the
// rows are not the full match set and the UI must offer a narrower query
// instead of claiming completeness. Reaching ?limit= is ordinary pagination,
// not truncation — the walk still covered the area.
type storageSearchView struct {
	Results   []storageSearchResult `json:"results"`
	Truncated bool                  `json:"truncated"`
	ElapsedMs int64                 `json:"elapsed_ms"`
}

// registerStorageSearch mounts GET .../projects/{projectID}/search under the
// same /v1/workspace/storage prefix as the inventory and file routes. The
// deps mirror registerStorageFileRoutes minus the audit store: search only
// reads, so there is nothing to journal.
func registerStorageSearch(mux *http.ServeMux, gate *controlplane.Gate, visibleProjects func(*http.Request) (map[string][]string, error), root string) {
	mux.HandleFunc("GET /v1/workspace/storage/projects/{projectID}/search", func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		// The same guard ladder as the file routes: a configured root, a
		// project id that cannot escape it, the reader role scoped to that
		// project, then visibility — invisible and unknown projects answer
		// the same 404 so the storage layout never leaks which ids exist.
		if root == "" {
			writeError(w, 503, errors.New("storage search requires KERNEL_SANDBOX_ROOT to be configured"))
			return
		}
		project := r.PathValue("projectID")
		if err := storage.ValidProjectID(project); err != nil {
			writeError(w, 400, err)
			return
		}
		if !gate.Allow(w, r, controlplane.RoleReader, project) {
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
		// q: at least two runes after trimming — a shorter query matches
		// most of the tree, which is exactly the storm this route exists to
		// avoid (422, per the contract).
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if utf8.RuneCountInString(query) < storageSearchMinQuery {
			writeError(w, 422, fmt.Errorf("q must be at least %d characters", storageSearchMinQuery))
			return
		}
		// area pins one root; empty searches both, which is what the Storage
		// page needs before the user picks a tab.
		area := r.URL.Query().Get("area")
		if area != "" {
			if err := storage.ValidArea(area); err != nil {
				writeError(w, 400, err)
				return
			}
		}
		// An unknown sort answers the default instead of an error — the tree
		// route's depth parameter degrades the same lenient way, and only the
		// three documented values ever come from the UI.
		sortBy := r.URL.Query().Get("sort")
		switch sortBy {
		case "", storageSearchSortRecent, storageSearchSortSize, storageSearchSortName:
		default:
			sortBy = storageSearchSortRecent
		}
		limit := storageSearchDefaultLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > storageSearchMaxLimit {
			limit = storageSearchMaxLimit
		}
		// One budget spans both areas: stopping in the first must not reset
		// the clock for the second.
		ctx, cancel := context.WithTimeout(r.Context(), storageSearchBudget)
		defer cancel()
		walk := &storageSearchWalk{
			query:      query,
			queryLower: strings.ToLower(query),
			hits:       make([]storageSearchResult, 0, limit),
		}
		areas := []string{storage.AreaWorkspace, storage.AreaCache}
		if area != "" {
			areas = []string{area}
		}
		truncated := false
		for _, current := range areas {
			base, err := storage.ResolveAreaPath(root, project, current, "")
			if err != nil {
				// Unreachable — project id and area are validated above; a
				// failure here would silently search nothing, so fail loudly.
				writeError(w, 500, err)
				return
			}
			if !walk.area(ctx, base, current) {
				truncated = true
				break
			}
		}
		results := walk.hits
		sortStorageSearchResults(results, sortBy)
		if len(results) > limit {
			results = results[:limit]
		}
		writeJSON(w, 200, storageSearchView{
			Results:   results,
			Truncated: truncated,
			ElapsedMs: time.Since(started).Milliseconds(),
		})
	})
}

// storageSearchWalk carries state across both areas: matched rows, the entry
// counter behind the scanned cap, and the query the rows must contain.
type storageSearchWalk struct {
	query      string
	queryLower string
	hits       []storageSearchResult
	scanned    int
}

// area walks one area's tree and appends matching rows in walk order (the
// caller walks workspace first, so cross-area ties stay stable). It reports
// false when the shared budget or the entry/match caps stopped the walk —
// the caller then reports truncated and skips the remaining area, whose rows
// under an exhausted budget would be arbitrary. Unreadable entries are
// skipped instead of failing the query: races with the sweeper must not 500
// a search.
func (w *storageSearchWalk) area(ctx context.Context, base, area string) bool {
	deadline, _ := ctx.Deadline() // the WithTimeout budget above always sets one
	complete := true
	_ = filepath.WalkDir(base, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			// A missing area (the project never ran) or an entry that
			// vanished mid-walk is a normal empty state, not a failure.
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) ||
			w.scanned >= storageSearchMaxScanned || len(w.hits) >= storageSearchMaxMatches {
			complete = false
			return fs.SkipAll
		}
		w.scanned++
		rel, relErr := filepath.Rel(base, abs)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil // the area root itself is not a searchable row
		}
		// §11's depth bound: subtrees past it are outside the search scope,
		// the same way the tree route's depth parameter cuts rendering.
		if d.IsDir() && strings.Count(rel, "/")+1 >= storageSearchMaxDepth {
			return fs.SkipDir
		}
		// The relative path contains the file name, so one check satisfies
		// both contract clauses (name match and path match). The exact-case
		// pass first spares the lowering allocation for verbatim hits.
		if !strings.Contains(rel, w.query) && !strings.Contains(strings.ToLower(rel), w.queryLower) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		var bytes int64
		if !d.IsDir() {
			// Directories report no size — dir-listing rows show none
			// either, and search rows render through the same components.
			bytes = info.Size()
		}
		w.hits = append(w.hits, storageSearchResult{
			Path:  rel,
			Area:  area,
			Bytes: bytes,
			ModAt: info.ModTime().UTC(),
			Dir:   d.IsDir(),
		})
		return nil
	})
	return complete
}

// sortStorageSearchResults orders rows per the contract: recent by mtime
// desc, size by bytes desc, name by path asc. Ties keep the walk order
// (stable sort), so equal keys never flicker between requests.
func sortStorageSearchResults(results []storageSearchResult, sortBy string) {
	switch sortBy {
	case storageSearchSortSize:
		sort.SliceStable(results, func(i, j int) bool {
			if results[i].Bytes != results[j].Bytes {
				return results[i].Bytes > results[j].Bytes
			}
			return results[i].Path < results[j].Path
		})
	case storageSearchSortName:
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].Path < results[j].Path
		})
	default: // storageSearchSortRecent
		sort.SliceStable(results, func(i, j int) bool {
			if !results[i].ModAt.Equal(results[j].ModAt) {
				return results[i].ModAt.After(results[j].ModAt)
			}
			return results[i].Path < results[j].Path
		})
	}
}
