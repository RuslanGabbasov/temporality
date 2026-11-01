package workspace

import (
	"testing"
)

func TestMatchFSPath(t *testing.T) {
	cases := []struct {
		mask, path string
		want       bool
	}{
		{"**", "a", true},
		{"**", "a/b/c.go", true},
		{"src/**", "src", true}, // trailing ** spans zero segments
		{"src/**", "src/a.go", true},
		{"src/**", "src/deep/b.go", true},
		{"src/**", "srcx/a.go", false},
		{"src/**", "lib/a.go", false},
		{"**/backend/**", "src/backend/api.go", true},
		{"**/backend/**", "backend/api.go", true}, // zero segments before
		{"**/backend/**", "a/b/backend", true},
		{"**/backend/**", "src/frontend/api.go", false},
		{"*.go", "main.go", true},
		{"*.go", "pkg/main.go", false}, // * does not cross '/'
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/sub/main.go", false},
		{"src/*/c", "src/b/c", true},
		{"src/*/c", "src/b/x/c", false},
		{"docs/**/README.md", "docs/README.md", true},
		{"docs/**/README.md", "docs/a/b/README.md", true},
		{"docs/**/README.md", "docs/README.txt", false},
		{"[ab].go", "a.go", true},
		{"[ab].go", "c.go", false},
		{"deploy/production/**", "deploy/production/app.yaml", true},
		{"deploy/production/**", "deploy/staging/app.yaml", false},
	}
	for _, c := range cases {
		if got := MatchFSPath(c.mask, c.path); got != c.want {
			t.Errorf("MatchFSPath(%q, %q) = %v, want %v", c.mask, c.path, got, c.want)
		}
	}
}

func TestNormalizeFSPath(t *testing.T) {
	ok := map[string]string{
		"a.go":            "a.go",
		"/workspace/a.go": "a.go",
		"/workspace/x/b":  "x/b",
		"./a/./b":         "a/b",
		"a//b":            "a/b",
		"a/./b/../c":      "a/c",
		"src/sub/file.go": "src/sub/file.go",
		"a b/c d":         "a b/c d",
	}
	for in, want := range ok {
		got, err := NormalizeFSPath(in)
		if err != nil || got != want {
			t.Errorf("NormalizeFSPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{"", " ", "/etc/passwd", "..", "../x", "a/../../x", "/workspace", "a/..", "./"}
	for _, in := range bad {
		if got, err := NormalizeFSPath(in); err == nil {
			t.Errorf("NormalizeFSPath(%q) = %q, want error", in, got)
		}
	}
}

func TestValidateFilesystemMask(t *testing.T) {
	good := []string{"**", "src/**", "**/x/*", "*.go", "a/[ab]/c", "a/**/b"}
	for _, mask := range good {
		if err := ValidateFilesystemMask(mask); err != nil {
			t.Errorf("ValidateFilesystemMask(%q) = %v, want nil", mask, err)
		}
	}
	bad := []string{"", " /x", "/x", "a//b", "a/../b", "..", ".", `a\b`, "a/["}
	for _, mask := range bad {
		if err := ValidateFilesystemMask(mask); err == nil {
			t.Errorf("ValidateFilesystemMask(%q) = nil, want error", mask)
		}
	}
}

// The ТЗ §3.4 example: Global write ** ∩ OrgUnit write src/** ∩ Project write
// src/backend/** must narrow to src/backend/** — semantic intersection of
// path domains, not string overlap.
func TestMergeFilesystemIntersectsLayers(t *testing.T) {
	rows := []Policy{
		{Enabled: true, Filesystem: &FilesystemRules{Write: []string{"**"}}},
		{Enabled: true, Filesystem: &FilesystemRules{Write: []string{"src/**"}}},
		{Enabled: true, Filesystem: &FilesystemRules{Write: []string{"src/backend/**"}}},
	}
	merged := MergeFilesystem(rows)
	for _, p := range []string{"src/backend", "src/backend/api.go", "src/backend/deep/x"} {
		if !merged.AllowWrite(p) {
			t.Errorf("AllowWrite(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"src", "src/other/x.go", "docs/x.md", "README.md"} {
		if merged.AllowWrite(p) {
			t.Errorf("AllowWrite(%q) = true, want false", p)
		}
	}
}

// deny is absolute and cannot be un-denied by a lower policy (§3.4).
func TestMergeFilesystemDenyWins(t *testing.T) {
	rows := []Policy{
		{Enabled: true, Filesystem: &FilesystemRules{Write: []string{"src/**"}, Deny: []string{"src/backend/generated/**"}}},
		{Enabled: true, Filesystem: &FilesystemRules{Write: []string{"**"}, Read: []string{"**"}, Deny: []string{"secrets/**"}}},
	}
	merged := MergeFilesystem(rows)
	cases := []struct {
		path        string
		read, write bool
	}{
		{"src/backend/generated/x.go", false, false},
		{"src/backend/api.go", true, true},
		{"secrets/prod.env", false, false},
		{"docs/a.md", true, false},
	}
	for _, c := range cases {
		if got := merged.AllowRead(c.path); got != c.read {
			t.Errorf("AllowRead(%q) = %v, want %v", c.path, got, c.read)
		}
		if got := merged.AllowWrite(c.path); got != c.write {
			t.Errorf("AllowWrite(%q) = %v, want %v", c.path, got, c.write)
		}
	}
}

// Write never exists without read: the writable area is the intersection of
// the write and read areas (product decision — write без read не бывает), so
// a contradiction between the dimensions narrows to nothing instead of
// silently widening read.
func TestMergeFilesystemWriteRequiresRead(t *testing.T) {
	// Write-only rules: read is unrestricted, so the write area stands.
	writeOnly := MergeFilesystem([]Policy{
		{Enabled: true, Filesystem: &FilesystemRules{Write: []string{"out/**"}}},
	})
	if !writeOnly.AllowWrite("out/report.md") {
		t.Error("AllowWrite(out/report.md) = false, want true")
	}
	if !writeOnly.AllowRead("anywhere.md") {
		t.Error("AllowRead(anywhere.md) = false, want true — read is unrestricted")
	}

	// Read restricted away from the write area: nothing writable there —
	// the intersection decides, read is never silently widened.
	split := MergeFilesystem([]Policy{
		{Enabled: true, Filesystem: &FilesystemRules{Read: []string{"src/**"}, Write: []string{"out/**"}}},
	})
	if split.AllowWrite("out/report.md") {
		t.Error("write must not exist where read is denied (out/**)")
	}
	if split.AllowWrite("src/a.go") {
		t.Error("src/a.go is outside the write area")
	}
	if !split.AllowRead("src/a.go") {
		t.Error("src/a.go must stay readable")
	}
	if split.AllowRead("out/report.md") {
		t.Error("out/report.md is outside the read area")
	}
}

// §3.5 compatibility: rows without rules constrain nothing; a disabled row
// contributes nothing; empty non-nil lists allow nothing.
func TestMergeFilesystemCompatSemantics(t *testing.T) {
	unrestricted := MergeFilesystem([]Policy{
		{Enabled: true},
		{Enabled: true, Filesystem: &FilesystemRules{}},
		{Enabled: false, Filesystem: &FilesystemRules{Write: []string{"blocked/**"}}},
	})
	if !unrestricted.Unrestricted() {
		t.Fatalf("rows without rules must not restrict, got %+v", unrestricted)
	}
	if !unrestricted.AllowWrite("a/b") || !unrestricted.AllowRead("a/b") {
		t.Error("policy without filesystem rules must keep pre-filesystem behavior")
	}

	empty := MergeFilesystem([]Policy{
		{Enabled: true, Filesystem: &FilesystemRules{Write: []string{}, Read: []string{}}},
	})
	// Empty lists normalize to nil at the store layer (normalizePolicyLists) and
	// read as "not set" here too: §3.3 — отсутствие ограничения не должно
	// случайно интерпретироваться как запрет всего.
	if !empty.AllowWrite("a") {
		t.Error("an empty rule list must behave like an unset one, not like deny-all")
	}
	if !empty.Unrestricted() {
		t.Error("empty lists must resolve to the unrestricted policy")
	}
}

// A bare ** layer narrows nothing (intersection with everything) and must be
// dropped — but deny ** is absolute and stays.
func TestMergeFilesystemWildcardLayer(t *testing.T) {
	rows := []Policy{
		{Enabled: true, Filesystem: &FilesystemRules{Read: []string{"**"}, Write: []string{"**", "src/**"}}},
	}
	merged := MergeFilesystem(rows)
	if !merged.AllowRead("anything/deep") {
		t.Error("read layer of bare ** must not restrict")
	}
	// Write layer contains ** → the whole layer is a no-op: pre-filesystem
	// permissiveness must not be silently narrowed by a wildcard.
	if !merged.AllowWrite("docs/x.md") {
		t.Error("write layer containing ** must not restrict")
	}

	deny := MergeFilesystem([]Policy{
		{Enabled: true, Filesystem: &FilesystemRules{Deny: []string{"**"}}},
	})
	if deny.AllowRead("a") || deny.AllowWrite("a") {
		t.Error("deny ** must block everything")
	}
}

// The run snapshot fingerprint: stable under mask order and duplicates,
// sensitive to any semantic change.
func TestFilesystemVersion(t *testing.T) {
	a := MergeFilesystem([]Policy{{Enabled: true, Filesystem: &FilesystemRules{
		Read:  []string{"**"},
		Write: []string{"src/**", "docs/**"},
		Deny:  []string{"secrets/**"},
	}}})
	b := MergeFilesystem([]Policy{{Enabled: true, Filesystem: &FilesystemRules{
		Deny:  []string{"secrets/**", "secrets/**"},
		Write: []string{"docs/**", "src/**"},
		Read:  []string{"**"},
	}}})
	if a.Version() != b.Version() {
		t.Errorf("version must be stable under reordering: %s != %s", a.Version(), b.Version())
	}
	c := MergeFilesystem([]Policy{{Enabled: true, Filesystem: &FilesystemRules{
		Read:  []string{"**"},
		Write: []string{"src/**"},
		Deny:  []string{"secrets/**"},
	}}})
	if a.Version() == c.Version() {
		t.Error("version must change when rules change")
	}
	if a.Version() == (&EffectiveFilesystem{}).Version() {
		t.Error("restricted policy must not fingerprint as the zero policy")
	}
}
