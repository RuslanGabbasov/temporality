package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAreaPath(t *testing.T) {
	root := t.TempDir()
	abs, err := ResolveAreaPath(root, "omni", "workspace", "docs/readme.md")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(abs), "/projects/omni/docs/readme.md") {
		t.Errorf("abs = %q", abs)
	}
	// Empty rel = area root.
	abs, err = ResolveAreaPath(root, "omni", "cache", "")
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(abs), "/cache/omni") {
		t.Errorf("cache root = %q", abs)
	}
}

func TestResolveAreaPathRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"../escape", "a/../../escape", "..", "/abs/path", `a\b`, "x\x00y"} {
		if _, err := ResolveAreaPath(root, "omni", "workspace", rel); err == nil {
			t.Errorf("rel %q must be refused", rel)
		}
	}
	if _, err := ResolveAreaPath(root, "../project", "workspace", "x"); err == nil {
		t.Error("bad project id must be refused")
	}
	if _, err := ResolveAreaPath(root, "omni", "system", "x"); err == nil {
		t.Error("unknown area must be refused")
	}
}

func TestListDirDeletesAndUploads(t *testing.T) {
	root := t.TempDir()
	area := filepath.Join(root, "projects", "omni")
	if err := os.MkdirAll(filepath.Join(area, "src/nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(area, "src/main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := ListDir(root, "omni", "workspace", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "src" || !entries[0].Dir || entries[0].Entries != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	// Nested listing carries slash-relative paths.
	nested, err := ListDir(root, "omni", "workspace", "src")
	if err != nil {
		t.Fatalf("list nested: %v", err)
	}
	if nested[0].Path != "src/nested" || nested[1].Path != "src/main.go" || nested[1].Bytes != 12 {
		t.Fatalf("nested = %+v", nested)
	}
	// Upload creates parents; overwrite needs the explicit flag.
	if err := StoreFile(root, "omni", "workspace", "src/gen/outil.txt", []byte("hi"), false); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := StoreFile(root, "omni", "workspace", "src/gen/outil.txt", []byte("hi"), false); err == nil {
		t.Fatal("second store without overwrite must conflict")
	}
	// Delete removes a subtree but never the area root.
	if err := DeletePath(root, "omni", "workspace", "src/gen"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := DeletePath(root, "omni", "workspace", ""); err == nil {
		t.Fatal("deleting the area root must be refused")
	}
	if _, err := os.Stat(filepath.Join(area, "src/main.go")); err != nil {
		t.Fatalf("unrelated file must survive: %v", err)
	}
	// Preview caps reads.
	big := filepath.Join(area, "big.bin")
	if err := os.WriteFile(big, make([]byte, MaxPreviewBytes+10), 0o644); err != nil {
		t.Fatal(err)
	}
	data, truncated, err := ReadPreview(big, MaxPreviewBytes+10)
	if err != nil || !truncated || len(data) != MaxPreviewBytes {
		t.Fatalf("preview = %d bytes truncated=%v err=%v", len(data), truncated, err)
	}
}

// The tree shows what a directory weighs: ListDir measures child subtrees,
// and the number updates after mutations — the size cache must serve repeat
// expands without re-walking, yet an upload or delete must show up
// immediately instead of waiting for the TTL.
func TestListDirReportsDirectorySizes(t *testing.T) {
	root := t.TempDir()
	area := filepath.Join(root, "projects", "omni")
	if err := os.MkdirAll(filepath.Join(area, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(area, "docs/a.md"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ListDir(root, "omni", "workspace", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Bytes != 100 {
		t.Fatalf("directory size = %+v, want 100 bytes", entries)
	}
	// Repeat listing answers from the cache with the same number.
	again, err := ListDir(root, "omni", "workspace", "")
	if err != nil || again[0].Bytes != 100 {
		t.Fatalf("cached listing = %+v (err %v), want 100 bytes", again, err)
	}

	// An upload invalidates the ancestors: the new size is immediate.
	if err := StoreFile(root, "omni", "workspace", "docs/b.md", make([]byte, 25), false); err != nil {
		t.Fatalf("store: %v", err)
	}
	after, err := ListDir(root, "omni", "workspace", "")
	if err != nil || after[0].Bytes != 125 {
		t.Fatalf("after upload = %+v (err %v), want 125 bytes", after, err)
	}

	// So does a delete.
	if err := DeletePath(root, "omni", "workspace", "docs/a.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	final, err := ListDir(root, "omni", "workspace", "")
	if err != nil || final[0].Bytes != 25 {
		t.Fatalf("after delete = %+v (err %v), want 25 bytes", final, err)
	}
}

func TestStatFileMissingIsClean(t *testing.T) {
	root := t.TempDir()
	_, _, _, _, err := StatFile(root, "omni", "workspace", "nope.txt")
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}
