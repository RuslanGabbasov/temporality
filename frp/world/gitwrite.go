package world

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Minimal pure-Go git write support (M12.3). Like the reader it shells out to
// nothing: blobs, trees, and commits are written as loose objects and refs are
// updated directly, so effects stay deterministic and auditable. Limitations
// are deliberate: packed objects are neither read nor written, checkout is not
// implemented, and commit snapshots the worktree (never the staged index).

var (
	ErrBranchExists   = errors.New("git branch already exists")
	ErrBranchInvalid  = errors.New("invalid git branch name")
	ErrEffectTooLarge = errors.New("world effect exceeds declared limits")
)

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// ValidateBranchName checks a candidate branch name against the conservative
// subset git ref rules this runtime is willing to write.
func ValidateBranchName(name string) error {
	if !branchPattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrBranchInvalid, name)
	}
	if strings.Contains(name, "..") || strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("%w: %q", ErrBranchInvalid, name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return fmt.Errorf("%w: %q", ErrBranchInvalid, name)
		}
	}
	return nil
}

// writeObject stores a loose object and returns its SHA. Existing objects are
// left untouched, making repeated commits idempotent for unchanged content.
func writeObject(gitDir, kind string, content []byte) (string, error) {
	header := []byte(fmt.Sprintf("%s %d\x00", kind, len(content)))
	full := append(header, content...)
	sum := sha1.Sum(full)
	name := hex.EncodeToString(sum[:])
	path := filepath.Join(gitDir, "objects", name[:2], name[2:])
	if _, err := os.Stat(path); err == nil {
		return name, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	if _, err := writer.Write(full); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o444); err != nil {
		return "", err
	}
	return name, nil
}

// GitCommitResult reports what a worktree commit changed.
type GitCommitResult struct {
	Branch string
	Commit string
	Parent string
	Tree   string
	Files  int
}

// CreateBranch writes refs/heads/<name> pointing at a start commit. An empty
// start resolves to the current HEAD; otherwise start may be a branch name or
// a full SHA.
func CreateBranch(root, name, start string) (string, error) {
	if err := ValidateBranchName(name); err != nil {
		return "", err
	}
	gitDir, err := GitDir(root)
	if err != nil {
		return "", err
	}
	sha := validateSHA(start)
	if sha == "" {
		if start == "" {
			_, head, _, headErr := ReadHead(gitDir)
			if headErr != nil && !errors.Is(headErr, ErrUnbornHead) {
				return "", headErr
			}
			if head == "" {
				return "", ErrUnbornHead
			}
			sha = head
		} else {
			sha, err = readRef(gitDir, "refs/heads/"+start)
			if err != nil {
				sha = validateSHA(start)
			}
			if sha == "" {
				return "", fmt.Errorf("start point %q not found", start)
			}
		}
	}
	refPath := filepath.Join(gitDir, "refs", "heads", filepath.FromSlash(name))
	if _, err = os.Stat(refPath); err == nil {
		return "", fmt.Errorf("%w: %s", ErrBranchExists, name)
	}
	if _, err = ReadCommit(gitDir, sha); err != nil {
		return "", fmt.Errorf("start point %s is not a readable commit: %w", sha, err)
	}
	if err = os.MkdirAll(filepath.Dir(refPath), 0o755); err != nil {
		return "", err
	}
	if err = os.WriteFile(refPath, []byte(sha+"\n"), 0o644); err != nil {
		return "", err
	}
	return sha, nil
}

type treeEntry struct {
	name string
	mode string
	sha  string
}

func (e treeEntry) sortKey() string {
	if e.mode == "40000" {
		return e.name + "/"
	}
	return e.name
}

// buildTree recursively writes tree objects for a directory, skipping .git,
// and returns the tree SHA. budget bounds the number of entries visited.
func buildTree(gitDir, dir string, budget *int) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	values := make([]treeEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		*budget--
		if *budget < 0 {
			return "", ErrEffectTooLarge
		}
		path := filepath.Join(dir, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			return "", infoErr
		}
		switch {
		case entry.IsDir():
			sha, treeErr := buildTree(gitDir, path, budget)
			if treeErr != nil {
				return "", treeErr
			}
			values = append(values, treeEntry{name: entry.Name(), mode: "40000", sha: sha})
		case info.Mode()&os.ModeSymlink != 0:
			link, linkErr := os.Readlink(path)
			if linkErr != nil {
				return "", linkErr
			}
			sha, blobErr := writeObject(gitDir, "blob", []byte(link))
			if blobErr != nil {
				return "", blobErr
			}
			values = append(values, treeEntry{name: entry.Name(), mode: "120000", sha: sha})
		default:
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", readErr
			}
			sha, blobErr := writeObject(gitDir, "blob", content)
			if blobErr != nil {
				return "", blobErr
			}
			mode := "100644"
			if info.Mode()&0o111 != 0 {
				mode = "100755"
			}
			values = append(values, treeEntry{name: entry.Name(), mode: mode, sha: sha})
		}
	}
	// Git sorts tree entries by name, comparing directories as name+"/".
	sort.Slice(values, func(i, j int) bool { return values[i].sortKey() < values[j].sortKey() })
	var buf bytes.Buffer
	for _, entry := range values {
		buf.WriteString(entry.mode)
		buf.WriteByte(' ')
		buf.WriteString(entry.name)
		buf.WriteByte(0)
		raw, decodeErr := hex.DecodeString(entry.sha)
		if decodeErr != nil {
			return "", decodeErr
		}
		buf.Write(raw)
	}
	return writeObject(gitDir, "tree", buf.Bytes())
}

// countFiles counts the worktree entries a commit would visit, bounded by
// budget, so over-limit worktrees are rejected before any object is written.
func countFiles(root string, budget *int) (int, error) {
	count := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		count++
		*budget--
		if *budget < 0 {
			return ErrEffectTooLarge
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrEffectTooLarge) {
		return count, err
	}
	return count, err
}

// CommitWorktree snapshots the entire worktree (skipping .git) as loose
// objects and advances the current ref to the new commit. The staged index is
// deliberately ignored: this runtime always commits the observed state of the
// working directory.
func CommitWorktree(root, message, author string, maxEntries int) (GitCommitResult, error) {
	if strings.TrimSpace(message) == "" {
		return GitCommitResult{}, errors.New("commit message is required")
	}
	if maxEntries <= 0 {
		return GitCommitResult{}, ErrEffectTooLarge
	}
	gitDir, err := GitDir(root)
	if err != nil {
		return GitCommitResult{}, err
	}
	branch, parent, detached, err := ReadHead(gitDir)
	if err != nil && !errors.Is(err, ErrUnbornHead) {
		return GitCommitResult{}, err
	}
	result := GitCommitResult{Branch: branch, Parent: parent}
	countBudget := maxEntries
	if result.Files, err = countFiles(root, &countBudget); err != nil {
		return GitCommitResult{}, err
	}
	treeBudget := maxEntries
	if result.Tree, err = buildTree(gitDir, root, &treeBudget); err != nil {
		return GitCommitResult{}, err
	}
	if author == "" {
		author = "Temporality Agent <agent@temporality.local>"
	}
	at := time.Now()
	stamp := fmt.Sprintf("%d +0000", at.Unix())
	var commitContent bytes.Buffer
	fmt.Fprintf(&commitContent, "tree %s\n", result.Tree)
	if parent != "" {
		fmt.Fprintf(&commitContent, "parent %s\n", parent)
	}
	fmt.Fprintf(&commitContent, "author %s %s\n", author, stamp)
	fmt.Fprintf(&commitContent, "committer %s %s\n", author, stamp)
	fmt.Fprintf(&commitContent, "\n%s\n", strings.TrimSpace(message))
	if result.Commit, err = writeObject(gitDir, "commit", commitContent.Bytes()); err != nil {
		return GitCommitResult{}, err
	}
	if detached {
		if err = os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(result.Commit+"\n"), 0o644); err != nil {
			return GitCommitResult{}, err
		}
		return result, nil
	}
	refPath := filepath.Join(gitDir, "refs", "heads", filepath.FromSlash(branch))
	if err = os.MkdirAll(filepath.Dir(refPath), 0o755); err != nil {
		return GitCommitResult{}, err
	}
	if err = os.WriteFile(refPath, []byte(result.Commit+"\n"), 0o644); err != nil {
		return GitCommitResult{}, err
	}
	return result, nil
}
