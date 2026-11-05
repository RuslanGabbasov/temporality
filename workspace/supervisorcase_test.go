package workspace

import (
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
