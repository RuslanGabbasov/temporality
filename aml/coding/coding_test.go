package coding

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/temporality-project/temporality/aml/llm"
	"github.com/temporality-project/temporality/aml/memory"
)

func TestInferScope(t *testing.T) {
	cases := map[string]string{
		"go test ./index/scorch/... -run Rollback":    "scorch",
		"analysis/freq.go":                            "analysis",
		"go test ./geov2/...":                         "geov2",
		"grep pattern index/upsidedown/upsidedown.go": "upsidedown",
		"go test ./...":                               "",
		"ls":                                          "",
	}
	for in, want := range cases {
		if got := InferScope(in, Subsystems); got != want {
			t.Errorf("InferScope(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassifyCause(t *testing.T) {
	cases := map[string]string{
		"--- FAIL: TestX (0.00s)\nFAIL\tgithub.com/blevesearch/bleve/v2/analysis\t0.5s":                                        memory.CauseTestFail,
		"analysis: refusing to run tests: set BLEVE_ANALYSIS_TESTMODE=1\nFAIL\tgithub.com/blevesearch/bleve/v2/analysis\t0.1s": memory.CauseTestFail,
		"# github.com/blevesearch/bleve/v2/foo\n./x.go:10:2: undefined: bar\nFAIL\tgithub.com/foo [build failed]":              memory.CauseBuild,
		"grep: invalid option -- P\nusage: grep":     memory.CauseNotFound,
		"find: -printf: unknown primary or operator": memory.CauseNotFound,
		"go: cannot find package: banana":            memory.CauseNotFound,
		"something mysterious":                       "",
	}
	for in, want := range cases {
		if got := ClassifyCause(in, 1); got != want {
			t.Errorf("ClassifyCause(%q) = %q, want %q", in, got, want)
		}
	}
}

func rec(seq int, tool string, params map[string]any, ok bool, cause string) memory.CallRecord {
	return memory.CallRecord{Seq: seq, Tool: tool, Params: params, OK: ok, Status: map[bool]int{true: 0, false: 1}[ok], Cause: cause}
}

func TestCodingExtractor(t *testing.T) {
	log := []memory.CallRecord{
		rec(1, "shell", map[string]any{"command": "grep -rnP 'TokenFrequency' analysis/"}, false, memory.CauseNotFound),
		rec(2, "shell", map[string]any{"command": "grep -rn 'TokenFrequency' analysis/"}, true, ""),
		rec(3, "read_file", map[string]any{"path": "analysis/freq.go"}, true, ""),
		rec(4, "read_file", map[string]any{"path": "analysis/freq.go"}, true, ""),
		rec(5, "read_file", map[string]any{"path": "analysis/freq_test.go"}, true, ""),
		rec(6, "shell", map[string]any{"command": "go test ./analysis/..."}, true, ""),
	}
	opt := memory.ExtractOptions{
		SessionID: "s1", TaskID: "T1", TaskText: "In the analysis subsystem, find where token frequencies are computed",
		Service: "analysis", Environment: "main", Success: true,
	}
	assets := Extractor(RepoName, Subsystems)(log, opt)

	var workaround, testCmd, nav *memory.Asset
	for _, a := range assets {
		switch a.Kind {
		case memory.KindCommandWorkaround:
			workaround = a
		case memory.KindTestCommand:
			testCmd = a
		case memory.KindNav:
			nav = a
		}
	}
	if workaround == nil {
		t.Fatal("expected a command-workaround asset")
	}
	if workaround.Recommendation.Value != "grep -rn 'TokenFrequency' analysis/" {
		t.Fatalf("workaround must recommend the successful command, got %q", workaround.Recommendation.Value)
	}
	if workaround.Confidence != 0.70 {
		t.Fatalf("workaround confidence = %v, want 0.70", workaround.Confidence)
	}
	if testCmd == nil {
		t.Fatal("expected a test-command asset")
	}
	if testCmd.Recommendation.Value != "go test ./analysis/..." || testCmd.Confidence != memory.WhatWorkedBaseline {
		t.Fatalf("test-command asset wrong: %+v", testCmd.Recommendation)
	}
	if nav == nil {
		t.Fatal("expected a navigation asset")
	}
	if nav.Recommendation.Value != "analysis/freq.go" {
		t.Fatalf("navigation must point at the most-read file, got %q", nav.Recommendation.Value)
	}
	if nav.Service != "analysis" || nav.Environment != "main" {
		t.Fatalf("navigation scope wrong: %s/%s", nav.Service, nav.Environment)
	}
}

func TestCodingExtractorComplianceFailure(t *testing.T) {
	// A task that failed only on answer compliance still yields its command
	// experience — but not navigation.
	log := []memory.CallRecord{
		rec(1, "read_file", map[string]any{"path": "analysis/freq.go"}, true, ""),
		rec(2, "shell", map[string]any{"command": "go test ./analysis/..."}, true, ""),
	}
	opt := memory.ExtractOptions{
		SessionID: "s1", TaskID: "T1", TaskText: "In the analysis subsystem, find where token frequencies are computed",
		Service: "analysis", Environment: "main", Success: false,
	}
	assets := Extractor(RepoName, Subsystems)(log, opt)
	var testCmd, nav *memory.Asset
	for _, a := range assets {
		switch a.Kind {
		case memory.KindTestCommand:
			testCmd = a
		case memory.KindNav:
			nav = a
		}
	}
	if testCmd == nil {
		t.Fatal("compliance-failed task must still yield its test-command asset")
	}
	if nav != nil {
		t.Fatal("navigation must require task success")
	}
}

func TestCodingParser(t *testing.T) {
	p := Parser(Subsystems)
	call := llm.ToolCall{ID: "c1", Name: "shell", Args: map[string]any{"command": "go test ./index/scorch/... -run Rollback"}}
	parsed := p(call, "analysis")
	if parsed.Service != "scorch" {
		t.Fatalf("service = %q, want scorch", parsed.Service)
	}
	if parsed.Params["command"] != call.Args["command"] {
		t.Fatal("command must be preserved in params")
	}

	call = llm.ToolCall{ID: "c2", Name: "shell", Args: map[string]any{"command": "go test ./..."}}
	parsed = p(call, "analysis")
	if parsed.Service != "analysis" {
		t.Fatalf("fallback to task scope failed: %q", parsed.Service)
	}
}

func TestEnvironmentTools(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package foo\n\nfunc Bar() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewEnvironment(dir)

	res := env.Execute(llm.ToolCall{ID: "1", Name: "grep", Args: map[string]any{"pattern": "func Bar"}})
	if !res.OK || !containsStr(res.Text, "x.go:3") {
		t.Fatalf("grep failed: %+v %q", res, res.Text)
	}
	res = env.Execute(llm.ToolCall{ID: "2", Name: "grep", Args: map[string]any{"pattern": "["}})
	if res.OK || res.Cause != memory.CauseParameter {
		t.Fatalf("invalid pattern must be a parameter error: %+v", res)
	}
	res = env.Execute(llm.ToolCall{ID: "3", Name: "read_file", Args: map[string]any{"path": "x.go"}})
	if !res.OK || !containsStr(res.Text, "func Bar() {}") {
		t.Fatalf("read_file failed: %+v", res)
	}
	res = env.Execute(llm.ToolCall{ID: "4", Name: "read_file", Args: map[string]any{"path": "missing.go"}})
	if res.OK || res.Cause != memory.CauseNotFound {
		t.Fatalf("missing file must be not-found: %+v", res)
	}
	res = env.Execute(llm.ToolCall{ID: "5", Name: "list_files", Args: map[string]any{}})
	if !res.OK || !containsStr(res.Text, "x.go") {
		t.Fatalf("list_files failed: %+v", res)
	}
	res = env.Execute(llm.ToolCall{ID: "6", Name: "shell", Args: map[string]any{"command": "echo hello"}})
	if !res.OK || !containsStr(res.Text, "hello") {
		t.Fatalf("shell echo failed: %+v %q", res, res.Text)
	}
	res = env.Execute(llm.ToolCall{ID: "7", Name: "shell", Args: map[string]any{"command": "grep -P x x.go"}})
	if res.OK || res.Cause != memory.CauseNotFound {
		t.Fatalf("BSD grep -P must fail with not-found cause: %+v %q", res, res.Text)
	}
}

func TestShellMasksGoTestFailure(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package foo\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { t.Fatal(\"boom\") }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module foo\n\ngo 1.23\n"), 0o644)
	env := NewEnvironment(dir)

	// `go test | tail; echo …` masks the test failure behind the trailing
	// echo's exit 0; the harness must still classify it as a test failure.
	res := env.Execute(llm.ToolCall{ID: "1", Name: "shell", Args: map[string]any{
		"command": "go test ./... 2>&1 | tail -5; echo \"exit: ${PIPESTATUS[0]}\"",
	}})
	if res.OK || res.Cause != memory.CauseTestFail {
		t.Fatalf("masked go test failure must be classified: ok=%v cause=%q text=%q", res.OK, res.Cause, res.Text)
	}
	if !containsStr(res.Text, "[exit: 1]") {
		t.Fatalf("exit marker must report the unmasked failure: %q", res.Text)
	}

	// A passing go test behind the same pipeline must stay a success.
	os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package foo\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n"), 0o644)
	res = env.Execute(llm.ToolCall{ID: "2", Name: "shell", Args: map[string]any{
		"command": "go test ./... 2>&1 | tail -5; echo \"exit: ${PIPESTATUS[0]}\"",
	}})
	if !res.OK {
		t.Fatalf("passing go test must remain ok: %+v %q", res, res.Text)
	}
}

func containsStr(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
