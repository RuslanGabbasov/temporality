package workspace

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// fakeExecer records the SQL and arguments of one Exec call so tests can pin
// the statement shape of the shared row writers without a database.
type fakeExecer struct {
	sql  string
	args []any
}

func (f *fakeExecer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.sql, f.args = sql, args
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

// TestScanAgentVersionLineageColumns round-trips the lineage columns
// (docs/agent-evolution.md §2.1) through the shared projection and scan.
func TestScanAgentVersionLineageColumns(t *testing.T) {
	created := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	values := sliceRow{"agent-a", 5, []byte(`{"constraints":["keep calm"]}`), "purpose",
		"prompt body", "ai", "gpt-x", "author", created,
		"rollback", 3, 2, "Откат к версии 2"}
	v, err := scanAgentVersion(values)
	require.NoError(t, err)
	require.Equal(t, 5, v.Version)
	require.Equal(t, "rollback", v.Kind)
	require.Equal(t, 3, v.BaselineVersion)
	require.Equal(t, 2, v.RollbackOf)
	require.Equal(t, "Откат к версии 2", v.ChangeSummary)
	require.Equal(t, []string{"keep calm"}, v.Definition.Constraints)
}

// TestInsertAgentVersionRowWiresLineage pins the extended writer: the lineage
// columns ride the INSERT, an empty kind defaults to a manual edit and absent
// lineage stores NULL (0 reads back as absent through the COALESCE projection).
func TestInsertAgentVersionRowWiresLineage(t *testing.T) {
	fake := &fakeExecer{}
	v := AgentVersion{AgentID: "agent-a", Version: 2, Kind: "proposal",
		BaselineVersion: 1, ChangeSummary: "answers shorter", CreatedAt: time.Now()}
	require.NoError(t, insertAgentVersionRow(context.Background(), fake, v))

	require.Contains(t, fake.sql, "kind, baseline_version, rollback_of, change_summary")
	require.Contains(t, fake.sql, "ON CONFLICT (agent_id, version) DO NOTHING")
	require.Len(t, fake.args, 13)
	require.Equal(t, "proposal", fake.args[9])
	require.Equal(t, 1, fake.args[10])
	require.Nil(t, fake.args[11]) // rollback_of absent → NULL
	require.Equal(t, "answers shorter", fake.args[12])

	fake = &fakeExecer{}
	require.NoError(t, insertAgentVersionRow(context.Background(), fake, AgentVersion{AgentID: "a", Version: 1}))
	require.Equal(t, "edit", fake.args[9])
	require.Nil(t, fake.args[10])
	require.Nil(t, fake.args[11])
}

// TestRollbackVersionOfShape pins the rollback row shape
// (docs/agent-evolution.md §3.2): history is extended — the NEXT version
// number carries the target snapshot's content, kind=rollback points at the
// original and the baseline is the version the rollback replaced.
func TestRollbackVersionOfShape(t *testing.T) {
	now := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	snapshot := AgentVersion{
		AgentID: "agent-a", Version: 2,
		Definition:     AgentDefinition{Constraints: []string{"never touch prod"}},
		Description:    "purpose v2",
		CompiledPrompt: "prompt v2",
		PromptSource:   "ai",
		GeneratorModel: "gpt-x",
	}
	created := rollbackVersionOf(snapshot, 5, "ruslan", now)

	require.Equal(t, "agent-a", created.AgentID)
	require.Equal(t, 6, created.Version) // latest (5) + 1
	require.Equal(t, snapshot.Definition, created.Definition)
	require.Equal(t, "purpose v2", created.Description)
	require.Equal(t, "prompt v2", created.CompiledPrompt)
	require.Equal(t, "ai", created.PromptSource)
	require.Equal(t, "rollback", created.Kind)
	require.Equal(t, 2, created.RollbackOf)
	require.Equal(t, 5, created.BaselineVersion)
	require.Equal(t, "Откат к версии 2", created.ChangeSummary)
	require.Equal(t, "ruslan", created.Author)
	require.Equal(t, now, created.CreatedAt)
}

// TestRollbackAgentVersionIsTransactional pins the read-head-then-insert
// wiring at the source level: the head and the insert ride one transaction
// under the agent row lock (unit tests here have no database).
func TestRollbackAgentVersionIsTransactional(t *testing.T) {
	raw, err := os.ReadFile("agentversions.go")
	require.NoError(t, err)
	source := string(raw)

	require.Contains(t, source, "func (s *Store) RollbackAgentVersion(ctx context.Context, agentID string, toVersion int, author string) (AgentVersion, error)")
	require.Contains(t, source, "s.pool.Begin(ctx)")
	require.Contains(t, source, "SELECT COALESCE(MAX(version), 0) FROM workspace_agent_version WHERE agent_id = $1")
	require.Contains(t, source, "FOR UPDATE")
	require.Contains(t, source, "tx.Commit(ctx)")
}

// TestDefinitionDiffStructuralChanges: §7.2 field set — purpose, constraints,
// completion and capability names; prompt bodies never enter the diff.
func TestDefinitionDiffStructuralChanges(t *testing.T) {
	yes, no := true, false
	from := AgentVersion{
		Version:     3,
		Description: "Old purpose",
		Definition: AgentDefinition{
			Constraints:    []string{"never touch prod", "keep answers short"},
			Completion:     []string{"tests pass"},
			Capabilities:   AgentCapabilities{ModifyFiles: &yes, Delegation: &no},
			PromptOverride: "manual prompt body v3",
		},
	}
	to := AgentVersion{
		Version:     4,
		Description: "New purpose",
		Definition: AgentDefinition{
			Constraints:    []string{"keep answers short", "redact credentials"},
			Completion:     []string{"tests pass", "lint clean"},
			Capabilities:   AgentCapabilities{ModifyFiles: &no, Delegation: &yes},
			PromptOverride: "different manual body v4",
		},
	}
	changes := DefinitionDiff(from, to)
	byKey := map[string]DefinitionChange{}
	for _, change := range changes {
		byKey[change.Field+":"+change.Kind+":"+change.Value] = change
	}
	purpose := byKey["purpose:changed:New purpose"]
	require.Equal(t, "Old purpose", purpose.From)
	require.Contains(t, byKey, "constraints:removed:never touch prod")
	require.Contains(t, byKey, "constraints:added:redact credentials")
	require.NotContains(t, byKey, "constraints:removed:keep answers short")
	require.Contains(t, byKey, "completion:added:lint clean")
	require.Contains(t, byKey, "capabilities:removed:modify_files")
	require.Contains(t, byKey, "capabilities:added:delegation")
	for _, change := range changes {
		require.NotEqual(t, "prompt_override", change.Field, "prompt bodies stay out of the structural diff (§7.2)")
	}
}

// TestVersionDiffOfLineDiffAndRedaction: the §8.5 payload carries the
// changed prompt lines and masks credential-looking text before it leaves
// storage (§10.3).
func TestVersionDiffOfLineDiffAndRedaction(t *testing.T) {
	from := AgentVersion{
		Version:        3,
		Description:    "same",
		CompiledPrompt: "line one\nline two\nline three",
	}
	to := AgentVersion{
		Version:        4,
		Description:    "same",
		CompiledPrompt: "line one\nline two changed\nline three",
	}
	diff := VersionDiffOf(from, to)
	require.Equal(t, 3, diff.From)
	require.Equal(t, 4, diff.To)
	require.Equal(t, []string{"- line two", "+ line two changed"}, diff.PromptDiff)
	require.Empty(t, diff.DefinitionChanges)
	require.False(t, diff.Redacted)

	// §10.3: credential-looking text is masked in both diff layers.
	from.Definition.Constraints = []string{"use token=abc123 only in vault"}
	to.Definition.Constraints = []string{"use token=def456 only in vault"}
	from.CompiledPrompt = "line one\nset password=hunter2"
	to.CompiledPrompt = "line one\nset password=hunter3"
	diff = VersionDiffOf(from, to)
	require.True(t, diff.Redacted)
	require.Len(t, diff.DefinitionChanges, 2)
	kinds := map[string]DefinitionChange{}
	for _, change := range diff.DefinitionChanges {
		require.Equal(t, "constraints", change.Field)
		require.True(t, change.Redacted)
		kinds[change.Kind] = change
	}
	require.Equal(t, "use token=[REDACTED] only in vault", kinds[DefinitionChangeRemoved].Value)
	require.Equal(t, "use token=[REDACTED] only in vault", kinds[DefinitionChangeAdded].Value)
	require.Equal(t, []string{"- set password=[REDACTED]", "+ set password=[REDACTED]"}, diff.PromptDiff)
}

// TestLineDiffChangedLinesOnly: pure line-diff behaviour — unchanged lines
// never appear, removals precede additions at a replacement site.
func TestLineDiffChangedLinesOnly(t *testing.T) {
	require.Empty(t, LineDiff("a\nb", "a\nb"))
	require.Equal(t, []string{"- b", "+ c"}, LineDiff("a\nb", "a\nc"))
	require.Equal(t, []string{"+ b"}, LineDiff("a", "a\nb"))
	require.Equal(t, []string{"- a", "- b", "+ x"}, LineDiff("a\nb", "x"))
	require.Equal(t, []string{"- b"}, LineDiff("a\nb", "a"))
}
