package workspace

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestScanAgentCheckCaseRoundTrip round-trips one control example
// (docs/agent-evolution.md §2.4) through the shared projection and scan.
func TestScanAgentCheckCaseRoundTrip(t *testing.T) {
	created := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	values := sliceRow{int64(9), "agent-a", "how do I roll back?", []string{"rollback"}, []string{"DROP"},
		"run-42", "user:ruslan", created}
	c, err := scanAgentCheckCase(values)
	require.NoError(t, err)
	require.Equal(t, int64(9), c.ID)
	require.Equal(t, "agent-a", c.AgentID)
	require.Equal(t, "how do I roll back?", c.Input)
	require.Equal(t, []string{"rollback"}, c.MustContain)
	require.Equal(t, []string{"DROP"}, c.MustNotContain)
	require.Equal(t, "run-42", c.FromRunID)
	require.Equal(t, "user:ruslan", c.CreatedBy)
	require.Equal(t, created, c.CreatedAt)
}

// TestScanAgentCheckCaseEmptyExpectations: a case without expectations scans
// as empty lists, never nil.
func TestScanAgentCheckCaseEmptyExpectations(t *testing.T) {
	values := sliceRow{int64(1), "agent-a", "input", nil, nil, "", "", time.Now()}
	c, err := scanAgentCheckCase(values)
	require.NoError(t, err)
	require.NotNil(t, c.MustContain)
	require.NotNil(t, c.MustNotContain)
	require.Empty(t, c.MustContain)
}

// TestAddAgentCheckCaseRequiresInput: an empty input is refused as a conflict
// before any SQL runs — the store is never touched.
func TestAddAgentCheckCaseRequiresInput(t *testing.T) {
	s := &Store{}
	err := s.AddAgentCheckCase(context.Background(), &AgentCheckCase{AgentID: "agent-a"})
	require.True(t, errors.Is(err, ErrConflict))
}

// TestDeleteAgentCheckCaseScopedToAgent pins the agent-scoped delete at the
// source level (unit tests here have no database): a foreign id is a
// not-found, never someone else's delete.
func TestDeleteAgentCheckCaseScopedToAgent(t *testing.T) {
	raw, err := os.ReadFile("agentcheckcase.go")
	require.NoError(t, err)
	source := string(raw)

	require.Contains(t, source, "DELETE FROM workspace_agent_check_case WHERE id = $1 AND agent_id = $2")
	require.Contains(t, source, "ORDER BY created_at, id")
}
