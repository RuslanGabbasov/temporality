package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// Storage sweep phase 2 (docs/sandbox-storage.md §5.3, §5.5, §6): the manual
// sweep plans before it deletes and journals every run, selective paths bypass
// the age filter, the age filter spares uploads/inbox/.git, the retention
// policy round-trips through workspace_setting, and the background cycle stays
// inert until policy.enabled flips on.

// fakeStorageSweepStore is the KV/audit surface of *workspace.Store without a
// database: settings come from a map, audit rows land in a slice.
type fakeStorageSweepStore struct {
	mu       sync.Mutex
	settings map[string]string
	audits   []storedAudit
}

func newFakeStorageSweepStore() *fakeStorageSweepStore {
	return &fakeStorageSweepStore{settings: map[string]string{}}
}

func (f *fakeStorageSweepStore) GetSetting(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.settings[key]
	if !ok {
		return "", workspace.ErrNotFound
	}
	return value, nil
}

func (f *fakeStorageSweepStore) SetSetting(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings[key] = value
	return nil
}

func (f *fakeStorageSweepStore) RecordAccessAudit(_ context.Context, actor, action, entityKind, entityID string, details map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, storedAudit{actor: actor, action: action, entityKind: entityKind, entityID: entityID, details: details})
	return nil
}

// newStorageSweepTestServer mounts only the sweep/policy routes with an
// operator-capable gate (the inventory server has no operator token).
func newStorageSweepTestServer(t *testing.T, root string, visible map[string][]string, store storageSweepStore, events sandboxProfileOutbox) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*\noperatortoken:operator:operator:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	registerStorageSweep(mux, gate, func(*http.Request) (map[string][]string, error) {
		return visible, nil
	}, root, store, events)
	return httptest.NewServer(gate.Authenticate(mux))
}

// sweepFixtureFile is one file with a controlled size and mtime under the
// storage root, so age assertions never depend on creation order.
type sweepFixtureFile struct {
	rel   string // slash-separated, under the storage root
	bytes int
	modAt time.Time
}

func writeSweepFixture(t *testing.T, files ...sweepFixtureFile) string {
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

// ageSweepPath backdates a directory after its contents are written —
// directory mtimes track entry churn, so the fixture must age them last.
func ageSweepPath(t *testing.T, root, rel string, modAt time.Time) {
	t.Helper()
	require.NoError(t, os.Chtimes(filepath.Join(root, filepath.FromSlash(rel)), modAt, modAt))
}

func sweepPost(t *testing.T, server *httptest.Server, token, project, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost,
		server.URL+"/v1/workspace/storage/projects/"+project+"/sweep", strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(raw)
}

func policyPut(t *testing.T, server *httptest.Server, token, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPut, server.URL+"/v1/workspace/storage/policies", strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(raw)
}

func decodeSweep(t *testing.T, body string) storageSweepResponse {
	t.Helper()
	var view storageSweepResponse
	require.NoError(t, json.Unmarshal([]byte(body), &view), body)
	return view
}

func sweepPlanPaths(view storageSweepResponse) []string {
	if view.Plan == nil {
		return nil
	}
	paths := make([]string, 0, len(*view.Plan))
	for _, entry := range *view.Plan {
		paths = append(paths, entry.Path)
	}
	return paths
}

func TestStorageSweepDryRunPlansWithoutDeleting(t *testing.T) {
	stale := time.Now().Add(-48 * time.Hour)
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/old.bin", bytes: 300, modAt: stale},
		sweepFixtureFile{rel: "cache/alpha/new.bin", bytes: 50, modAt: time.Now()},
		sweepFixtureFile{rel: "projects/alpha/docs/old.md", bytes: 100, modAt: stale},
		sweepFixtureFile{rel: "projects/alpha/keep.md", bytes: 10, modAt: time.Now()},
	)
	store := newFakeStorageSweepStore()
	outbox := &captureProfileOutbox{}
	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, store, outbox)
	defer server.Close()

	status, body := sweepPost(t, server, "operatortoken", "alpha",
		`{"dry_run":true,"areas":["cache","workspace"],"older_than_hours":24}`)
	require.Equal(t, 200, status, body)
	view := decodeSweep(t, body)
	require.True(t, view.DryRun)
	require.Equal(t, 2, view.Removed)
	require.Equal(t, int64(400), view.FreedBytes)
	require.Contains(t, body, `"plan"`)
	require.Equal(t, []string{"cache/alpha/old.bin", "projects/alpha/docs/old.md"}, sweepPlanPaths(view))

	// A dry run touches nothing on disk, journals no evicted rows, writes no
	// audit — but still leaves the swept summary so the attempt is visible.
	for _, rel := range []string{"cache/alpha/old.bin", "projects/alpha/docs/old.md"} {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		require.NoError(t, err, rel)
	}
	require.Empty(t, outbox.byType("storage.evicted"))
	require.Len(t, outbox.byType("storage.swept"), 1)
	require.Equal(t, true, outbox.byType("storage.swept")[0].Data["dry_run"])
	require.Empty(t, store.audits)
}

func TestStorageSweepRemovesStaleFilesAndJournals(t *testing.T) {
	stale := time.Now().Add(-48 * time.Hour)
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/old.bin", bytes: 300, modAt: stale},
		sweepFixtureFile{rel: "cache/alpha/new.bin", bytes: 50, modAt: time.Now()},
		sweepFixtureFile{rel: "projects/alpha/docs/old.md", bytes: 100, modAt: stale},
	)
	store := newFakeStorageSweepStore()
	outbox := &captureProfileOutbox{}
	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, store, outbox)
	defer server.Close()

	status, body := sweepPost(t, server, "operatortoken", "alpha",
		`{"dry_run":false,"areas":["cache","workspace"],"older_than_hours":24}`)
	require.Equal(t, 200, status, body)
	view := decodeSweep(t, body)
	require.False(t, view.DryRun)
	require.Equal(t, 2, view.Removed)
	require.Equal(t, int64(400), view.FreedBytes)
	require.NotContains(t, body, `"plan"`)

	_, err := os.Stat(filepath.Join(root, "cache", "alpha", "old.bin"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(root, "projects", "alpha", "docs", "old.md"))
	require.True(t, os.IsNotExist(err))
	// Fresh siblings survive.
	_, err = os.Stat(filepath.Join(root, "cache", "alpha", "new.bin"))
	require.NoError(t, err)

	evicted := outbox.byType("storage.evicted")
	require.Len(t, evicted, 2)
	require.Equal(t, "cache/alpha/old.bin", evicted[0].Data["path"])
	require.Equal(t, int64(300), evicted[0].Data["bytes"])
	require.Equal(t, "manual", evicted[0].Data["policy"])
	require.Equal(t, "manual", evicted[0].Data["trigger"])
	require.Equal(t, 24, evicted[0].Data["older_than_hours"])
	require.Equal(t, "projects/alpha/docs/old.md", evicted[1].Data["path"])

	swept := outbox.byType("storage.swept")
	require.Len(t, swept, 1)
	require.Equal(t, "manual", swept[0].Data["trigger"])
	require.Equal(t, "alpha", swept[0].Data["project"])
	require.Equal(t, []string{"cache", "workspace"}, swept[0].Data["areas"])
	require.Equal(t, "age", swept[0].Data["mode"])
	require.Equal(t, false, swept[0].Data["dry_run"])
	require.Equal(t, 2, swept[0].Data["removed"])
	require.Equal(t, int64(400), swept[0].Data["freed_bytes"])
	require.Equal(t, 2, swept[0].Data["evicted_logged"])
	require.Equal(t, 0, swept[0].Data["evicted_suppressed"])
	require.GreaterOrEqual(t, swept[0].Data["duration_ms"].(int64), int64(0))

	// The manual run is audited (docs §5.3 «Аудит + событие»).
	require.Len(t, store.audits, 1)
	require.Equal(t, auditStorageSwept, store.audits[0].action)
	require.Equal(t, "storage", store.audits[0].entityKind)
	require.Equal(t, "alpha", store.audits[0].entityID)
	require.Equal(t, "operator", store.audits[0].actor)
}

func TestStorageSweepCollapsesWhollyStaleDirectory(t *testing.T) {
	stale := time.Now().Add(-48 * time.Hour)
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/toolchain/a.bin", bytes: 30, modAt: stale},
		sweepFixtureFile{rel: "cache/alpha/toolchain/b.bin", bytes: 20, modAt: stale},
	)
	ageSweepPath(t, root, "cache/alpha/toolchain", stale)
	outbox := &captureProfileOutbox{}
	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, newFakeStorageSweepStore(), outbox)
	defer server.Close()

	// A wholly stale subtree plans as one entry instead of one row per file.
	status, body := sweepPost(t, server, "operatortoken", "alpha",
		`{"dry_run":true,"areas":["cache"],"older_than_hours":24}`)
	require.Equal(t, 200, status, body)
	view := decodeSweep(t, body)
	require.Equal(t, 1, view.Removed)
	require.Equal(t, int64(50), view.FreedBytes)
	require.Equal(t, []string{"cache/alpha/toolchain"}, sweepPlanPaths(view))

	status, body = sweepPost(t, server, "operatortoken", "alpha",
		`{"dry_run":false,"areas":["cache"],"older_than_hours":24}`)
	require.Equal(t, 200, status, body)
	_, err := os.Stat(filepath.Join(root, "cache", "alpha", "toolchain"))
	require.True(t, os.IsNotExist(err))
	// One directory removal is one journal row with the subtree's bytes.
	evicted := outbox.byType("storage.evicted")
	require.Len(t, evicted, 1)
	require.Equal(t, "cache/alpha/toolchain", evicted[0].Data["path"])
	require.Equal(t, int64(50), evicted[0].Data["bytes"])
}

func TestStorageSweepSelectivePathsIgnoreAge(t *testing.T) {
	now := time.Now()
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/keep.bin", bytes: 10, modAt: now},                     // listed, fresh → goes
		sweepFixtureFile{rel: "cache/alpha/old.bin", bytes: 30, modAt: now.Add(-72 * time.Hour)}, // not listed, stale → stays
		sweepFixtureFile{rel: "projects/alpha/uploads/user.txt", bytes: 40, modAt: now},          // explicit pick beats the uploads guard
	)
	store := newFakeStorageSweepStore()
	outbox := &captureProfileOutbox{}
	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, store, outbox)
	defer server.Close()

	status, body := sweepPost(t, server, "operatortoken", "alpha",
		`{"dry_run":false,"areas":["cache","workspace"],"paths":["keep.bin","uploads/user.txt"]}`)
	require.Equal(t, 200, status, body)
	view := decodeSweep(t, body)
	require.Equal(t, 2, view.Removed)
	require.Equal(t, int64(50), view.FreedBytes)
	require.NotContains(t, body, `"plan"`)

	_, err := os.Stat(filepath.Join(root, "cache", "alpha", "keep.bin"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(root, "projects", "alpha", "uploads", "user.txt"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(root, "cache", "alpha", "old.bin"))
	require.NoError(t, err)

	require.Len(t, outbox.byType("storage.evicted"), 2)
	// Selective runs carry no age: policy is "manual", hours 0.
	first := outbox.byType("storage.evicted")[0]
	require.Equal(t, "manual", first.Data["policy"])
	require.Equal(t, 0, first.Data["older_than_hours"])
	require.Len(t, store.audits, 1)
}

func TestStorageSweepSelectivePathsSkipMissing(t *testing.T) {
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/only.bin", bytes: 10, modAt: time.Now()},
	)
	outbox := &captureProfileOutbox{}
	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, newFakeStorageSweepStore(), outbox)
	defer server.Close()

	// Missing paths are skipped (idempotent re-run), not errors.
	status, body := sweepPost(t, server, "operatortoken", "alpha",
		`{"dry_run":true,"areas":["cache"],"paths":["only.bin","gone.bin"]}`)
	require.Equal(t, 200, status, body)
	view := decodeSweep(t, body)
	require.Equal(t, 1, view.Removed)
	require.Equal(t, []string{"cache/alpha/only.bin"}, sweepPlanPaths(view))
}

func TestStorageSweepRejectsUnsafePaths(t *testing.T) {
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/only.bin", bytes: 10, modAt: time.Now()},
	)
	outbox := &captureProfileOutbox{}
	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, newFakeStorageSweepStore(), outbox)
	defer server.Close()

	for _, bad := range []string{
		`{"dry_run":true,"areas":["cache"],"paths":["../../secret"]}`, // escapes the area
		`{"dry_run":true,"areas":["cache"],"paths":["/etc/passwd"]}`,  // absolute
		`{"dry_run":true,"areas":["cache"],"paths":["a\\b"]}`,         // backslash
		`{"dry_run":true,"areas":["cache"],"paths":[""]}`,             // the area root itself
		`{"dry_run":true,"areas":["cache"],"paths":["."]}`,            // the area root itself
	} {
		status, body := sweepPost(t, server, "operatortoken", "alpha", bad)
		require.Equal(t, 400, status, "%s → %s", bad, body)
	}
	// Nothing was removed by the rejected requests.
	_, err := os.Stat(filepath.Join(root, "cache", "alpha", "only.bin"))
	require.NoError(t, err)
}

func TestStorageSweepWorkspaceAgeSparesUserData(t *testing.T) {
	stale := time.Now().Add(-48 * time.Hour)
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "projects/alpha/uploads/u.txt", bytes: 500, modAt: stale},
		sweepFixtureFile{rel: "projects/alpha/inbox/i.txt", bytes: 400, modAt: stale},
		sweepFixtureFile{rel: "projects/alpha/.git/config", bytes: 300, modAt: stale},
		sweepFixtureFile{rel: "projects/alpha/src/.git/objects/o", bytes: 200, modAt: stale},
		sweepFixtureFile{rel: "projects/alpha/src/old.go", bytes: 60, modAt: stale},
		sweepFixtureFile{rel: "projects/alpha/stale.txt", bytes: 50, modAt: stale},
		sweepFixtureFile{rel: "projects/alpha/fresh.txt", bytes: 10, modAt: time.Now()},
	)
	ageSweepPath(t, root, "projects/alpha/src", stale)
	outbox := &captureProfileOutbox{}
	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, newFakeStorageSweepStore(), outbox)
	defer server.Close()

	status, body := sweepPost(t, server, "operatortoken", "alpha",
		`{"dry_run":true,"areas":["workspace"],"older_than_hours":24}`)
	require.Equal(t, 200, status, body)
	view := decodeSweep(t, body)
	// Only stale ordinary files: uploads/, inbox/ and both .git/ trees are
	// protected, the fresh file is inside the window, and src/ itself does
	// not collapse because its .git would go with it.
	require.Equal(t, []string{"projects/alpha/src/old.go", "projects/alpha/stale.txt"}, sweepPlanPaths(view))

	status, body = sweepPost(t, server, "operatortoken", "alpha",
		`{"dry_run":false,"areas":["workspace"],"older_than_hours":24}`)
	require.Equal(t, 200, status, body)
	require.Equal(t, 2, decodeSweep(t, body).Removed)

	for _, kept := range []string{
		"projects/alpha", // never the project root itself
		"projects/alpha/uploads/u.txt",
		"projects/alpha/inbox/i.txt",
		"projects/alpha/.git/config",
		"projects/alpha/src/.git/objects/o",
		"projects/alpha/src",
		"projects/alpha/fresh.txt",
	} {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(kept)))
		require.NoError(t, err, "%s must survive", kept)
	}
	for _, gone := range []string{"projects/alpha/src/old.go", "projects/alpha/stale.txt"} {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(gone)))
		require.True(t, os.IsNotExist(err), "%s must go", gone)
	}
}

func TestStorageSweepStaleDirWithSymlinkDoesNotCollapse(t *testing.T) {
	stale := time.Now().Add(-48 * time.Hour)
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/bundle/old.bin", bytes: 25, modAt: stale},
	)
	bundle := filepath.Join(root, "cache", "alpha", "bundle")
	require.NoError(t, os.Symlink(filepath.Join(bundle, "old.bin"), filepath.Join(bundle, "link")))
	ageSweepPath(t, root, "cache/alpha/bundle", stale)
	outbox := &captureProfileOutbox{}
	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, newFakeStorageSweepStore(), outbox)
	defer server.Close()

	status, body := sweepPost(t, server, "operatortoken", "alpha",
		`{"dry_run":false,"areas":["cache"],"older_than_hours":24}`)
	require.Equal(t, 200, status, body)
	// The symlink is not an ordinary file: age mode leaves it alone, which
	// also forbids collapsing its parent (RemoveAll would take the link).
	_, err := os.Stat(filepath.Join(bundle, "old.bin"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Lstat(filepath.Join(bundle, "link"))
	require.NoError(t, err)
	_, err = os.Stat(bundle)
	require.NoError(t, err)
	require.Len(t, outbox.byType("storage.evicted"), 1)
}

func TestStorageSweepEvictedEventCap(t *testing.T) {
	stale := time.Now().Add(-48 * time.Hour)
	files := make([]sweepFixtureFile, 0, 150)
	for i := range 150 {
		files = append(files, sweepFixtureFile{
			rel:   fmt.Sprintf("cache/alpha/f%03d.bin", i),
			bytes: 1,
			modAt: stale,
		})
	}
	root := writeSweepFixture(t, files...)
	outbox := &captureProfileOutbox{}
	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, newFakeStorageSweepStore(), outbox)
	defer server.Close()

	status, body := sweepPost(t, server, "operatortoken", "alpha",
		`{"dry_run":false,"areas":["cache"],"older_than_hours":24}`)
	require.Equal(t, 200, status, body)
	view := decodeSweep(t, body)
	require.Equal(t, 150, view.Removed)
	require.Equal(t, int64(150), view.FreedBytes)

	// The journal is capped at the first 100 rows; the summary carries the
	// suppressed tail so the totals still reconcile.
	require.Len(t, outbox.byType("storage.evicted"), 100)
	swept := outbox.byType("storage.swept")
	require.Len(t, swept, 1)
	require.Equal(t, 150, swept[0].Data["removed"])
	require.Equal(t, 100, swept[0].Data["evicted_logged"])
	require.Equal(t, 50, swept[0].Data["evicted_suppressed"])

	entries, err := os.ReadDir(filepath.Join(root, "cache", "alpha"))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestStorageSweepGuards(t *testing.T) {
	stale := time.Now().Add(-48 * time.Hour)
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/old.bin", bytes: 10, modAt: stale},
	)
	outbox := &captureProfileOutbox{}
	store := newFakeStorageSweepStore()

	// No configured root → 503, no journal → 503.
	noRoot := newStorageSweepTestServer(t, "", map[string][]string{"alpha": {}}, store, outbox)
	defer noRoot.Close()
	status, _ := sweepPost(t, noRoot, "operatortoken", "alpha", `{"dry_run":true,"areas":["cache"]}`)
	require.Equal(t, 503, status)
	noJournal := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, store, nil)
	defer noJournal.Close()
	status, _ = sweepPost(t, noJournal, "operatortoken", "alpha", `{"dry_run":true,"areas":["cache"]}`)
	require.Equal(t, 503, status)

	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, store, outbox)
	defer server.Close()

	// Role ladder: operator is required, writer is not enough.
	status, _ = sweepPost(t, server, "", "alpha", `{"dry_run":true,"areas":["cache"]}`)
	require.Equal(t, 401, status)
	for _, token := range []string{"readertoken", "writertoken"} {
		status, _ = sweepPost(t, server, token, "alpha", `{"dry_run":true,"areas":["cache"]}`)
		require.Equal(t, 403, status, token)
	}
	// Visibility: unknown projects and invisible ones share the 404.
	status, _ = sweepPost(t, server, "operatortoken", "ghost", `{"dry_run":true,"areas":["cache"]}`)
	require.Equal(t, 404, status)
	invisible := newStorageSweepTestServer(t, root, map[string][]string{"other": {}}, store, outbox)
	defer invisible.Close()
	status, _ = sweepPost(t, invisible, "operatortoken", "alpha", `{"dry_run":true,"areas":["cache"]}`)
	require.Equal(t, 404, status)

	// Request validation.
	for _, bad := range []struct {
		body string
		want int
	}{
		{`{"dry_run":true}`, 422},                   // no areas
		{`{"dry_run":true,"areas":[]}`, 422},        // empty areas
		{`{"dry_run":true,"areas":["bogus"]}`, 400}, // unknown area
		{`{"dry_run":true,"areas":["cache"],"older_than_hours":0}`, 422},
		{`{"dry_run":true,"areas":["cache"],"older_than_hours":-1}`, 422},
		{`{"dry_run":true,"areas":["cache"],"older_than_hours":9999999}`, 422},
		{`not json`, 400},
	} {
		status, body := sweepPost(t, server, "operatortoken", "alpha", bad.body)
		require.Equal(t, bad.want, status, "%s → %s", bad.body, body)
	}
	// Validation must not have deleted anything.
	_, err := os.Stat(filepath.Join(root, "cache", "alpha", "old.bin"))
	require.NoError(t, err)
}

func TestStorageSweepAgeDefaultsFromPolicy(t *testing.T) {
	now := time.Now()
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/old.bin", bytes: 7, modAt: now.Add(-48 * time.Hour)},
		sweepFixtureFile{rel: "cache/alpha/recent.bin", bytes: 3, modAt: now.Add(-2 * time.Hour)},
	)
	store := newFakeStorageSweepStore()
	// cache_ttl_hours=4: the 48h file is stale, the 2h one is inside the TTL.
	// workspace_stale_hours=0: the workspace area is off and gets skipped.
	require.NoError(t, store.SetSetting(context.Background(), storagePolicyKey,
		`{"enabled":false,"cache_ttl_hours":4,"workspace_stale_hours":0,"quota_bytes":0}`))
	outbox := &captureProfileOutbox{}
	server := newStorageSweepTestServer(t, root, map[string][]string{"alpha": {}}, store, outbox)
	defer server.Close()

	status, body := sweepPost(t, server, "operatortoken", "alpha", `{"dry_run":true,"areas":["cache"]}`)
	require.Equal(t, 200, status, body)
	view := decodeSweep(t, body)
	require.Equal(t, 1, view.Removed)
	require.Equal(t, []string{"cache/alpha/old.bin"}, sweepPlanPaths(view))

	// A disabled area plans nothing rather than sweeping "everything".
	status, body = sweepPost(t, server, "operatortoken", "alpha", `{"dry_run":true,"areas":["workspace"]}`)
	require.Equal(t, 200, status, body)
	view = decodeSweep(t, body)
	require.Equal(t, 0, view.Removed)
	require.NotNil(t, view.Plan)
	require.Empty(t, *view.Plan)
}

func TestStoragePoliciesRoundTrip(t *testing.T) {
	store := newFakeStorageSweepStore()
	server := newStorageSweepTestServer(t, t.TempDir(), map[string][]string{}, store, &captureProfileOutbox{})
	defer server.Close()

	// Before any record: the documented safe defaults, reader-readable.
	status, body := storageRequest(t, server, "readertoken", "/v1/workspace/storage/policies")
	require.Equal(t, 200, status, body)
	var policy storagePolicy
	require.NoError(t, json.Unmarshal([]byte(body), &policy), body)
	require.Equal(t, defaultStoragePolicy(), policy)
	require.False(t, policy.Enabled)

	// Writer PUT round-trips.
	status, body = policyPut(t, server, "writertoken",
		`{"enabled":true,"cache_ttl_hours":24,"workspace_stale_hours":48,"quota_bytes":1048576}`)
	require.Equal(t, 200, status, body)
	var stored storagePolicy
	require.NoError(t, json.Unmarshal([]byte(body), &stored), body)
	require.Equal(t, storagePolicy{Enabled: true, CacheTTLHours: 24, WorkspaceStaleHours: 48, QuotaBytes: 1048576}, stored)

	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/policies")
	require.Equal(t, 200, status, body)
	require.NoError(t, json.Unmarshal([]byte(body), &policy), body)
	require.Equal(t, stored, policy)

	// The change is audited.
	require.Len(t, store.audits, 1)
	require.Equal(t, workspace.AuditSettingChanged, store.audits[0].action)
	require.Equal(t, "storage.policy", store.audits[0].entityID)
}

func TestStoragePoliciesValidationAndRoles(t *testing.T) {
	store := newFakeStorageSweepStore()
	server := newStorageSweepTestServer(t, t.TempDir(), map[string][]string{}, store, &captureProfileOutbox{})
	defer server.Close()

	// Reader can GET but not PUT.
	status, _ := storageRequest(t, server, "", "/v1/workspace/storage/policies")
	require.Equal(t, 401, status)
	status, _ = policyPut(t, server, "readertoken", `{"enabled":true}`)
	require.Equal(t, 403, status)

	for _, bad := range []struct {
		body string
		want int
	}{
		{`{"enabled":true,"cache_ttl_hours":-1}`, 422},
		{`{"enabled":true,"workspace_stale_hours":-5}`, 422},
		{`{"enabled":true,"cache_ttl_hours":9999999}`, 422},
		{`{"enabled":true,"quota_bytes":-1}`, 422},
		{`not json`, 400}, // malformed JSON is a decode error, not a range one
	} {
		status, body := policyPut(t, server, "writertoken", bad.body)
		require.Equal(t, bad.want, status, "%s → %s", bad.body, body)
	}
	// Zero values stay legal: 0 means «выкл», not "invalid".
	status, body := policyPut(t, server, "writertoken",
		`{"enabled":false,"cache_ttl_hours":0,"workspace_stale_hours":0,"quota_bytes":0}`)
	require.Equal(t, 200, status, body)

	// Nothing was stored by the rejected PUTs.
	raw, err := store.GetSetting(context.Background(), storagePolicyKey)
	require.NoError(t, err)
	require.Contains(t, raw, `"enabled":false`)
}

func TestRunStorageSweepCycleDisabledByDefault(t *testing.T) {
	stale := time.Now().Add(-72 * time.Hour)
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/old.bin", bytes: 300, modAt: stale},
		sweepFixtureFile{rel: "projects/alpha/old.txt", bytes: 200, modAt: stale},
	)
	store := newFakeStorageSweepStore() // no record → default policy, enabled=false
	outbox := &captureProfileOutbox{}
	runStorageSweepCycle(context.Background(), root, &storageSnapshotCache{}, store, outbox)

	// Auto-deletion is off: no events at all, no filesystem changes.
	require.Empty(t, outbox.byType("storage.swept"))
	require.Empty(t, outbox.byType("storage.evicted"))
	require.Empty(t, outbox.byType("storage.quota_exceeded"))
	_, err := os.Stat(filepath.Join(root, "cache", "alpha", "old.bin"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "projects", "alpha", "old.txt"))
	require.NoError(t, err)
}

func TestRunStorageSweepCycleSweepsEnabledPolicy(t *testing.T) {
	stale := time.Now().Add(-72 * time.Hour)
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/old.bin", bytes: 300, modAt: stale},
		sweepFixtureFile{rel: "cache/alpha/new.bin", bytes: 50, modAt: time.Now()},
		sweepFixtureFile{rel: "projects/alpha/stale.txt", bytes: 200, modAt: stale},
		sweepFixtureFile{rel: "projects/alpha/uploads/u.txt", bytes: 500, modAt: stale},
		sweepFixtureFile{rel: "projects/alpha/fresh.txt", bytes: 10, modAt: time.Now()},
	)
	store := newFakeStorageSweepStore()
	require.NoError(t, store.SetSetting(context.Background(), storagePolicyKey,
		`{"enabled":true,"cache_ttl_hours":1,"workspace_stale_hours":1,"quota_bytes":100}`))
	outbox := &captureProfileOutbox{}
	runStorageSweepCycle(context.Background(), root, &storageSnapshotCache{}, store, outbox)

	// Policy trimming: stale ordinary files go, fresh ones and uploads/ stay.
	_, err := os.Stat(filepath.Join(root, "cache", "alpha", "old.bin"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(root, "projects", "alpha", "stale.txt"))
	require.True(t, os.IsNotExist(err))
	for _, kept := range []string{
		"cache/alpha/new.bin",
		"projects/alpha/uploads/u.txt",
		"projects/alpha/fresh.txt",
	} {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(kept)))
		require.NoError(t, err, kept)
	}

	// Quota came before the sweep on the same snapshot: 710+350=1060 > 100.
	quota := outbox.byType("storage.quota_exceeded")
	require.Len(t, quota, 1)
	require.Equal(t, "alpha", quota[0].Data["project"])
	require.Equal(t, int64(100), quota[0].Data["quota_bytes"])
	require.Equal(t, int64(1060), quota[0].Data["used_bytes"])
	require.Equal(t, int64(710), quota[0].Data["workspace_bytes"])
	require.Equal(t, int64(350), quota[0].Data["cache_bytes"])

	// One evicted row per removal, cache sweep before workspace sweep.
	evicted := outbox.byType("storage.evicted")
	require.Len(t, evicted, 2)
	require.Equal(t, "cache/alpha/old.bin", evicted[0].Data["path"])
	require.Equal(t, "cache_ttl_hours", evicted[0].Data["policy"])
	require.Equal(t, "projects/alpha/stale.txt", evicted[1].Data["path"])
	require.Equal(t, "workspace_stale_hours", evicted[1].Data["policy"])
	for _, event := range evicted {
		require.Equal(t, "background", event.Data["trigger"])
		require.Equal(t, "alpha", event.Data["project"])
	}

	swept := outbox.byType("storage.swept")
	require.Len(t, swept, 1)
	require.Equal(t, "background", swept[0].Data["trigger"])
	require.Equal(t, false, swept[0].Data["dry_run"])
	require.Equal(t, 2, swept[0].Data["removed"])
	require.Equal(t, int64(500), swept[0].Data["freed_bytes"])
	require.Equal(t, 1, swept[0].Data["quota_exceeded"])
	require.Equal(t, 2, swept[0].Data["evicted_logged"])
}

func TestRunStorageSweepCycleQuotaZeroEmitsNothing(t *testing.T) {
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/old.bin", bytes: 300, modAt: time.Now().Add(-72 * time.Hour)},
	)
	store := newFakeStorageSweepStore()
	require.NoError(t, store.SetSetting(context.Background(), storagePolicyKey,
		`{"enabled":true,"cache_ttl_hours":1,"workspace_stale_hours":0,"quota_bytes":0}`))
	outbox := &captureProfileOutbox{}
	runStorageSweepCycle(context.Background(), root, &storageSnapshotCache{}, store, outbox)

	// quota_bytes=0 means "no quota" — the sweep still runs, the warning
	// never fires.
	require.Empty(t, outbox.byType("storage.quota_exceeded"))
	require.Len(t, outbox.byType("storage.evicted"), 1)
	require.Len(t, outbox.byType("storage.swept"), 1)
	_, err := os.Stat(filepath.Join(root, "cache", "alpha", "old.bin"))
	require.True(t, os.IsNotExist(err))
}

func TestStorageSweepNilJournalDeletesNothing(t *testing.T) {
	root := writeSweepFixture(t,
		sweepFixtureFile{rel: "cache/alpha/old.bin", bytes: 300, modAt: time.Now().Add(-72 * time.Hour)},
	)
	store := newFakeStorageSweepStore()
	require.NoError(t, store.SetSetting(context.Background(), storagePolicyKey,
		`{"enabled":true,"cache_ttl_hours":1,"workspace_stale_hours":0,"quota_bytes":0}`))
	// Without a journal the background cycle refuses to delete (docs §4:
	// «ничего не удаляется молча»), even though the policy is on.
	runStorageSweepCycle(context.Background(), root, &storageSnapshotCache{}, store, nil)
	_, err := os.Stat(filepath.Join(root, "cache", "alpha", "old.bin"))
	require.NoError(t, err)

	// The emit helpers and the journal treat a nil outbox as a no-op.
	emitStorageEvictedEvent(context.Background(), nil, observation.Context{}, map[string]any{"path": "x"})
	emitStorageSweptEvent(context.Background(), nil, observation.Context{}, map[string]any{})
	emitStorageQuotaExceededEvent(context.Background(), nil, observation.Context{}, map[string]any{})
	journal := &storageEvictionJournal{events: nil, trigger: "manual"}
	journal.record(context.Background(), storageEviction{project: "alpha"}, "cache/alpha/x", 1)
	require.Equal(t, 0, journal.logged)
}
