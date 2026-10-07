package skills

import (
	"fmt"
	"strings"
)

// DiffEntry is one structured difference between two manifests.
type DiffEntry struct {
	Field string `json:"field"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// DiffManifests compares two manifests field by field (docs/living-skills.md
// §24 skill.diff): description, capabilities, tools, runtime, conditions,
// evidence and evaluation references. Memory is deliberately not part of a
// skill diff — it is a separate temporal layer.
func DiffManifests(from, to Manifest) []DiffEntry {
	var entries []DiffEntry
	if from.Description != to.Description {
		entries = append(entries, DiffEntry{Field: "description", From: from.Description, To: to.Description})
	}
	entries = append(entries, diffStringSets("capabilities", from.Capabilities, to.Capabilities)...)
	entries = append(entries, diffStringSets("tools", from.Tools, to.Tools)...)
	if r := diffRuntime(from.Runtime, to.Runtime); len(r) > 0 {
		entries = append(entries, r...)
	}
	entries = append(entries, diffStringSets("preconditions", from.Preconditions, to.Preconditions)...)
	entries = append(entries, diffStringSets("postconditions", from.Postconditions, to.Postconditions)...)
	if fmt.Sprint(from.Evidence.Required) != fmt.Sprint(to.Evidence.Required) {
		entries = append(entries, DiffEntry{Field: "evidence.required", From: strings.Join(from.Evidence.Required, ", "), To: strings.Join(to.Evidence.Required, ", ")})
	}
	fromSuite, toSuite := "", ""
	if from.Evaluation != nil {
		fromSuite = from.Evaluation.Suite
	}
	if to.Evaluation != nil {
		toSuite = to.Evaluation.Suite
	}
	if fromSuite != toSuite {
		entries = append(entries, DiffEntry{Field: "evaluation.suite", From: fromSuite, To: toSuite})
	}
	return entries
}

func diffStringSets(field string, from, to []string) []DiffEntry {
	fromSet := make(map[string]bool, len(from))
	for _, v := range from {
		fromSet[v] = true
	}
	toSet := make(map[string]bool, len(to))
	for _, v := range to {
		toSet[v] = true
	}
	var added, removed []string
	for _, v := range to {
		if !fromSet[v] {
			added = append(added, v)
		}
	}
	for _, v := range from {
		if !toSet[v] {
			removed = append(removed, v)
		}
	}
	var entries []DiffEntry
	if len(added) > 0 {
		entries = append(entries, DiffEntry{Field: field, From: "(none)", To: "+ " + strings.Join(added, ", + ")})
	}
	if len(removed) > 0 {
		entries = append(entries, DiffEntry{Field: field, From: "- " + strings.Join(removed, ", - "), To: "(none)"})
	}
	return entries
}

func diffRuntime(from, to Runtime) []DiffEntry {
	var entries []DiffEntry
	if from.Sandbox != to.Sandbox {
		entries = append(entries, DiffEntry{Field: "runtime.sandbox", From: from.Sandbox, To: to.Sandbox})
	}
	if from.Network != to.Network {
		entries = append(entries, DiffEntry{Field: "runtime.network", From: from.Network, To: to.Network})
	}
	entries = append(entries, diffStringSets("runtime.credentials", from.Credentials, to.Credentials)...)
	entries = append(entries, diffStringSets("runtime.mcp", from.MCP, to.MCP)...)
	return entries
}

// DiffMarkdown renders a compact unified summary of SKILL.md changes: counts
// of added/removed lines. The full texts stay available through version rows.
func DiffMarkdown(from, to string) (added, removed int) {
	fromLines := strings.Split(from, "\n")
	toLines := strings.Split(to, "\n")
	fromSet := make(map[string]int, len(fromLines))
	for _, line := range fromLines {
		fromSet[line]++
	}
	toSet := make(map[string]int, len(toLines))
	for _, line := range toLines {
		toSet[line]++
	}
	for line, count := range toSet {
		if extra := count - fromSet[line]; extra > 0 {
			added += extra
		}
	}
	for line, count := range fromSet {
		if extra := count - toSet[line]; extra > 0 {
			removed += extra
		}
	}
	return added, removed
}

// RenderDiff turns diff entries into the compact text the skill_diff tool
// returns to the model.
func RenderDiff(fromVersion, toVersion string, entries []DiffEntry, markdownAdded, markdownRemoved int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s → %s\n", fromVersion, toVersion)
	if len(entries) == 0 && markdownAdded == 0 && markdownRemoved == 0 {
		b.WriteString("no manifest or SKILL.md changes\n")
		return b.String()
	}
	for _, e := range entries {
		if e.From == "" && e.To != "" {
			fmt.Fprintf(&b, "%s: (none) → %s\n", e.Field, e.To)
		} else if e.To == "" && e.From != "" {
			fmt.Fprintf(&b, "%s: %s → (none)\n", e.Field, e.From)
		} else {
			fmt.Fprintf(&b, "%s: %s → %s\n", e.Field, e.From, e.To)
		}
	}
	if markdownAdded > 0 || markdownRemoved > 0 {
		fmt.Fprintf(&b, "SKILL.md: +%d -%d lines\n", markdownAdded, markdownRemoved)
	}
	return b.String()
}
