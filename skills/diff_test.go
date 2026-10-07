package skills

import (
	"strings"
	"testing"
)

func TestDiffManifestsDetectsAddedAndRemoved(t *testing.T) {
	from := Manifest{
		Description:  "Reviews pull requests",
		Capabilities: []string{"inspect-diff", "report-findings"},
		Tools:        []string{"run_command"},
	}
	to := Manifest{
		Description:  "Reviews pull requests and merge queues",
		Capabilities: []string{"inspect-diff", "run-tests"},
		Tools:        []string{"run_command", "repository"},
		Runtime:      Runtime{Sandbox: "required"},
	}
	entries := DiffManifests(from, to)
	joined := strings.Builder{}
	for _, e := range entries {
		joined.WriteString(e.Field + "|" + e.From + "|" + e.To + "\n")
	}
	for _, want := range []string{
		"description|Reviews pull requests|Reviews pull requests and merge queues\n",
		"capabilities|(none)|+ run-tests\n",
		"capabilities|- report-findings|(none)\n",
		"tools|(none)|+ repository\n",
		"runtime.sandbox||required\n",
	} {
		if !strings.Contains(joined.String(), want) {
			t.Errorf("diff missing entry %q in:\n%s", want, joined.String())
		}
	}
}

func TestDiffManifestsIdenticalIsEmpty(t *testing.T) {
	m := Manifest{Capabilities: []string{"a"}, Tools: []string{"t"}}
	if entries := DiffManifests(m, m); len(entries) != 0 {
		t.Fatalf("identical manifests produced %d entries: %+v", len(entries), entries)
	}
}

func TestDiffMarkdownCounts(t *testing.T) {
	from := "line one\nline two\nline three"
	to := "line one\nline two edited\nline three\nline four"
	added, removed := DiffMarkdown(from, to)
	if added != 2 || removed != 1 {
		t.Fatalf("DiffMarkdown = +%d -%d, want +2 -1", added, removed)
	}
}

func TestRenderDiffIncludesMarkdownCounts(t *testing.T) {
	entries := []DiffEntry{{Field: "tools", From: "(none)", To: "+ repository"}}
	out := RenderDiff("1.0.0", "1.1.0", entries, 3, 1)
	for _, want := range []string{"1.0.0 → 1.1.0", "tools: (none) → + repository", "SKILL.md: +3 -1 lines"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
	if out := RenderDiff("1.0.0", "1.0.0", nil, 0, 0); !strings.Contains(out, "no manifest or SKILL.md changes") {
		t.Errorf("empty diff render = %q", out)
	}
}
