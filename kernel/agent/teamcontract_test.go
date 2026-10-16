package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/sandbox"
)

// contractSandbox is a minimal SandboxRunner rooted at a temp dir: contracts
// live under <root>/projects/<project>, mirroring the project workspaces
// PrepareRun creates.
type contractSandbox struct{ root string }

func (c *contractSandbox) SandboxRoot() string                       { return c.root }
func (c *contractSandbox) ResolveWorkspace(p string) (string, error) { return p, nil }
func (c *contractSandbox) Execute(context.Context, sandbox.Request) (sandbox.Result, error) {
	return sandbox.Result{}, nil
}

func TestWriteTeamContract(t *testing.T) {
	dir := t.TempDir()
	activities := &Activities{Sandbox: &contractSandbox{root: dir}}

	t.Run("writes the contract under the project workspace", func(t *testing.T) {
		rel, err := activities.WriteTeamContract(context.Background(), TeamContractRequest{Project: "repo-a", RunID: "team-run-1", TaskID: "coder", Answer: "  CODER-CONTRACT  "})
		require.NoError(t, err)
		require.Equal(t, "team/team-run-1/contracts/coder.md", rel)
		body, err := os.ReadFile(filepath.Join(dir, "projects", "repo-a", filepath.FromSlash(rel)))
		require.NoError(t, err)
		require.Contains(t, string(body), "# Contract: coder")
		require.Contains(t, string(body), "CODER-CONTRACT")
	})

	t.Run("rework overwrites the same path", func(t *testing.T) {
		_, err := activities.WriteTeamContract(context.Background(), TeamContractRequest{Project: "repo-a", RunID: "team-run-1", TaskID: "coder", Answer: "CODER-V2"})
		require.NoError(t, err)
		_, err = activities.WriteTeamContract(context.Background(), TeamContractRequest{Project: "repo-a", RunID: "team-run-1", TaskID: "coder", Answer: "CODER-V3"})
		require.NoError(t, err)
		body, err := os.ReadFile(filepath.Join(dir, "projects", "repo-a", "team", "team-run-1", "contracts", "coder.md"))
		require.NoError(t, err)
		require.Contains(t, string(body), "CODER-V3")
		require.NotContains(t, string(body), "CODER-V2")
	})

	t.Run("path traversal in ids is neutralized", func(t *testing.T) {
		_, err := activities.WriteTeamContract(context.Background(), TeamContractRequest{Project: "repo-a", RunID: "..", TaskID: "coder", Answer: "x"})
		require.Error(t, err, "a run id reduced to dots must be rejected")
		// Separators are stripped: the token becomes one filename inside the
		// contracts dir — no traversal surface even with dots in the middle.
		rel, err := activities.WriteTeamContract(context.Background(), TeamContractRequest{Project: "repo-a", RunID: "team-run-1", TaskID: "a/b..c", Answer: "x"})
		require.NoError(t, err)
		require.Equal(t, "team/team-run-1/contracts/ab..c.md", rel)
		require.FileExists(t, filepath.Join(dir, "projects", "repo-a", filepath.FromSlash(rel)))
	})

	t.Run("missing fields are rejected", func(t *testing.T) {
		_, err := activities.WriteTeamContract(context.Background(), TeamContractRequest{Project: "repo-a"})
		require.Error(t, err)
	})
}

func TestSweepTeamScratch(t *testing.T) {
	dir := t.TempDir()
	activities := &Activities{Sandbox: &contractSandbox{root: dir}}
	now := time.Now()
	old := filepath.Join(dir, "projects", "repo-a", "team", "run-old")
	fresh := filepath.Join(dir, "projects", "repo-a", "team", "run-fresh")
	require.NoError(t, os.MkdirAll(old, 0o777))
	require.NoError(t, os.MkdirAll(fresh, 0o777))
	require.NoError(t, os.Chtimes(old, now.Add(-72*time.Hour), now.Add(-72*time.Hour)))
	require.NoError(t, os.Chtimes(fresh, now.Add(-time.Minute), now.Add(-time.Minute)))

	removed, err := activities.SweepTeamScratch(now, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	_, statErr := os.Stat(old)
	require.True(t, os.IsNotExist(statErr), "expired scratch must be gone")
	_, statErr = os.Stat(fresh)
	require.NoError(t, statErr, "fresh scratch must survive")

	// A second sweep is a no-op.
	removed, err = activities.SweepTeamScratch(now, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, 0, removed)
}
