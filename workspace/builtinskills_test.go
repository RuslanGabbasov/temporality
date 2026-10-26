package workspace

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/skills"
)

// agentTools is the toolset an agent can actually call (KernelTools + the
// sandbox/file/browser tools). A builtin manifest may only reference tools
// from this set — a manifest naming a phantom tool would promise a
// capability the runtime cannot deliver (docs/living-skills.md §9).
var agentTools = map[string]bool{
	"run_command": true, "read_file": true, "write_file": true, "edit_file": true,
	"view_image": true, "fetch_image": true, "browser": true, "send_file": true,
	"ask_human": true, "human_contacts": true, "request_approval": true,
	"artifact_create": true, "artifact_read": true, "artifact_search": true,
	"artifact_submit": true, "artifact_update": true,
	"remember": true, "remember_many": true,
	"skill_search": true, "skill_inspect": true,
	"knowledge_lookup": true, "knowledge_explore": true,
	"delegate": true, "plan": true,
}

func TestBuiltinSkillsCatalogSanity(t *testing.T) {
	list := BuiltinSkills()
	require.NotEmpty(t, list)
	ids := map[string]bool{}
	for _, bs := range list {
		require.False(t, ids[bs.ID], "duplicate builtin skill id %s", bs.ID)
		ids[bs.ID] = true

		require.Equal(t, bs.ID, bs.Manifest.ID, "%s: manifest id must match the catalog id", bs.ID)
		require.NotEmpty(t, bs.Version)
		require.NotEmpty(t, bs.Name)
		require.NotEmpty(t, bs.Description)

		// The manifest must be a valid contract and round-trip through the
		// same parse path run start uses (resolveAgentSkills).
		require.Empty(t, bs.Manifest.Validate(), "%s: manifest issues", bs.ID)
		raw, err := json.Marshal(bs.Manifest)
		require.NoError(t, err)
		parsed, err := skills.ParseManifest(string(raw))
		require.NoError(t, err, "%s: stored manifest must parse", bs.ID)
		require.Equal(t, bs.Manifest.ID, parsed.ID)

		// SKILL.md carries the canonical sections (docs/living-skills.md §5).
		for _, section := range []string{"## Purpose", "## When to use", "## Procedure", "## Constraints"} {
			require.True(t, strings.Contains(bs.Markdown, section), "%s: missing %s", bs.ID, section)
		}

		// Tools and runtime: only real tools; sandbox-bound, no open network.
		require.NotEmpty(t, bs.Manifest.Tools, "%s: must declare tools", bs.ID)
		for _, tool := range bs.Manifest.Tools {
			require.True(t, agentTools[tool], "%s: manifest references unknown tool %q", bs.ID, tool)
		}
		require.Equal(t, "required", bs.Manifest.Runtime.Sandbox, "%s: office skills run in the sandbox", bs.ID)
		require.NotEqual(t, "open", bs.Manifest.Runtime.Network, "%s: network must not be open", bs.ID)
		require.NotEmpty(t, bs.Manifest.Capabilities, "%s: must declare capabilities", bs.ID)
		require.NotEmpty(t, bs.Manifest.Evidence.Required, "%s: must require evidence", bs.ID)
	}

	// The office pack the platform promises (docs/builtin-skills.md §2).
	for _, expected := range []string{"pdf-processing", "word-processing", "spreadsheet-analysis", "presentation-authoring", "document-review", "document-generation", "archive-management"} {
		require.Contains(t, ids, expected)
	}
}

// The review skill must not declare write tools — review reports facts and
// Every builtin skill ships bundled tooling in the "## Bundled files"
// appendix (same format the import pipeline produces), so the editor's file
// tab, its risk scanner and skill_inspect treat them alike. The appendix must
// carry at least one script and keep its fences balanced.
func TestBuiltinSkillsBundleScripts(t *testing.T) {
	for _, bs := range BuiltinSkills() {
		require.Contains(t, bs.Markdown, "## Bundled files", "%s: expected bundled scripts", bs.ID)
		require.Contains(t, bs.Markdown, "### scripts/", "%s: expected script entries", bs.ID)
		appendix := bs.Markdown[strings.Index(bs.Markdown, "## Bundled files"):]
		require.Equal(t, strings.Count(appendix, "```"), strings.Count(appendix, "```"))
		// A fenced block must never be left unterminated: pairs of fences per
		// appendix are even.
		require.Zero(t, strings.Count(appendix, "```")%2, "%s: unbalanced code fences", bs.ID)
	}
}

// The document-review skill must not declare write tools — review reports
// facts and never edits the reviewed documents (its own Constraints section).
func TestDocumentReviewDeclaresNoWriteTools(t *testing.T) {
	for _, bs := range BuiltinSkills() {
		if bs.ID != "document-review" {
			continue
		}
		for _, tool := range bs.Manifest.Tools {
			require.NotContains(t, []string{"write_file", "edit_file"}, tool,
				"document-review must stay read-only over the reviewed documents")
		}
	}
}

// The seed's version-table insert must pass EMPTY ARRAYS for the NOT NULL
// text[] provenance columns, never bare NULL — a NULL violates the constraint
// and once left the installation with only the first catalog skill seeded
// (the loop aborted on the first version insert). Guarded at the source
// level: unit tests here have no database.
func TestSeedBuiltinSkillProvenanceArraysAreEmptyNotNULL(t *testing.T) {
	raw, err := os.ReadFile("builtinskills.go")
	require.NoError(t, err)
	source := string(raw)

	insertStart := strings.Index(source, "INSERT INTO workspace_skill_version")
	require.Greater(t, insertStart, -1, "expected the version insert statement")
	insertEnd := strings.Index(source[insertStart:], "ON CONFLICT")
	require.Greater(t, insertEnd, -1)
	statement := source[insertStart : insertStart+insertEnd]

	require.Contains(t, statement, "$7, $7, $7",
		"source_runs/evidence_refs/knowledge_ids must share one empty-array parameter")
	require.NotRegexp(t, `VALUES[^"]*\bNULL\b`, statement,
		"version insert must not pass bare NULL into NOT NULL columns")
	require.Contains(t, source, "textArray(nil)",
		"the empty-array parameter must come from the store's own textArray convention")
}
