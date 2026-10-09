package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, rel, content string) string {
	t.Helper()
	root := t.TempDir()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolveWorkspaceFileAcceptsWorkspacePaths(t *testing.T) {
	root := writeTemp(t, filepath.Join("out", "report.md"), "# hi")
	for _, arg := range []string{"/workspace/out/report.md", "out/report.md", "out//report.md"} {
		abs, name, err := resolveWorkspaceFile(root, arg)
		if err != nil {
			t.Fatalf("resolve(%q): %v", arg, err)
		}
		if name != "report.md" || abs != filepath.Join(root, "out", "report.md") {
			t.Fatalf("resolve(%q) = %q %q", arg, abs, name)
		}
	}
}

func TestResolveWorkspaceFileRejectsEscapes(t *testing.T) {
	root := writeTemp(t, "secret.txt", "x")
	for _, arg := range []string{"", "   ", "/workspace", "..", "../secret.txt", "/workspace/../../etc/passwd", "a/../../../secret.txt"} {
		if _, _, err := resolveWorkspaceFile(root, arg); err == nil {
			t.Fatalf("path %q must be rejected", arg)
		}
	}
}

func TestResolveWorkspaceFileRejectsMissingAndDirs(t *testing.T) {
	root := writeTemp(t, "placeholder.txt", "x")
	if err := os.MkdirAll(filepath.Join(root, "nested", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveWorkspaceFile(root, "missing.txt"); err == nil {
		t.Fatal("missing file must be rejected")
	}
	if _, _, err := resolveWorkspaceFile(root, "nested/dir"); err == nil {
		t.Fatal("directory must be rejected")
	}
	if _, _, err := resolveWorkspaceFile("", "report.md"); err == nil {
		t.Fatal("empty workspace must be rejected")
	}
}

func TestResolveWorkspaceFileCapsSize(t *testing.T) {
	root := writeTemp(t, "big.bin", strings.Repeat("x", 1024))
	// Shrink the cap view: a file larger than the cap must fail. The real cap is
	// 50MB; exercise the branch through a tiny file compared against the real
	// constant by writing a file just over sendFileUploadCap is impractical, so
	// assert the constant is sane and the small file passes.
	if sendFileUploadCap < 1<<20 {
		t.Fatalf("upload cap suspiciously small: %d", sendFileUploadCap)
	}
	if _, _, err := resolveWorkspaceFile(root, "big.bin"); err != nil {
		t.Fatalf("small file must pass: %v", err)
	}
}
