package landlock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/temporality-project/temporality/workspace"
)

func plant(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func ruleMap(result Result) map[string]Access {
	rules := make(map[string]Access, len(result.Rules))
	for _, r := range result.Rules {
		rules[r.Path] |= r.Access
	}
	return rules
}

func TestCompileUnrestrictedCollapsesToOneRule(t *testing.T) {
	root := t.TempDir()
	plant(t, root, "src/a.go", "docs/b.md")
	result := Compile(root, &workspace.EffectiveFilesystem{})
	if len(result.Rules) != 1 {
		t.Fatalf("rules = %+v, want one root rule", result.Rules)
	}
	if result.Rules[0].Path != root || result.Rules[0].Access != FullOps {
		t.Fatalf("root rule = %+v, want FullOps on %s", result.Rules[0], root)
	}
}

// The ТЗ §3.4 intersection: Global ** ∩ Unit src/** ∩ Project src/backend/**
// — the OS layer sees exactly src/backend as the writable subtree.
func TestCompileIntersectedWriteAreas(t *testing.T) {
	root := t.TempDir()
	plant(t, root, "src/backend/api.go", "src/other/x.go", "docs/d.md")
	policy := workspace.MergeFilesystem([]workspace.Policy{
		{Enabled: true, Filesystem: &workspace.FilesystemRules{Write: []string{"**"}}},
		{Enabled: true, Filesystem: &workspace.FilesystemRules{Write: []string{"src/**"}}},
		{Enabled: true, Filesystem: &workspace.FilesystemRules{Write: []string{"src/backend/**"}}},
	})
	rules := ruleMap(Compile(root, &policy))
	if rules[filepath.Join(root, "src", "backend")] != FullOps {
		t.Errorf("src/backend rule = %x, want FullOps", rules[filepath.Join(root, "src", "backend")])
	}
	for _, p := range []string{
		filepath.Join(root, "src", "other"),
		filepath.Join(root, "src", "other", "x.go"),
		filepath.Join(root, "docs", "d.md"),
	} {
		if access := rules[p]; access&WriteOps != 0 {
			t.Errorf("rule for %s = %x, must carry no write ops", p, access)
		}
	}
}

// A denied subarea loses everything; the clean sibling subtree keeps FullOps;
// a rule must never cover both (Landlock rules are pure allows).
func TestCompileDenySubtree(t *testing.T) {
	root := t.TempDir()
	plant(t, root, "secrets/key.pem", "secrets/nested/deep.txt", "src/a.go")
	policy := workspace.MergeFilesystem([]workspace.Policy{
		{Enabled: true, Filesystem: &workspace.FilesystemRules{Write: []string{"**"}, Deny: []string{"secrets/**"}}},
	})
	rules := ruleMap(Compile(root, &policy))
	for _, p := range []string{
		filepath.Join(root, "secrets"),
		filepath.Join(root, "secrets", "key.pem"),
		filepath.Join(root, "secrets", "nested", "deep.txt"),
	} {
		if access := rules[p]; access != 0 {
			t.Errorf("denied entry %s got access %x", p, access)
		}
	}
	if rules[filepath.Join(root, "src")] != FullOps {
		t.Errorf("src rule = %x, want FullOps", rules[filepath.Join(root, "src")])
	}
	if rules[root]&ReadDir == 0 {
		t.Error("the tainted root must still allow listing")
	}
	if rules[root]&^ReadOps != 0 {
		t.Errorf("the tainted root got %x beyond ReadOps — creation there would also cover secrets/", rules[root])
	}
}

// Name-pattern masks: exact per-file grants for existing files, no creation
// rights anywhere (a name pattern cannot stop `> dir/evil`), and an explicit
// approximation note for the operator.
func TestCompileNamePatternIsConservative(t *testing.T) {
	root := t.TempDir()
	plant(t, root, "src/a.go", "src/b.txt")
	policy := workspace.MergeFilesystem([]workspace.Policy{
		{Enabled: true, Filesystem: &workspace.FilesystemRules{Write: []string{"src/*.go"}}},
	})
	result := Compile(root, &policy)
	rules := ruleMap(result)
	if len(result.Notes) == 0 {
		t.Error("a name-pattern mask must produce an approximation note")
	}
	for _, r := range result.Rules {
		if r.Access&(MakeReg|MakeDir|MakeSym|MakeFifo|MakeSock|MakeChar|MakeBlock) != 0 {
			t.Errorf("rule %s = %x: creation rights must not appear under name-pattern masks", r.Path, r.Access)
		}
	}
	goRule := rules[filepath.Join(root, "src", "a.go")]
	if goRule&(WriteFile|Truncate) == 0 {
		t.Errorf("existing src/a.go rule = %x, want write", goRule)
	}
	if goRule&ReadFile != 0 {
		t.Errorf("src/a.go rule = %x: reads ride the ancestor ReadOps rule, no per-file read needed", goRule)
	}
	if rules[filepath.Join(root, "src", "b.txt")] != 0 {
		t.Errorf("src/b.txt got %x, want no rule (outside the write mask)", rules[filepath.Join(root, "src", "b.txt")])
	}
}

// Read-only area plus a narrow write area: reads cover the area once, only
// the writable file carries a write rule.
func TestCompileReadWideWriteNarrow(t *testing.T) {
	root := t.TempDir()
	plant(t, root, "docs/a.md", "docs/report.md", "other/x.md")
	policy := workspace.MergeFilesystem([]workspace.Policy{
		{Enabled: true, Filesystem: &workspace.FilesystemRules{Read: []string{"docs/**"}, Write: []string{"docs/report.md"}}},
	})
	rules := ruleMap(Compile(root, &policy))
	if rules[filepath.Join(root, "docs")] != ReadOps {
		t.Errorf("docs rule = %x, want ReadOps", rules[filepath.Join(root, "docs")])
	}
	report := rules[filepath.Join(root, "docs", "report.md")]
	if report&(WriteFile|Truncate) == 0 {
		t.Errorf("docs/report.md rule = %x, want write", report)
	}
	if rules[filepath.Join(root, "other", "x.md")] != 0 {
		t.Errorf("other/x.md got %x, want none (outside read area)", rules[filepath.Join(root, "other", "x.md")])
	}
}
