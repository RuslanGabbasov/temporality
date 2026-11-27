package workspace

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// SupervisorCaseKey pins the case identity: the verdict's journal event id
// WITHOUT its nano suffix — subjects carry slashes of their own, so only the
// last segment is the suffix. Acknowledgements already name cases this way,
// so the queue, the acks and the journal agree on what «one case» means.
func TestSupervisorCaseKey(t *testing.T) {
	require.Equal(t,
		"supervisor-case/knowledge_conflict/target-release/knowledge/60",
		SupervisorCaseKey("supervisor-case/knowledge_conflict/target-release/knowledge/60/1793851717689219946"))
	// A subject without slashes still works.
	require.Equal(t,
		"supervisor-case/skill_candidate/671c868bd12eef08",
		SupervisorCaseKey("supervisor-case/skill_candidate/671c868bd12eef08/1792251356164052819"))
	// Degenerate ids never panic and never collapse to empty.
	require.Equal(t, "plain", SupervisorCaseKey("plain"))
	require.Equal(t, "", SupervisorCaseKey(""))
}

// The noise budget (docs/supervisor-model.md §3): republishing-by-design
// families land in the digest lane, never in the attention queue.
func TestSupervisorDigestFamilies(t *testing.T) {
	require.True(t, supervisorDigestFamilies["knowledge_hygiene"])
	require.True(t, supervisorDigestFamilies["development_narrative"])
	require.False(t, supervisorDigestFamilies["knowledge_conflict"], "conflicts demand attention")
	require.False(t, supervisorDigestFamilies["skill_candidate"])
	require.False(t, supervisorDigestFamilies[""])
}

// UpsertSupervisorCaseSQLShape pins the upsert against two inference
// incidents: 42P08 «inconsistent types deduced» — the timestamp rode ONE
// parameter ($11) through three inference contexts — and, after the split,
// 42804 «column decided_at is of type timestamp with time zone but
// expression is of type text» — the CASE…ELSE NULL branch let Postgres
// conclude TEXT for $13. Each occurrence is now its own parameter AND each
// carries an explicit ::timestamptz cast, so no describe/protocol mode can
// re-run the inference lottery; the shape test keeps all of that forever.
func TestUpsertSupervisorCaseSQLShape(t *testing.T) {
	// The timestamp placeholders are distinct, each in exactly one position,
	// each explicitly timestamptz.
	require.Contains(t, supervisorCaseUpsertSQL, "1, $11::timestamptz, $12::timestamptz, $9,")
	require.Contains(t, supervisorCaseUpsertSQL, "THEN $13::timestamptz ELSE NULL END")
	// No reused timestamp parameter, in any of the old spellings.
	require.NotContains(t, supervisorCaseUpsertSQL, "$11, $11")
	require.NotContains(t, supervisorCaseUpsertSQL, "THEN $11 ELSE NULL")
	// The acknowledged-lane branches still resolve against the acks anchor
	// (three EXISTS subqueries) plus the last_event_id column — all TEXT
	// contexts, one parameter, no conflict.
	require.Equal(t, 5, strings.Count(supervisorCaseUpsertSQL, "$9"))
	// The conflict branch must self-heal the project (a case first folded from
	// a project-less verdict kept that empty project forever — the debugger's
	// invalidate buttons refused to fire on the row), keeping any known value
	// against an empty re-publish.
	require.Contains(t, supervisorCaseUpsertSQL,
		"project = COALESCE(NULLIF(EXCLUDED.project, ''), workspace_supervisor_case.project)")
}
