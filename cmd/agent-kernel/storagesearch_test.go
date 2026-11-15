package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
)

// Storage search (docs/sandbox-storage.md §5) answers from a lazy walk:
// case-insensitive substring over the area-relative path, both areas unless
// ?area= pins one, contract sorting, and every guard (root, reader role,
// project visibility, q length) mirroring the neighbouring storage routes.

// storageSearchFixtureFile is one file the fixture writes with a controlled
// size and mtime, so sort assertions never depend on creation order.
type storageSearchFixtureFile struct {
	rel   string // slash-separated, under the storage root
	bytes int
	modAt time.Time
}

func writeStorageSearchFixture(t *testing.T, files ...storageSearchFixtureFile) string {
	t.Helper()
	root := t.TempDir()
	for _, file := range files {
		full := filepath.Join(root, filepath.FromSlash(file.rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, make([]byte, file.bytes), 0o644))
		require.NoError(t, os.Chtimes(full, file.modAt, file.modAt))
	}
	return root
}

// standardStorageSearchFixture seeds both areas of project alpha: two
// READMEs (workspace + cache), a docs/ directory for path and dir-row
// assertions, and decoys that must not match "readme" or "docs/".
func standardStorageSearchFixture(t *testing.T) string {
	t.Helper()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	return writeStorageSearchFixture(t,
		storageSearchFixtureFile{rel: "projects/alpha/docs/README.md", bytes: 10, modAt: base},
		storageSearchFixtureFile{rel: "projects/alpha/docs/notes.txt", bytes: 20, modAt: base.Add(time.Hour)},
		storageSearchFixtureFile{rel: "projects/alpha/src/main.go", bytes: 30, modAt: base.Add(2 * time.Hour)},
		storageSearchFixtureFile{rel: "cache/alpha/go-build/README.md", bytes: 40, modAt: base.Add(3 * time.Hour)},
		storageSearchFixtureFile{rel: "cache/alpha/log/output.log", bytes: 50, modAt: base.Add(4 * time.Hour)},
	)
}

// newStorageSearchTestServer mounts only the search route — the inventory
// test server cannot serve it without RegisterStorageRoutes changing, which
// this file must not touch.
func newStorageSearchTestServer(t *testing.T, root string, visible map[string][]string) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nscopedtoken:scoped:reader:other")
	require.NoError(t, err)
	mux := http.NewServeMux()
	registerStorageSearch(mux, gate, func(*http.Request) (map[string][]string, error) {
		return visible, nil
	}, root)
	return httptest.NewServer(gate.Authenticate(mux))
}

// storageSearchPaths flattens a response for order assertions.
func storageSearchPaths(view storageSearchView) []string {
	paths := make([]string, 0, len(view.Results))
	for _, hit := range view.Results {
		paths = append(paths, hit.Path)
	}
	return paths
}

func decodeStorageSearch(t *testing.T, body string) storageSearchView {
	t.Helper()
	var view storageSearchView
	require.NoError(t, json.Unmarshal([]byte(body), &view), body)
	return view
}

func TestStorageSearchMatchesNameAndPath(t *testing.T) {
	root := standardStorageSearchFixture(t)
	server := newStorageSearchTestServer(t, root, map[string][]string{"alpha": {}})
	defer server.Close()

	// Case-insensitive name match across both areas.
	status, body := storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=readme")
	require.Equal(t, 200, status, body)
	view := decodeStorageSearch(t, body)
	require.Len(t, view.Results, 2)
	require.False(t, view.Truncated)
	require.GreaterOrEqual(t, view.ElapsedMs, int64(0))
	require.Less(t, view.ElapsedMs, int64(2000), "the walk must respect the response deadline")
	byArea := map[string]storageSearchResult{}
	for _, hit := range view.Results {
		byArea[hit.Area] = hit
	}
	workspaceHit, ok := byArea["workspace"]
	require.True(t, ok, "workspace area must be searched without ?area=")
	require.Equal(t, "docs/README.md", workspaceHit.Path)
	require.False(t, workspaceHit.Dir)
	require.Equal(t, int64(10), workspaceHit.Bytes)
	// Area-relative path: usable directly as ?path= for the file route.
	require.False(t, strings.HasPrefix(workspaceHit.Path, "projects/"))
	cacheHit, ok := byArea["cache"]
	require.True(t, ok, "cache area must be searched without ?area=")
	require.Equal(t, "go-build/README.md", cacheHit.Path)
	require.Equal(t, int64(40), cacheHit.Bytes)
	// mod_at round-trips as RFC3339 on the wire.
	require.Contains(t, body, `"mod_at":"2026-11-01T12:00:00Z"`)
	require.Equal(t, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), workspaceHit.ModAt)

	// Path match: "docs/" appears in the path but never in the file name.
	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=docs%2F")
	require.Equal(t, 200, status, body)
	view = decodeStorageSearch(t, body)
	require.Len(t, view.Results, 2)
	for _, hit := range view.Results {
		require.True(t, strings.HasPrefix(hit.Path, "docs/"), hit.Path)
		require.False(t, hit.Dir)
	}

	// Directories are rows too, with no size — dir-listing parity.
	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=docs")
	require.Equal(t, 200, status, body)
	view = decodeStorageSearch(t, body)
	require.Len(t, view.Results, 3)
	var dirRow *storageSearchResult
	for i := range view.Results {
		if view.Results[i].Path == "docs" {
			dirRow = &view.Results[i]
		}
	}
	require.NotNil(t, dirRow, "the docs directory itself must match")
	require.True(t, dirRow.Dir)
	require.Equal(t, int64(0), dirRow.Bytes)
	require.Equal(t, "workspace", dirRow.Area)
}

func TestStorageSearchAreaFilter(t *testing.T) {
	root := standardStorageSearchFixture(t)
	server := newStorageSearchTestServer(t, root, map[string][]string{"alpha": {}})
	defer server.Close()

	cases := []struct {
		query string
		want  []string
	}{
		{"/v1/workspace/storage/projects/alpha/search?q=readme", []string{"docs/README.md", "go-build/README.md"}},
		{"/v1/workspace/storage/projects/alpha/search?q=readme&area=workspace", []string{"docs/README.md"}},
		{"/v1/workspace/storage/projects/alpha/search?q=readme&area=cache", []string{"go-build/README.md"}},
	}
	for _, c := range cases {
		status, body := storageRequest(t, server, "readertoken", c.query)
		require.Equal(t, 200, status, body)
		view := decodeStorageSearch(t, body)
		require.ElementsMatch(t, c.want, storageSearchPaths(view), c.query)
		for _, hit := range view.Results {
			if strings.HasPrefix(hit.Path, "docs/") {
				require.Equal(t, "workspace", hit.Area)
			} else {
				require.Equal(t, "cache", hit.Area)
			}
		}
	}
}

func TestStorageSearchSort(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	root := writeStorageSearchFixture(t,
		storageSearchFixtureFile{rel: "projects/alpha/fileA.bin", bytes: 30, modAt: base},
		storageSearchFixtureFile{rel: "projects/alpha/fileB.bin", bytes: 10, modAt: base.Add(time.Hour)},
		storageSearchFixtureFile{rel: "cache/alpha/fileC.bin", bytes: 20, modAt: base.Add(2 * time.Hour)},
		storageSearchFixtureFile{rel: "cache/alpha/fileD.bin", bytes: 40, modAt: base.Add(3 * time.Hour)},
	)
	server := newStorageSearchTestServer(t, root, map[string][]string{"alpha": {}})
	defer server.Close()

	cases := []struct {
		query string
		want  []string
	}{
		// Default "recent": mtime desc — D (+3h), C (+2h), B (+1h), A (base).
		// Paths are area-relative, so the four names stay unique across the
		// two areas and the expected order is unambiguous.
		{"/v1/workspace/storage/projects/alpha/search?q=file", []string{
			"fileD.bin", "fileC.bin", "fileB.bin", "fileA.bin",
		}},
		// "size": bytes desc (D=40, A=30, C=20, B=10).
		{"/v1/workspace/storage/projects/alpha/search?q=file&sort=size", []string{
			"fileD.bin", "fileA.bin", "fileC.bin", "fileB.bin",
		}},
		// "name": path asc.
		{"/v1/workspace/storage/projects/alpha/search?q=file&sort=name", []string{
			"fileA.bin", "fileB.bin", "fileC.bin", "fileD.bin",
		}},
	}
	for _, c := range cases {
		status, body := storageRequest(t, server, "readertoken", c.query)
		require.Equal(t, 200, status, body)
		view := decodeStorageSearch(t, body)
		require.Equal(t, c.want, storageSearchPaths(view), c.query)
	}
}

func TestStorageSearchLimit(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	files := make([]storageSearchFixtureFile, 0, 12)
	for i := 0; i < 12; i++ {
		files = append(files, storageSearchFixtureFile{
			rel:   fmt.Sprintf("projects/alpha/file%02d.bin", i),
			bytes: i,
			modAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	root := writeStorageSearchFixture(t, files...)
	server := newStorageSearchTestServer(t, root, map[string][]string{"alpha": {}})
	defer server.Close()

	cases := []struct {
		query string
		want  int
	}{
		{"/v1/workspace/storage/projects/alpha/search?q=file", 12},        // default limit not reached
		{"/v1/workspace/storage/projects/alpha/search?q=file&limit=3", 3}, // explicit cut
		{"/v1/workspace/storage/projects/alpha/search?q=file&limit=0", 12},
		{"/v1/workspace/storage/projects/alpha/search?q=file&limit=abc", 12},
		{"/v1/workspace/storage/projects/alpha/search?q=file&limit=-5", 12},
	}
	for _, c := range cases {
		status, body := storageRequest(t, server, "readertoken", c.query)
		require.Equal(t, 200, status, body)
		view := decodeStorageSearch(t, body)
		require.Len(t, view.Results, c.want, c.query)
		// Reaching the limit is pagination: the walk still covered the area.
		require.False(t, view.Truncated, c.query)
	}
}

func TestStorageSearchLimitCapIs500(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	files := make([]storageSearchFixtureFile, 0, 505)
	for i := 0; i < 505; i++ {
		files = append(files, storageSearchFixtureFile{
			rel:   fmt.Sprintf("projects/alpha/file%03d.bin", i),
			bytes: 1,
			modAt: base,
		})
	}
	root := writeStorageSearchFixture(t, files...)
	server := newStorageSearchTestServer(t, root, map[string][]string{"alpha": {}})
	defer server.Close()

	// Above the cap: 500 rows regardless of what was asked for.
	status, body := storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=file&limit=1000")
	require.Equal(t, 200, status, body)
	view := decodeStorageSearch(t, body)
	require.Len(t, view.Results, 500)
	require.False(t, view.Truncated)

	// Default limit is 100.
	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=file")
	require.Equal(t, 200, status, body)
	view = decodeStorageSearch(t, body)
	require.Len(t, view.Results, 100)
}

func TestStorageSearchValidation(t *testing.T) {
	root := standardStorageSearchFixture(t)
	server := newStorageSearchTestServer(t, root, map[string][]string{"alpha": {}})
	defer server.Close()

	// q under two characters is 422 — including a missing or blank query.
	for _, suffix := range []string{"", "&q=", "&q=a", "&q=%20", "&q=%20%20"} {
		status, body := storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?x=1"+suffix)
		require.Equal(t, 422, status, "suffix %q: %s", suffix, body)
	}
	// The length rule applies to the trimmed query: " re " is two characters.
	status, body := storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=%20re%20")
	require.Equal(t, 200, status, body)

	// An unknown area is a client error (same as the file routes), while an
	// unknown sort degrades to the default instead of failing.
	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=docs&area=projects")
	require.Equal(t, 400, status, body)
	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=docs&area=bogus")
	require.Equal(t, 400, status, body)
	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=docs&sort=bogus")
	require.Equal(t, 200, status, body)
	require.Len(t, decodeStorageSearch(t, body).Results, 3)
}

func TestStorageSearchGuards(t *testing.T) {
	// No sandbox root: the whole storage API answers 503.
	noRoot := newStorageSearchTestServer(t, "", map[string][]string{"alpha": {}})
	defer noRoot.Close()
	status, body := storageRequest(t, noRoot, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=docs")
	require.Equal(t, 503, status, body)

	root := standardStorageSearchFixture(t)
	visible := newStorageSearchTestServer(t, root, map[string][]string{"alpha": {}})
	defer visible.Close()

	// Reader role (and authentication) like every neighbouring route.
	status, _ = storageRequest(t, visible, "", "/v1/workspace/storage/projects/alpha/search?q=docs")
	require.Equal(t, 401, status)
	// A reader scoped to another project gets 403, not someone else's rows.
	status, _ = storageRequest(t, visible, "scopedtoken", "/v1/workspace/storage/projects/alpha/search?q=docs")
	require.Equal(t, 403, status)

	// Invisible and unknown projects answer the same 404.
	hidden := newStorageSearchTestServer(t, root, map[string][]string{"other": {}})
	defer hidden.Close()
	status, body = storageRequest(t, hidden, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=docs")
	require.Equal(t, 404, status, body)

	// Ids that could walk out of the root are rejected before the walk —
	// ValidProjectID fires first, exactly like the file routes' authorize.
	status, body = storageRequest(t, hidden, "readertoken", "/v1/workspace/storage/projects/..%2Fescape/search?q=docs")
	require.Equal(t, 400, status, body)
	status, body = storageRequest(t, hidden, "readertoken", "/v1/workspace/storage/projects/a%5Cb/search?q=docs")
	require.Equal(t, 400, status, body)
}

func TestStorageSearchEmptyResultAndMissingCacheArea(t *testing.T) {
	// A project that never ran has no cache directory — that is a normal
	// empty state, not a walk failure.
	root := writeStorageSearchFixture(t, storageSearchFixtureFile{
		rel: "projects/alpha/only.txt", bytes: 7, modAt: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
	})
	server := newStorageSearchTestServer(t, root, map[string][]string{"alpha": {}})
	defer server.Close()

	status, body := storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=only")
	require.Equal(t, 200, status, body)
	view := decodeStorageSearch(t, body)
	require.Len(t, view.Results, 1)
	require.Equal(t, "workspace", view.Results[0].Area)

	// No matches: 200 with an empty array (never null), not truncated.
	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=zzzz")
	require.Equal(t, 200, status, body)
	require.Contains(t, body, `"results":[]`)
	require.False(t, decodeStorageSearch(t, body).Truncated)
}

func TestStorageSearchTruncatedWhenBudgetAlreadyExpired(t *testing.T) {
	root := standardStorageSearchFixture(t)
	gate, err := controlplane.NewGate("readertoken:reader:reader:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	registerStorageSearch(mux, gate, func(*http.Request) (map[string][]string, error) {
		return map[string][]string{"alpha": {}}, nil
	}, root)

	// An already-expired deadline makes the walk stop on its first entry —
	// the response must be a partial 200 (truncated:true), never an error.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/v1/workspace/storage/projects/alpha/search?q=readme", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer readertoken")
	recorder := httptest.NewRecorder()
	gate.Authenticate(mux).ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	view := decodeStorageSearch(t, recorder.Body.String())
	require.True(t, view.Truncated, "an exhausted budget must answer a partial page")
	require.Empty(t, view.Results)
}

func TestStorageSearchDepthBound(t *testing.T) {
	// A file past the walk's depth limit is outside the search scope, while
	// a shallow twin with the same name is found — and depth being scope
	// (like the tree route's depth parameter) does not mark truncation.
	deepRel := "projects/alpha/deep/" + strings.Repeat("d/", storageSearchMaxDepth) + "deep.txt"
	root := writeStorageSearchFixture(t,
		storageSearchFixtureFile{rel: "projects/alpha/deep.txt", bytes: 5, modAt: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)},
		storageSearchFixtureFile{rel: deepRel, bytes: 6, modAt: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)},
	)
	server := newStorageSearchTestServer(t, root, map[string][]string{"alpha": {}})
	defer server.Close()

	status, body := storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha/search?q=deep.txt")
	require.Equal(t, 200, status, body)
	view := decodeStorageSearch(t, body)
	require.Equal(t, []string{"deep.txt"}, storageSearchPaths(view))
	require.False(t, view.Truncated)
}

// The 2026-11-15 incident: a lexical DFS walked into the capital-letter
// subtree first (node_modules under SuperApp), burned the whole budget
// inside it, and the root-level file the operator saw as the tree's first
// row was never reached — search answered «nothing found». Breadth-first
// ordering pins the fix: every level-N match lands before anything deeper.
func TestStorageSearchVisitsShallowLevelsBeforeDeepSubtrees(t *testing.T) {
	root := t.TempDir()
	area := filepath.Join(root, "projects", "alpha")
	// «A-huge» sorts before «z-root-match.txt» — the order a DFS would visit
	// and get stuck in.
	huge := filepath.Join(area, "A-huge")
	require.NoError(t, os.MkdirAll(filepath.Join(huge, "nested"), 0o755))
	for i := 0; i < 50; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(huge, fmt.Sprintf("file-%02d.txt", i)), []byte("x"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(huge, "nested", "deep-match.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(area, "z-root-match.txt"), []byte("x"), 0o644))

	walk := &storageSearchWalk{query: "match", queryLower: "match", hits: make([]storageSearchResult, 0, 4)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !walk.area(ctx, area, "workspace") {
		t.Fatal("walk must complete inside the budget")
	}
	paths := make([]string, 0, len(walk.hits))
	for _, hit := range walk.hits {
		paths = append(paths, hit.Path)
	}
	require.Equal(t, []string{"z-root-match.txt", "A-huge/nested/deep-match.txt"}, paths,
		"the shallow match must land before the deep one — BFS order, not lexical DFS")
}
