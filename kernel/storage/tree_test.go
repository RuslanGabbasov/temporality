package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProjectTreeAggregatesAndCutsDepth(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "projects", "alpha", "docs", "deep", "f.md"), 100, time.Now())
	writeFile(t, filepath.Join(root, "projects", "alpha", "top.txt"), 700, time.Now())
	writeFile(t, filepath.Join(root, "cache", "alpha", "go-build", "x"), 300, time.Now())

	tree, err := ProjectTreeSnapshot(context.Background(), root, "alpha", TreeOptions{Depth: 1})
	require.NoError(t, err)
	require.False(t, tree.Truncated)
	require.Equal(t, "alpha", tree.Project)

	// Depth 1 renders only top-level directory children (files live in the
	// totals and the top-files list), but sizes below the cut still count.
	require.Equal(t, int64(800), tree.Workspace.Bytes)
	require.Equal(t, int64(2), tree.Workspace.Files)
	require.Len(t, tree.Workspace.Children, 1)
	require.Equal(t, "docs", tree.Workspace.Children[0].Path)
	require.Equal(t, int64(100), tree.Workspace.Children[0].Bytes)

	require.Equal(t, int64(300), tree.Cache.Bytes)

	// Top files mix areas with a cache/ qualifier for the cache ones.
	require.NotEmpty(t, tree.TopFiles)
	require.Equal(t, "top.txt", tree.TopFiles[0].Path)
	require.Equal(t, int64(700), tree.TopFiles[0].Bytes)
	for _, f := range tree.TopFiles {
		if f.Path != "top.txt" && f.Path != "docs/deep/f.md" {
			require.True(t, strings.HasPrefix(f.Path, "cache/"), "path %q", f.Path)
		}
	}
}

func TestProjectTreeMissingAreasAreEmptyNodes(t *testing.T) {
	root := t.TempDir()
	tree, err := ProjectTreeSnapshot(context.Background(), root, "ghost", TreeOptions{})
	require.NoError(t, err)
	require.NotNil(t, tree.Workspace)
	require.NotNil(t, tree.Cache)
	require.Equal(t, int64(0), tree.Workspace.Bytes)
}

func TestProjectTreeNodeBudgetTruncates(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		dir := filepath.Join(root, "projects", "wide", "d"+string(rune('a'+i%26))+string(rune('a'+i/26)))
		writeFile(t, filepath.Join(dir, "f"), 1, time.Now())
	}
	tree, err := ProjectTreeSnapshot(context.Background(), root, "wide", TreeOptions{Depth: 2, MaxNodes: 5})
	require.NoError(t, err)
	require.True(t, tree.Truncated)
	// Sizes stay complete even with the node budget spent.
	require.Equal(t, int64(30), tree.Workspace.Bytes)
}

func TestProjectTreeInvalidProjectIDs(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		_, err := ProjectTreeSnapshot(context.Background(), root, id, TreeOptions{})
		require.ErrorIs(t, err, ErrInvalidProject, "id %q", id)
	}
}

func TestProjectTreeMissingRootIsError(t *testing.T) {
	_, err := ProjectTreeSnapshot(context.Background(), filepath.Join(t.TempDir(), "nope"), "alpha", TreeOptions{})
	require.Error(t, err)
}
