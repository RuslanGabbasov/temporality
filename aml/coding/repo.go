package coding

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BaseSHA pins the bleve fixture (v2.6.1 tree).
const BaseSHA = "048761396d42661336db8caa0bed1e98cf2aeaa6"

// RepoName identifies the repository in asset propositions.
const RepoName = "bleve"

// EvolutionFileName is the 3B repository-evolution commit: the analysis
// package grows a test guard that requires an environment variable, exactly
// like real projects whose tests need conventions (env vars, flags, build
// tags). The old "just run go test ./analysis" memory must be contradicted
// (test-fail), and a new memory (env var + command) must take over.
const EvolutionFileName = "analysis/testconvention_test.go"

// EvolutionFileContent is the guard added on main in phase 3B / present on
// main in phase 3C.
const EvolutionFileContent = `package analysis

import (
	"fmt"
	"os"
	"testing"
)

// TestMain enforces the analysis test convention: these tests must run with
// BLEVE_ANALYSIS_TESTMODE=1 (see docs/CONTRIBUTING.md).
func TestMain(m *testing.M) {
	if os.Getenv("BLEVE_ANALYSIS_TESTMODE") != "1" {
		fmt.Fprintln(os.Stderr, "analysis: refusing to run tests: set BLEVE_ANALYSIS_TESTMODE=1 (see docs/CONTRIBUTING.md)")
		os.Exit(1)
	}
	os.Exit(m.Run())
}
`

// PrepareWorkspace copies the pristine clone into dst and verifies the pinned
// commit. The clone itself is never touched.
func PrepareWorkspace(srcClone, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if out, err := exec.Command("cp", "-R", srcClone, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("copy clone: %v: %s", err, out)
	}
	sha, err := git(dst, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(sha) != BaseSHA {
		// Workspaces are copied from the clone per run-series; re-pin.
		if _, err := git(dst, "checkout", "--detach", BaseSHA); err != nil {
			return err
		}
	}
	// Keep workspaces on a local main branch for evolution/branch phases.
	if _, err := git(dst, "checkout", "-B", "main", BaseSHA); err != nil {
		return err
	}
	return nil
}

// ResetWorkspace removes test residue between tasks (tracked changes and
// untracked files), keeping the checkout deterministic.
func ResetWorkspace(ws string) error {
	if _, err := git(ws, "checkout", "--", "."); err != nil {
		return err
	}
	out, err := git(ws, "clean", "-fdq")
	_ = out
	return err
}

// ApplyEvolution commits the analysis test-convention guard on main.
func ApplyEvolution(ws string) error {
	path := filepath.Join(ws, EvolutionFileName)
	if _, err := os.Stat(path); err == nil {
		return nil // already evolved
	}
	if err := os.WriteFile(path, []byte(EvolutionFileContent), 0o644); err != nil {
		return err
	}
	if _, err := git(ws, "add", EvolutionFileName); err != nil {
		return err
	}
	if _, err := git(ws, "-c", "user.name=aml-bench", "-c", "user.email=aml@bench.local",
		"commit", "-m", "ci: analysis tests require BLEVE_ANALYSIS_TESTMODE=1"); err != nil {
		return err
	}
	return nil
}

// EnsureEvolved makes sure main carries the evolution (3B/3C) — no-op when
// the guard file is already on HEAD's tree.
func EnsureEvolved(ws string) error {
	if _, err := os.Stat(filepath.Join(ws, EvolutionFileName)); err == nil {
		return nil
	}
	return ApplyEvolution(ws)
}

// CheckoutBranch switches the workspace and reports whether the tree contains
// the evolution guard (used as the memory environment tag).
func CheckoutBranch(ws, branch string, baseForNew string) error {
	out, err := git(ws, "rev-parse", "--verify", branch)
	if err != nil || strings.TrimSpace(out) == "" {
		if _, err := git(ws, "branch", branch, baseForNew); err != nil {
			return err
		}
	}
	_, err = git(ws, "checkout", branch)
	return err
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}
