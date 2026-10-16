package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/storage"
)

// The storage API is read-only phase 1 (docs/sandbox-storage.md §5): the
// snapshot filters by project visibility, the drill-down hides unknown or
// invisible projects behind a uniform 404, and host health answers even
// without docker.

func newStorageTestServer(t *testing.T, root string, visible map[string][]string) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*\nadmintoken:admin:admin:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterStorageRoutes(mux, gate, func(*http.Request) (map[string][]string, error) {
		return visible, nil
	}, root, "/nonexistent-docker-binary", &storageSnapshotCache{}, nil)
	return httptest.NewServer(gate.Authenticate(mux))
}

func storageRequest(t *testing.T, server *httptest.Server, token, path string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
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

func TestStorageSnapshotFiltersByVisibility(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "projects", "alpha", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "projects", "alpha", "pkg", "a.bin"), make([]byte, 300), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "projects", "secret"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "projects", "secret", "s.txt"), make([]byte, 10), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "cache", "alpha", "go-build"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "cache", "alpha", "go-build", "g"), make([]byte, 100), 0o644))

	server := newStorageTestServer(t, root, map[string][]string{"alpha": {}})
	defer server.Close()

	status, body := storageRequest(t, server, "readertoken", "/v1/workspace/storage")
	require.Equal(t, 200, status, body)
	var view storageSnapshotView
	require.NoError(t, json.Unmarshal([]byte(body), &view))
	require.Len(t, view.Projects, 1)
	require.Equal(t, "alpha", view.Projects[0].Project)
	require.Equal(t, int64(300), view.Projects[0].WorkspaceBytes)
	require.Equal(t, int64(1), view.Projects[0].WorkspaceFiles)
	require.Equal(t, int64(100), view.Projects[0].CacheBytes)
	require.Equal(t, int64(defaultStorageQuotaBytes), view.Projects[0].QuotaBytes)
	require.InDelta(t, 400.0/float64(defaultStorageQuotaBytes), view.Projects[0].QuotaUsed, 1e-12)
	require.NotEmpty(t, view.Projects[0].TopPaths)
	require.Equal(t, "pkg", view.Projects[0].TopPaths[0].Path)
	require.NotNil(t, view.Projects[0].LastActivityAt)
	require.Greater(t, view.Disk.TotalBytes, uint64(0))
	// generated_at round-trips as RFC3339 with the timezone attached.
	require.False(t, view.GeneratedAt.IsZero())
}

func TestStorageSnapshotWithoutRootIs503(t *testing.T) {
	server := newStorageTestServer(t, "", map[string][]string{"alpha": {}})
	defer server.Close()
	status, _ := storageRequest(t, server, "readertoken", "/v1/workspace/storage")
	require.Equal(t, 503, status)
}

func TestStorageSnapshotRequiresReader(t *testing.T) {
	server := newStorageTestServer(t, t.TempDir(), map[string][]string{})
	defer server.Close()
	status, _ := storageRequest(t, server, "", "/v1/workspace/storage")
	require.Equal(t, 401, status)
}

func TestStorageProjectTreeHidesInvisibleProjects(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "projects", "alpha", "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "projects", "alpha", "docs", "a.md"), make([]byte, 50), 0o644))

	server := newStorageTestServer(t, root, map[string][]string{"alpha": {}})
	defer server.Close()

	status, body := storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/alpha?depth=2")
	require.Equal(t, 200, status, body)
	var tree storageProjectTreeView
	require.NoError(t, json.Unmarshal([]byte(body), &tree))
	require.Equal(t, "alpha", tree.Project)
	require.Equal(t, 2, tree.Depth)
	require.NotNil(t, tree.Workspace)
	require.Equal(t, int64(50), tree.Workspace.Bytes)
	require.NotEmpty(t, tree.TopFiles)

	// Invisible and unknown projects answer the same 404 — the layout must
	// not leak which ids exist on disk.
	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/secret")
	require.Equal(t, 404, status, body)
	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/nope")
	require.Equal(t, 404, status, body)

	// Traversal attempts are rejected by the project-id contract.
	status, body = storageRequest(t, server, "readertoken", "/v1/workspace/storage/projects/..%2Fescape")
	require.Equal(t, 404, status, body)
}

func TestSystemHealthAnswersWithoutDocker(t *testing.T) {
	server := newStorageTestServer(t, t.TempDir(), map[string][]string{})
	defer server.Close()
	status, body := storageRequest(t, server, "readertoken", "/v1/system/health")
	require.Equal(t, 200, status, body)
	var health storageHealthView
	require.NoError(t, json.Unmarshal([]byte(body), &health))
	require.NotEmpty(t, health.Disk)
	require.Greater(t, health.CPU.Cores, 0)
	// The stub docker binary is unreachable: the widget must degrade, not 500.
	require.False(t, health.Docker.Available)
	require.True(t, strings.Contains(body, `"docker"`))
}

func TestStorageQuotaEnvOverride(t *testing.T) {
	t.Setenv("KERNEL_STORAGE_DEFAULT_QUOTA_BYTES", "1000")
	require.Equal(t, int64(1000), storageQuotaBytes())
	t.Setenv("KERNEL_STORAGE_DEFAULT_QUOTA_BYTES", "not-a-number")
	require.Equal(t, int64(defaultStorageQuotaBytes), storageQuotaBytes())
}

func TestStorageSweeperGate(t *testing.T) {
	require.True(t, storageSweeperEnabled())
	for _, off := range []string{"off", "0", "false", "NO", " Off "} {
		t.Setenv("KERNEL_STORAGE_SWEEPER", off)
		require.False(t, storageSweeperEnabled(), "value %q", off)
	}
}

func TestEmitStorageSnapshotEvent(t *testing.T) {
	outbox := &captureProfileOutbox{}
	inv := &storage.Inventory{
		GeneratedAt: time.Now().UTC(),
		Root:        "/srv/temporality/.sandbox",
		Disk:        storage.DiskUsage{TotalBytes: 1, FreeBytes: 1},
		Projects:    []storage.ProjectUsage{{Project: "omni", Workspace: storage.AreaUsage{Bytes: 41 << 30}}},
	}
	emitStorageSnapshotEvent(context.Background(), outbox, inv)
	events := outbox.byType("storage.snapshot")
	require.Len(t, events, 1)
	require.Equal(t, "storage.snapshot", events[0].Type)
	require.Equal(t, "workspace", events[0].Source.ID)
	require.Equal(t, "/srv/temporality/.sandbox", events[0].Data["root"])
	// Nil surfaces must be a no-op, not a panic.
	emitStorageSnapshotEvent(context.Background(), nil, nil)
	require.Len(t, outbox.byType("storage.snapshot"), 1)
}
