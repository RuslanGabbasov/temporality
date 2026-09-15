package world

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateBranchFromHead(t *testing.T) {
	root := buildTestRepository(t)
	gitDir, _ := GitDir(root)
	_, head, _, _ := ReadHead(gitDir)
	sha, err := CreateBranch(root, "experiment/agent", "")
	if err != nil {
		t.Fatal(err)
	}
	if sha != head {
		t.Fatalf("branch points at %q, want HEAD %q", sha, head)
	}
	raw, readErr := os.ReadFile(filepath.Join(gitDir, "refs", "heads", "experiment", "agent"))
	if readErr != nil || strings.TrimSpace(string(raw)) != head {
		t.Fatalf("ref file wrong: %q %v", raw, readErr)
	}
	if _, err = CreateBranch(root, "experiment/agent", ""); !errors.Is(err, ErrBranchExists) {
		t.Fatalf("duplicate branch accepted: %v", err)
	}
}

func TestCreateBranchFromNamedStart(t *testing.T) {
	root := buildTestRepository(t)
	gitDir, _ := GitDir(root)
	if _, err := CreateBranch(root, "feature", "main"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(gitDir, "refs", "heads", "feature"))
	_, head, _, _ := ReadHead(gitDir)
	if strings.TrimSpace(string(raw)) != head {
		t.Fatal("named start point did not resolve to main")
	}
	if _, err := CreateBranch(root, "other", "missing-branch"); err == nil {
		t.Fatal("unknown start point accepted")
	}
}

func TestCreateBranchUnborn(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateBranch(root, "x", ""); !errors.Is(err, ErrUnbornHead) {
		t.Fatalf("unborn HEAD accepted: %v", err)
	}
}

func TestValidateBranchName(t *testing.T) {
	for _, name := range []string{"main", "feature/x", "agent.experiment-1", "v1.0.2"} {
		if err := ValidateBranchName(name); err != nil {
			t.Fatalf("valid name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"", "-bad", "has..dots", ".hidden", "a/../b", "ends.lock", "ends.", "has space", "a/"} {
		if err := ValidateBranchName(name); err == nil {
			t.Fatalf("invalid name %q accepted", name)
		}
	}
}

func TestCommitWorktreeAdvancesRef(t *testing.T) {
	root := buildTestRepository(t)
	gitDir, _ := GitDir(root)
	_, parent, _, _ := ReadHead(gitDir)
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("tracked change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "new.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := CommitWorktree(root, "agent changes", "Agent <agent@temporality.local>", 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.Parent != parent || result.Commit == "" || result.Commit == parent {
		t.Fatalf("commit did not advance: %#v", result)
	}
	if result.Files != 3 { // README.md, notes.txt, src/new.go
		t.Fatalf("unexpected file count: %d", result.Files)
	}
	_, head, _, _ := ReadHead(gitDir)
	if head != result.Commit {
		t.Fatal("refs/heads/main was not advanced")
	}
	commit, readErr := ReadCommit(gitDir, head)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if commit.Tree != result.Tree || len(commit.Parents) != 1 || commit.Parents[0] != parent {
		t.Fatalf("commit content wrong: %#v", commit)
	}
	if commit.Author == "" || !strings.Contains(commit.Author, "agent@temporality.local") {
		t.Fatalf("author missing: %q", commit.Author)
	}
}

func TestCommitWorktreeEmptyRepo(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := CommitWorktree(root, "initial", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if result.Parent != "" || result.Files != 1 {
		t.Fatalf("unexpected initial commit: %#v", result)
	}
	commit, readErr := ReadCommit(gitDir, result.Commit)
	if readErr != nil || len(commit.Parents) != 0 {
		t.Fatalf("initial commit has parents: %#v %v", commit, readErr)
	}
}

func TestCommitWorktreeBudget(t *testing.T) {
	root := buildTestRepository(t)
	if _, err := CommitWorktree(root, "too big", "", 1); !errors.Is(err, ErrEffectTooLarge) {
		t.Fatalf("budget not enforced: %v", err)
	}
	// The failed commit must not have advanced the ref.
	gitDir, _ := GitDir(root)
	_, head, _, _ := ReadHead(gitDir)
	if head == "" {
		t.Fatal("ref damaged by over-budget commit")
	}
}

func TestCommitWorktreeRequiresMessage(t *testing.T) {
	root := buildTestRepository(t)
	if _, err := CommitWorktree(root, "  ", "", 100); err == nil {
		t.Fatal("empty message accepted")
	}
}

func TestCommitTreeSortingMatchesGit(t *testing.T) {
	root := buildTestRepository(t)
	// b/ as a directory must sort as "b/" — after file "a" but before file "b.txt".
	if err := os.MkdirAll(filepath.Join(root, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b", "inner.txt"), []byte("inner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("bee\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := CommitWorktree(root, "sorting", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	gitDir, _ := GitDir(root)
	kind, content, readErr := ReadObject(gitDir, result.Tree)
	if readErr != nil || kind != "tree" {
		t.Fatalf("tree unreadable: %v", readErr)
	}
	names := treeEntryNames(t, content)
	// Git compares directories as name+"/"; since '.' (0x2E) sorts before
	// '/' (0x2F), b.txt precedes the b directory.
	want := []string{"README.md", "a.txt", "b.txt", "b", "notes.txt"}
	if len(names) != len(want) {
		t.Fatalf("tree entries %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tree order %v, want %v", names, want)
		}
	}
}

func treeEntryNames(t *testing.T, content []byte) []string {
	t.Helper()
	names := []string{}
	offset := 0
	for offset < len(content) {
		sp := -1
		for i := offset; i < len(content); i++ {
			if content[i] == ' ' {
				sp = i
				break
			}
		}
		if sp < 0 {
			t.Fatal("malformed tree entry")
		}
		zero := -1
		for i := sp + 1; i < len(content); i++ {
			if content[i] == 0 {
				zero = i
				break
			}
		}
		if zero < 0 || zero+21 > len(content) {
			t.Fatal("malformed tree entry")
		}
		names = append(names, string(content[sp+1:zero]))
		offset = zero + 21
	}
	return names
}

func TestCommitCompatibleWithGitCLI(t *testing.T) {
	gitPath, lookErr := exec.LookPath("git")
	if lookErr != nil {
		t.Skip("git binary not available")
	}
	root := buildTestRepository(t)
	if err := os.WriteFile(filepath.Join(root, "cli.txt"), []byte("cli check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitWorktree(root, "cli verification", "CLI <cli@example.com>", 100); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		cmd := exec.Command(gitPath, append([]string{"-c", "safe.directory=*", "-C", root}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v: %s", args, err, output)
		}
		return string(output)
	}
	run("fsck")
	log := run("log", "-1", "--format=%s%n%an")
	if !strings.Contains(log, "cli verification") || !strings.Contains(log, "CLI") {
		t.Fatalf("git log cannot see our commit: %s", log)
	}
	listing := run("ls-tree", "-r", "HEAD", "--name-only")
	for _, path := range []string{"README.md", "cli.txt", "notes.txt"} {
		if !strings.Contains(listing, path) {
			t.Fatalf("git ls-tree missing %s: %s", path, listing)
		}
	}
	branch, createErr := CreateBranch(root, "cli-branch", "")
	if createErr != nil {
		t.Fatal(createErr)
	}
	if strings.TrimSpace(run("rev-parse", "cli-branch")) != branch {
		t.Fatal("git rev-parse disagrees with created branch")
	}
}
