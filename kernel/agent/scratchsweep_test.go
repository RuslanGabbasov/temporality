package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// SweepRunScratch is the only thing between a project and an unbounded
// /scratch graveyard (the 2026-11 incident: ~29G of run trees piled up while
// nobody watched the sweep). The guard pins the contract that kept the disk
// growing silently: stale run trees are removed against the RUNNER'S root,
// fresh ones stay for post-run inspection, and an unconfigured runner is a
// no-op rather than a nil-deref or a sweep against the wrong path.
func TestSweepRunScratchRemovesStaleRunTrees(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "scratch", "proj", "run-old")
	fresh := filepath.Join(root, "scratch", "proj", "run-new")
	for _, dir := range []string{stale, fresh} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "artifact.bin"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The sweep ages the RUN directory's mtime — backdate only the stale one.
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	activities := &Activities{Sandbox: &fileToolSandbox{root: root}}
	removed, err := activities.SweepRunScratch(time.Now(), 7*24*time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want exactly the stale run tree", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale run tree survived the sweep")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh run tree must stay for post-run inspection: %v", err)
	}
}

// A sweep configured against a root without any scratch is a quiet no-op —
// the incident's silent variant (wrong/empty root) must not error, so the
// periodic caller keeps ticking instead of logging warnings forever.
func TestSweepRunScratchQuietOnEmptyRoot(t *testing.T) {
	activities := &Activities{Sandbox: &fileToolSandbox{root: t.TempDir()}}
	removed, err := activities.SweepRunScratch(time.Now(), 7*24*time.Hour)
	if err != nil || removed != 0 {
		t.Fatalf("empty-root sweep = (%d, %v), want (0, nil)", removed, err)
	}
}

// No runner at all: sandboxConfigured short-circuits before touching the
// filesystem — the sweep must report nothing rather than panic (the path the
// production incident took when KERNEL_SANDBOX_IMAGE was unset at boot).
func TestSweepRunScratchInertWithoutRunner(t *testing.T) {
	activities := &Activities{}
	removed, err := activities.SweepRunScratch(time.Now(), 7*24*time.Hour)
	if err != nil || removed != 0 {
		t.Fatalf("unconfigured sweep = (%d, %v), want (0, nil)", removed, err)
	}
}
