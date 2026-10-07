package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/skills"
)

func TestAlignRows(t *testing.T) {
	lines := alignRows([][]string{
		{"ID", "VERSION"},
		{"deploy-service", "1.2.0"},
		{"x", "0.1.0"},
	})
	if len(lines) != 3 {
		t.Fatalf("lines: %v", lines)
	}
	// Columns pad to the widest cell; the last column is unpadded.
	if lines[0] != "ID              VERSION" {
		t.Fatalf("header: %q", lines[0])
	}
	if lines[1] != "deploy-service  1.2.0" {
		t.Fatalf("row: %q", lines[1])
	}
	if lines[2] != "x               0.1.0" {
		t.Fatalf("row: %q", lines[2])
	}
	for i, line := range lines {
		if line != strings.TrimRight(line, " ") {
			t.Fatalf("line %d has trailing whitespace: %q", i, line)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Fatalf("short: %q", got)
	}
	if got := truncate("hello world", 8); got != "hello w…" {
		t.Fatalf("long: %q", got)
	}
	// rune-safe, not byte-safe
	if got := truncate("тысяча", 3); got != "ты…" {
		t.Fatalf("multibyte: %q", got)
	}
}

func TestFormatTime(t *testing.T) {
	if got := formatTime(time.Time{}); got != "-" {
		t.Fatalf("zero: %q", got)
	}
	parsed := time.Date(2026, 10, 6, 18, 39, 51, 0, time.FixedZone("X", 2*3600))
	if got := formatTime(parsed); got != "2026-10-06 16:39" {
		t.Fatalf("utc: %q", got)
	}
	if got := formatMemoryTime("2026-10-03T10:13:18.555438Z"); got != "2026-10-03 10:13" {
		t.Fatalf("memory: %q", got)
	}
	if got := formatMemoryTime("garbage"); got != "garbage" {
		t.Fatalf("fallback: %q", got)
	}
}

func TestWrap(t *testing.T) {
	lines := wrap("aaa bbb ccc ddd", 7)
	if strings.Join(lines, "|") != "aaa bbb|ccc ddd" {
		t.Fatalf("lines: %v", lines)
	}
	if got := wrap("   ", 10); got != nil {
		t.Fatalf("empty: %v", got)
	}
}

func TestRuntimeSummary(t *testing.T) {
	got := runtimeSummary(skills.Runtime{
		Sandbox: "none", Network: "restricted",
		Filesystem: skills.FSAccess{Read: []string{"/workspace"}},
		MCP:        []string{"graphmap"},
	})
	want := "sandbox=none network=restricted mcp=graphmap read=/workspace"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := runtimeSummary(skills.Runtime{}); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestFieldsSummary(t *testing.T) {
	got := fieldsSummary(map[string]skills.Field{
		"b": {Type: "string", Required: true},
		"a": {Type: "object"},
	})
	if got != "a:object, b:string*" {
		t.Fatalf("got %q", got)
	}
	if got := fieldsSummary(nil); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestWriteSkillDetail(t *testing.T) {
	manifest, err := skills.ParseManifest(`
id: deploy-service
version: 1.2.0
name: Deploy service
description: Deploys a service.
capabilities: [build, deploy]
tools: [run_command]
runtime:
  sandbox: required
  network: restricted
`)
	if err != nil {
		t.Fatal(err)
	}
	s := skill{
		ID: "deploy-service", Name: "Deploy service", Version: "1.2.0", VersionStatus: "active",
		Description: "Deploys a service.",
		Markdown:    "## Purpose\nDeploys things.\n",
		Manifest:    skills.MarshalJSONForStorage(manifest),
	}
	var buf bytes.Buffer
	writeSkillDetail(&buf, s)
	out := buf.String()
	for _, want := range []string{
		"deploy-service — Deploy service",
		"1.2.0 (active)",
		"org unit:",
		"global",
		"contract (skill.yaml):",
		"capabilities:",
		"build, deploy",
		"sandbox=required network=restricted",
		"SKILL.md:",
		"Deploys things.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteSkillDetailContinuationAlignment(t *testing.T) {
	s := skill{
		ID: "x", Name: "X", Version: "1.0.0",
		Manifest: json.RawMessage(`{"id":"x"}`),
	}
	var buf bytes.Buffer
	writeSkillDetail(&buf, s)
	out := buf.String()
	// No manifest fields beyond id: contract renders just the id line.
	if !strings.Contains(out, "id:") {
		t.Fatalf("output missing id:\n%s", out)
	}
}

func TestWriteMemoryList(t *testing.T) {
	items := []skillMemoryItem{{
		KnowledgeID: "run/delegate/01/knowledge/51",
		Proposition: "GraphMap service was unavailable during this QA run: 4/4 calls failed.",
		Capability:  "resolve target map",
		Project:     "lighthouse",
		OccurredAt:  "2026-10-03T10:13:18Z",
		State:       "invalidated",
	}}
	var buf bytes.Buffer
	writeMemoryList(&buf, items)
	out := buf.String()
	if !strings.Contains(out, "invalidated  lighthouse  2026-10-03 10:13  resolve target map") {
		t.Fatalf("row: %s", out)
	}
	if !strings.Contains(out, "    GraphMap service was unavailable") {
		t.Fatalf("proposition: %s", out)
	}
}

func TestTailID(t *testing.T) {
	if got := tailID("run/delegate/01/knowledge/51"); got != "51" {
		t.Fatalf("got %q", got)
	}
	if got := tailID("plain"); got != "plain" {
		t.Fatalf("got %q", got)
	}
	if got := tailID("trailing/"); got != "trailing/" {
		t.Fatalf("got %q", got)
	}
}
