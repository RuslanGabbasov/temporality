package world

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestRepository writes a minimal loose-object git repository without
// invoking the git binary: one commit, one staged file, one untracked file.
func buildTestRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	for _, dir := range []string{"objects/info", "objects/pack", "refs/heads"} {
		if err := os.MkdirAll(filepath.Join(gitDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blobSHA := writeLooseObject(t, gitDir, "blob", []byte("hello temporality\n"))
	treeContent := fmt.Sprintf("100644 README.md\x00%s", mustHex(t, blobSHA))
	treeSHA := writeLooseObject(t, gitDir, "tree", []byte(treeContent))
	author := "Tester <tester@example.com> 1700000000 +0000"
	commitContent := fmt.Sprintf("tree %s\nauthor %s\ncommitter %s\n\ninitial observation\n", treeSHA, author, author)
	commitSHA := writeLooseObject(t, gitDir, "commit", []byte(commitContent))
	if err := os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"), []byte(commitSHA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = https://example.com/repo.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello temporality\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeIndex(t, gitDir, []IndexEntry{{Path: "README.md", SHA: blobSHA}})
	return root
}

func mustHex(t *testing.T, sha string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(sha)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeLooseObject(t *testing.T, gitDir, kind string, content []byte) string {
	t.Helper()
	header := []byte(fmt.Sprintf("%s %d\x00", kind, len(content)))
	full := append(header, content...)
	sum := sha1.Sum(full)
	fullHex := hex.EncodeToString(sum[:])
	objectPath := filepath.Join(gitDir, "objects", fullHex[:2], fullHex[2:])
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	if _, err := writer.Write(full); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, buf.Bytes(), 0o444); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(sum[:])
}

func writeIndex(t *testing.T, gitDir string, entries []IndexEntry) {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("DIRC")
	_ = binary.Write(&buf, binary.BigEndian, uint32(2))
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(entries)))
	for _, entry := range entries {
		start := buf.Len()
		for i := 0; i < 10; i++ {
			_ = binary.Write(&buf, binary.BigEndian, uint32(0))
		}
		buf.Write(mustHex(t, entry.SHA))
		_ = binary.Write(&buf, binary.BigEndian, uint16(len(entry.Path)))
		buf.WriteString(entry.Path)
		buf.WriteByte(0)
		for (buf.Len()-start)%8 != 0 {
			buf.WriteByte(0)
		}
	}
	// git index v2 ends with the SHA-1 of everything before it.
	sum := sha1.Sum(buf.Bytes())
	buf.Write(sum[:])
	if err := os.WriteFile(filepath.Join(gitDir, "index"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitHeadAndRemotes(t *testing.T) {
	root := buildTestRepository(t)
	gitDir, err := GitDir(root)
	if err != nil {
		t.Fatal(err)
	}
	branch, head, detached, err := ReadHead(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" || detached || len(head) != 40 {
		t.Fatalf("unexpected head: %q %q %v", branch, head, detached)
	}
	remotes, err := ReadRemotes(gitDir)
	if err != nil || len(remotes) != 1 || remotes[0].Name != "origin" || remotes[0].URL != "https://example.com/repo.git" {
		t.Fatalf("unexpected remotes: %#v %v", remotes, err)
	}
}

func TestGitWalkCommits(t *testing.T) {
	root := buildTestRepository(t)
	gitDir, _ := GitDir(root)
	_, head, _, _ := ReadHead(gitDir)
	commits, complete, err := WalkCommits(gitDir, head, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || len(commits) != 1 {
		t.Fatalf("unexpected walk: %d commits complete=%v", len(commits), complete)
	}
	if commits[0].Summary != "initial observation" || !strings.Contains(commits[0].Author, "Tester") {
		t.Fatalf("unexpected commit: %#v", commits[0])
	}
}

func TestGitStatus(t *testing.T) {
	root := buildTestRepository(t)
	status, err := InspectRepository(root, 100)
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "main" || status.Detached {
		t.Fatalf("unexpected branch state: %#v", status)
	}
	if !status.HeadReadable {
		t.Fatal("head commit not readable")
	}
	if status.Staged != 1 || status.Untracked != 1 {
		t.Fatalf("unexpected counts: staged=%d untracked=%d", status.Staged, status.Untracked)
	}
}

func TestGitStatusRejectsNonRepository(t *testing.T) {
	if _, err := InspectRepository(t.TempDir(), 10); err != ErrNotAGitRepository {
		t.Fatalf("expected ErrNotAGitRepository, got %v", err)
	}
}

func TestGitDirRejectsMissing(t *testing.T) {
	if _, err := GitDir(t.TempDir()); err != ErrNotAGitRepository {
		t.Fatalf("expected ErrNotAGitRepository, got %v", err)
	}
}
