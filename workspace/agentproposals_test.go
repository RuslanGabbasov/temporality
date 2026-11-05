package workspace

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestScanAgentProposalEvolutionColumns round-trips the evolution-v2 columns
// (docs/agent-evolution.md §2.2) through the shared projection and scan — the
// position-sensitive wiring every proposal query goes through.
func TestScanAgentProposalEvolutionColumns(t *testing.T) {
	created := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	decided := created.Add(time.Hour)
	values := sliceRow{int64(7), "agent-a", 3, []byte(`{"constraints":["x"]}`), "purpose",
		"problem", "change", "effect", []byte(`["run r-1"]`), "applied", "author", "gpt-x",
		created, decided, "boss", "user", "behaves calmer now", []byte(`{"run_id":"r-1","project":"p"}`), 4}
	p, err := scanAgentProposal(values)
	require.NoError(t, err)
	require.Equal(t, int64(7), p.ID)
	require.Equal(t, "user", p.Source)
	require.Equal(t, "behaves calmer now", p.ChangeSummary)
	require.Equal(t, json.RawMessage(`{"run_id":"r-1","project":"p"}`), p.Context)
	require.Equal(t, 4, p.AppliedVersion)
	require.Equal(t, []string{"run r-1"}, p.Evidence)
	require.Equal(t, []string{"x"}, p.Definition.Constraints)
	require.Equal(t, &decided, p.DecidedAt)
}

// TestScanAgentProposalNilContextAndDefaults: pre-v2 and pending rows scan
// cleanly — nil context stays absent, evidence stays a list.
func TestScanAgentProposalNilContextAndDefaults(t *testing.T) {
	now := time.Now()
	values := sliceRow{int64(1), "agent-a", 1, []byte(`{}`), "", "problem", "change", "",
		nil, "pending", "bot", "", now, nil, "", "agent", "", nil, 0}
	p, err := scanAgentProposal(values)
	require.NoError(t, err)
	require.Empty(t, p.Context)
	require.Empty(t, p.AppliedVersion)
	require.NotNil(t, p.Evidence)
	require.Nil(t, p.DecidedAt)
}

// TestCreateAgentProposalDefaultsSource pins the source default and the
// extended INSERT at the source level (unit tests here have no database):
// an empty source becomes "agent", and the v2 columns ride the INSERT.
func TestCreateAgentProposalDefaultsSource(t *testing.T) {
	raw, err := os.ReadFile("agentproposals.go")
	require.NoError(t, err)
	source := string(raw)

	require.Contains(t, source, `if p.Source == "" {`)
	require.Contains(t, source, `p.Source = "agent"`)
	require.Contains(t, source, "source, change_summary, context")
	require.Contains(t, source, "$12,$13,$14")
}

// TestApplyAgentProposalStampsVersionLineage guards the v2 apply contract at
// the source level: the version row is written as kind=proposal with the
// proposal's base version and change summary as lineage, and the proposal row
// is stamped with the version its change landed in (docs/agent-evolution.md §2.2).
func TestApplyAgentProposalStampsVersionLineage(t *testing.T) {
	raw, err := os.ReadFile("agentproposals.go")
	require.NoError(t, err)
	source := string(raw)

	require.Contains(t, source, `snapshot.Kind = "proposal"`)
	require.Contains(t, source, "snapshot.BaselineVersion = decided.BaseVersion")
	require.Contains(t, source, "snapshot.ChangeSummary = decided.ChangeSummary")
	require.Contains(t, source, "UPDATE workspace_agent_proposal SET applied_version = $2 WHERE id = $1")
	require.Contains(t, source, "decided.AppliedVersion = snapshot.Version")
}

// TestGetAgentProposalLooksUpByID pins the id-only lookup signature the v2
// handlers use (the returned AgentID scopes authorization).
func TestGetAgentProposalLooksUpByID(t *testing.T) {
	raw, err := os.ReadFile("agentproposals.go")
	require.NoError(t, err)
	require.Contains(t, string(raw),
		"func (s *Store) GetAgentProposal(ctx context.Context, id int64) (AgentProposal, error)")
}
