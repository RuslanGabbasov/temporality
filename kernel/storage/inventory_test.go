package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path string, size int, mod time.Time) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, size), 0o644))
	require.NoError(t, os.Chtimes(path, mod, mod))
}

func TestSnapshotAggregatesProjectAreas(t *testing.T) {
	root := t.TempDir()
	recent := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	older := recent.Add(-24 * time.Hour)

	// alpha: workspace with two top-level consumers plus a cache area.
	writeFile(t, filepath.Join(root, "projects", "alpha", "docs", "a.md"), 100, older)
	writeFile(t, filepath.Join(root, "projects", "alpha", "node_modules", "x", "y.bin"), 900, recent)
	writeFile(t, filepath.Join(root, "projects", "alpha", "node_modules", "x", "z.bin"), 100, older)
	writeFile(t, filepath.Join(root, "cache", "alpha", "go-build", "g"), 500, older)
	// beta: cache-only project (workspace dir removed).
	writeFile(t, filepath.Join(root, "cache", "beta", "npm", "pkg.tgz"), 700, older)
	// gamma: workspace-only project.
	writeFile(t, filepath.Join(root, "projects", "gamma", "README"), 10, older)
	// Not a project area — must not appear anywhere.
	writeFile(t, filepath.Join(root, "inbound", "junk"), 42, older)
	// Files directly in the areas (not directories) are ignored.
	require.NoError(t, os.WriteFile(filepath.Join(root, "projects", "loose"), []byte("x"), 0o644))

	inv, err := Snapshot(context.Background(), root, WalkOptions{})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root), inv.Root)
	require.False(t, inv.Truncated)
	require.Greater(t, inv.Disk.TotalBytes, uint64(0))

	require.Len(t, inv.Projects, 3)
	require.Equal(t, "alpha", inv.Projects[0].Project)
	require.Equal(t, int64(1100), inv.Projects[0].Workspace.Bytes)
	require.Equal(t, int64(3), inv.Projects[0].Workspace.Files)
	require.Equal(t, int64(500), inv.Projects[0].Cache.Bytes)
	require.Equal(t, recent.UTC(), inv.Projects[0].Workspace.ModAt.UTC())

	// Top paths: node_modules dwarfs docs, and nested buckets never crowd in.
	require.NotEmpty(t, inv.Projects[0].TopPaths)
	require.Equal(t, "node_modules", inv.Projects[0].TopPaths[0].Path)
	require.Equal(t, int64(1000), inv.Projects[0].TopPaths[0].Bytes)
	for _, p := range inv.Projects[0].TopPaths[1:] {
		require.NotContains(t, p.Path, "node_modules/")
	}

	require.Equal(t, "beta", inv.Projects[1].Project)
	require.Equal(t, int64(700), inv.Projects[1].Cache.Bytes)
	require.Equal(t, int64(0), inv.Projects[1].Workspace.Bytes)
	require.Equal(t, "gamma", inv.Projects[2].Project)
	require.Equal(t, int64(10), inv.Projects[2].Workspace.Bytes)
}

func TestSnapshotMissingRootIsError(t *testing.T) {
	_, err := Snapshot(context.Background(), filepath.Join(t.TempDir(), "nope"), WalkOptions{})
	require.Error(t, err)
}

func TestSnapshotEmptyRootYieldsNoProjects(t *testing.T) {
	inv, err := Snapshot(context.Background(), t.TempDir(), WalkOptions{})
	require.NoError(t, err)
	require.Empty(t, inv.Projects)
}

func TestSnapshotBudgetMarksTruncated(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		writeFile(t, filepath.Join(root, "projects", "bulk", fmt.Sprintf("f%03d", i)), 1, time.Now())
	}
	// The deadline is checked every 64 entries; 200 files with an expired
	// budget must mark the snapshot partial instead of failing it.
	inv, err := Snapshot(context.Background(), root, WalkOptions{Budget: time.Nanosecond})
	require.NoError(t, err)
	require.True(t, inv.Truncated)
}

func TestSnapshotCanceledContextMarksTruncated(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		writeFile(t, filepath.Join(root, "projects", "bulk", fmt.Sprintf("f%03d", i)), 1, time.Now())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inv, err := Snapshot(ctx, root, WalkOptions{})
	require.NoError(t, err)
	require.True(t, inv.Truncated)
}

func TestPrefixAt(t *testing.T) {
	require.Equal(t, "a", prefixAt("a/b/c.txt", 1))
	require.Equal(t, "a/b", prefixAt("a/b/c.txt", 2))
	require.Equal(t, "a/b/c.txt", prefixAt("a/b/c.txt", 3))
	require.Equal(t, "", prefixAt("a/b/c.txt", 4))
	require.Equal(t, "", prefixAt("", 1))
}
