package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/temporality-project/temporality/skills"
)

// renderWidth is the target line width for wrapped human output.
const renderWidth = 100

func printLines(w io.Writer, lines []string) {
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

// alignRows renders rows as space-separated fixed-width columns; the last
// column is left unpadded so long values do not trail whitespace.
func alignRows(rows [][]string) []string {
	if len(rows) == 0 {
		return nil
	}
	cols := len(rows[0])
	widths := make([]int, cols)
	for _, row := range rows {
		for i := 0; i < cols && i < len(row); i++ {
			if w := len([]rune(row[i])); w > widths[i] {
				widths[i] = w
			}
		}
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		var b strings.Builder
		for i := 0; i < len(row); i++ {
			if i == cols-1 {
				b.WriteString(row[i])
				break
			}
			b.WriteString(row[i])
			if pad := widths[i] - len([]rune(row[i])) + 2; pad > 0 {
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
		lines = append(lines, b.String())
	}
	return lines
}

// dash replaces empty values in table cells.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func statusLabel(status string) string {
	return dash(status)
}

// formatTime renders timestamps as compact UTC dates.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// formatMemoryTime parses the server's RFC3339-ish timestamp strings.
func formatMemoryTime(value string) string {
	if value == "" {
		return "-"
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return formatTime(parsed)
	}
	return value
}

// truncate shortens text to max runes, marking cuts with an ellipsis.
func truncate(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// flatten collapses multiline text into a single line.
func flatten(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// wrap word-wraps text to width runes; empty text wraps to nothing.
func wrap(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	lines := make([]string, 0, 1)
	current := words[0]
	for _, word := range words[1:] {
		if len(current)+1+len(word) > width {
			lines = append(lines, current)
			current = word
			continue
		}
		current += " " + word
	}
	return append(lines, current)
}

// writeJSONOrFail emits machine-readable JSON; encoding cannot fail for the
// shapes involved, but a write error (broken pipe) exits with 1.
func writeJSONOrFail(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(stderr, "temporality: write JSON:", err)
		return 1
	}
	return 0
}

// field is one labeled value in the inspect output.
type field struct {
	name  string
	value string
}

// writeFields writes labeled values as a two-column block: "label: value"
// with wrapped continuation lines aligned under the value column.
func writeFields(w io.Writer, fields []field, indent int) {
	labelWidth := 0
	for _, f := range fields {
		if f.value == "" {
			continue
		}
		if n := len(f.name) + 1; n > labelWidth { // +1 for the colon
			labelWidth = n
		}
	}
	valueCol := labelWidth + 1
	valueWidth := renderWidth - indent - valueCol
	pad := strings.Repeat(" ", indent)
	for _, f := range fields {
		if f.value == "" {
			continue
		}
		label := f.name + ":"
		gap := valueCol - len(label)
		lines := []string{f.value}
		if valueWidth > 20 {
			lines = wrap(f.value, valueWidth)
		}
		for i, line := range lines {
			if i == 0 {
				fmt.Fprintf(w, "%s%s%s%s\n", pad, label, strings.Repeat(" ", gap), line)
				continue
			}
			fmt.Fprintf(w, "%s%s%s\n", pad, strings.Repeat(" ", valueCol), line)
		}
	}
}

// writeSkillDetail renders the full inspect view: identity, manifest
// contract and SKILL.md.
func writeSkillDetail(w io.Writer, s skill) {
	fmt.Fprintf(w, "%s — %s\n\n", s.ID, s.Name)
	writeFields(w, []field{
		{"version", s.Version + suffix(s.VersionStatus)},
		{"org unit", orgUnitLabel(s.OrgUnitID)},
		{"created", formatTime(s.CreatedAt) + " UTC"},
		{"updated", formatTime(s.UpdatedAt) + " UTC"},
		{"description", flatten(s.Description)},
	}, 0)

	if len(s.Manifest) != 0 {
		var manifest skills.Manifest
		if err := json.Unmarshal(s.Manifest, &manifest); err != nil {
			fmt.Fprintf(w, "\ncontract: undecodable manifest (%v)\n", err)
		} else {
			fmt.Fprintln(w, "\ncontract (skill.yaml):")
			writeManifest(w, manifest)
		}
	}

	fmt.Fprintln(w, "\nSKILL.md:")
	if s.Markdown == "" {
		fmt.Fprintln(w, "  (no markdown content)")
		return
	}
	fmt.Fprintln(w, s.Markdown)
}

func suffix(status string) string {
	if status == "" {
		return ""
	}
	return " (" + status + ")"
}

// orgUnitLabel renders the org binding; an empty binding means the skill is
// visible to the whole installation.
func orgUnitLabel(unit string) string {
	if unit == "" {
		return "global"
	}
	return unit
}

// writeManifest renders the machine contract fields that are set.
func writeManifest(w io.Writer, m skills.Manifest) {
	evaluationSuite := ""
	if m.Evaluation != nil {
		evaluationSuite = m.Evaluation.Suite
	}
	writeFields(w, []field{
		{"id", m.ID},
		{"name", m.Name},
		{"version", m.Version},
		{"description", flatten(m.Description)},
		{"capabilities", strings.Join(m.Capabilities, ", ")},
		{"tools", strings.Join(m.Tools, ", ")},
		{"inputs", fieldsSummary(m.Inputs)},
		{"outputs", fieldsSummary(m.Outputs)},
		{"runtime", runtimeSummary(m.Runtime)},
		{"preconditions", strings.Join(m.Preconditions, "; ")},
		{"postconditions", strings.Join(m.Postconditions, "; ")},
		{"evidence", strings.Join(m.Evidence.Required, "; ")},
		{"evaluation suite", evaluationSuite},
		{"inferred", strings.Join(m.Inferred, ", ")},
	}, 2)
}

// runtimeSummary compacts the runtime block into flag[=value] parts.
func runtimeSummary(r skills.Runtime) string {
	var parts []string
	if r.Sandbox != "" {
		parts = append(parts, "sandbox="+r.Sandbox)
	}
	if r.Network != "" {
		parts = append(parts, "network="+r.Network)
	}
	if len(r.Credentials) > 0 {
		parts = append(parts, "credentials="+strings.Join(r.Credentials, ","))
	}
	if len(r.MCP) > 0 {
		parts = append(parts, "mcp="+strings.Join(r.MCP, ","))
	}
	if len(r.Filesystem.Read) > 0 {
		parts = append(parts, "read="+strings.Join(r.Filesystem.Read, ","))
	}
	if len(r.Filesystem.Write) > 0 {
		parts = append(parts, "write="+strings.Join(r.Filesystem.Write, ","))
	}
	return strings.Join(parts, " ")
}

// fieldsSummary renders declared inputs/outputs as name:type lines with a
// marker for required fields.
func fieldsSummary(declared map[string]skills.Field) string {
	if len(declared) == 0 {
		return ""
	}
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	// Map iteration is random; sort for stable output.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		spec := declared[name]
		part := name
		if spec.Type != "" {
			part += ":" + spec.Type
		}
		if spec.Required {
			part += "*"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// writeMemoryList renders linked knowledge: one aligned row per item plus an
// indented, truncated proposition underneath.
func writeMemoryList(w io.Writer, items []skillMemoryItem) {
	rows := [][]string{{"K", "STATE", "PROJECT", "OCCURRED", "CAPABILITY"}}
	for _, item := range items {
		rows = append(rows, []string{
			truncate(tailID(item.KnowledgeID), 10),
			truncate(dash(item.State), 11),
			truncate(dash(item.Project), 16),
			formatMemoryTime(item.OccurredAt),
			dash(item.Capability),
		})
	}
	lines := alignRows(rows)
	fmt.Fprintln(w, lines[0])
	for i, item := range items {
		fmt.Fprintln(w, lines[i+1])
		fmt.Fprintf(w, "    %s\n", truncate(flatten(item.Proposition), renderWidth-4))
	}
}

// tailID returns the last path segment of an entity id (knowledge ids are
// long slash-separated paths).
func tailID(id string) string {
	if at := strings.LastIndex(id, "/"); at >= 0 && at+1 < len(id) {
		return id[at+1:]
	}
	return id
}
