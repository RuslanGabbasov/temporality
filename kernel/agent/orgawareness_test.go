package agent

// Org supervisor batch model-reply parsing tests
// (docs/temporality-organizational-operations-spec.md §6.4): the model's
// reply is normalized — decision kinds lower-cased, detail levels clamped to
// the §7.1 enum, texts capped — before the caller validates against the
// batch it actually built.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOrgBatchReplyNormalizes(t *testing.T) {
	verdict, err := parseOrgBatchReply(`{
		"decisions": [
			{"source_event_id": "src/ev-1", "target_run_id": "run-1", "decision": "NOTIFY",
			 "relevance": "HIGH", "reason": "  related work  ", "urgency": "High",
			 "suggested_lookup": {"run_id": "src-run", "detail_level": "VERBATIM"}},
			{"decision": "ignore"},
			{"source_event_id": "src/ev-2", "target_run_id": "run-2", "decision": "urgent_steer",
			 "message": "watch out"}
		]
	}`)
	require.NoError(t, err)
	require.Len(t, verdict.Decisions, 3)
	first := verdict.Decisions[0]
	require.Equal(t, "notify", first.Decision)
	require.Equal(t, "high", first.Relevance)
	require.Equal(t, "related work", first.Reason)
	require.Equal(t, "high", first.Urgency) // "High" normalizes to a valid urgency
	require.Equal(t, "summary", first.SuggestedLookup.DetailLevel)
	require.Equal(t, "urgent_steer", verdict.Decisions[2].Decision)
}

func TestParseOrgBatchReplyWrappers(t *testing.T) {
	verdict, err := parseOrgBatchReply("Here is my routing decision:\n\n```json\n{\"decisions\": [{\"decision\": \"ignore\"}]}\n```")
	require.NoError(t, err)
	require.Len(t, verdict.Decisions, 1)

	_, err = parseOrgBatchReply("no json at all")
	require.Error(t, err)
}

func TestOrgCapText(t *testing.T) {
	long := make([]rune, 50)
	for i := range long {
		long[i] = 'a'
	}
	require.Len(t, orgCapText(string(long), 10), 10)
	require.Len(t, orgCapText("short", 10), 5)
	require.Equal(t, "", orgCapText("  ", 10))
}
