package workspace

// Live-database round-trips for the agent evolution v2 data layer
// (docs/agent-evolution.md §2): the scan-level unit tests cannot see SQL and
// scan-shape bugs, so the create/list/get contracts run against a real
// migrated schema here. Skipped unless TEMPORALITY_TEST_DATABASE_URL points
// at one (same contract as artifactlifecycle_live_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAgentEvolutionV2Live(t *testing.T) {
	dsn := os.Getenv("TEMPORALITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEMPORALITY_TEST_DATABASE_URL not set")
	}
	store, err := Open(context.Background(), dsn)
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	agentID := fmt.Sprintf("agent-evolution-live-%d", time.Now().UnixNano())
	require.NoError(t, store.CreateAgent(ctx, &Agent{
		ID: agentID, Name: "evolution live", Model: "test-model", DefinitionVersion: 1,
	}))
	defer func() { _ = store.DeleteAgent(ctx, agentID) }()

	v1 := AgentVersion{
		AgentID: agentID, Version: 1, Description: "purpose v1",
		CompiledPrompt: "prompt v1", PromptSource: "manual", Author: "tester",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.InsertAgentVersion(ctx, v1))

	t.Run("proposal create/list/get/apply", func(t *testing.T) {
		p := &AgentProposal{
			AgentID: agentID, BaseVersion: 1,
			Definition:    AgentDefinition{Constraints: []string{"be brief"}},
			Problem:       "the agent writes essays",
			Change:        "cap the answer length",
			ChangeSummary: "answers become one paragraph",
			Source:        "", // must default to agent
			Context:       json.RawMessage(`{"run_id":"run-1","project":"proj-1"}`),
		}
		require.NoError(t, store.CreateAgentProposal(ctx, p))
		require.NotZero(t, p.ID)
		require.Equal(t, "agent", p.Source)

		got, err := store.GetAgentProposal(ctx, p.ID)
		require.NoError(t, err)
		require.Equal(t, "agent", got.Source)
		require.Equal(t, "answers become one paragraph", got.ChangeSummary)
		require.Equal(t, json.RawMessage(`{"run_id":"run-1","project":"proj-1"}`), got.Context)
		require.Zero(t, got.AppliedVersion)

		list, err := store.ListAgentProposals(ctx, agentID, "pending", 50)
		require.NoError(t, err)
		require.NotEmpty(t, list)
		require.Equal(t, p.ID, list[0].ID)
		require.Equal(t, "answers become one paragraph", list[0].ChangeSummary)

		next, err := store.GetAgent(ctx, agentID)
		require.NoError(t, err)
		snapshot := AgentVersion{
			AgentID: agentID, Version: 2, Description: "purpose v2",
			CompiledPrompt: "prompt v2", PromptSource: "ai", CreatedAt: time.Now().UTC(),
		}
		decided, err := store.ApplyAgentProposal(ctx, next, p.ID, "tester", &snapshot)
		require.NoError(t, err)
		require.Equal(t, "applied", decided.Status)
		require.Equal(t, 2, decided.AppliedVersion)

		got, err = store.GetAgentProposal(ctx, p.ID)
		require.NoError(t, err)
		require.Equal(t, 2, got.AppliedVersion)

		versions, err := store.ListAgentVersions(ctx, agentID)
		require.NoError(t, err)
		require.NotEmpty(t, versions)
		require.Equal(t, 2, versions[0].Version)
		require.Equal(t, "proposal", versions[0].Kind)
		require.Equal(t, 1, versions[0].BaselineVersion)
		require.Equal(t, "answers become one paragraph", versions[0].ChangeSummary)
	})

	t.Run("rollback", func(t *testing.T) {
		created, err := store.RollbackAgentVersion(ctx, agentID, 1, "tester")
		require.NoError(t, err)
		require.Equal(t, 3, created.Version) // head (2) + 1
		require.Equal(t, "rollback", created.Kind)
		require.Equal(t, 1, created.RollbackOf)
		require.Equal(t, 2, created.BaselineVersion)
		require.Equal(t, "Откат к версии 1", created.ChangeSummary)
		require.Equal(t, "purpose v1", created.Description) // content of v1
		require.Equal(t, "prompt v1", created.CompiledPrompt)

		stored, err := store.GetAgentVersion(ctx, agentID, 3)
		require.NoError(t, err)
		require.Equal(t, created.Kind, stored.Kind)
		require.Equal(t, created.RollbackOf, stored.RollbackOf)

		_, err = store.RollbackAgentVersion(ctx, agentID, 99, "tester")
		require.True(t, errors.Is(err, ErrNotFound))
	})

	t.Run("metrics upsert/list", func(t *testing.T) {
		day := time.Date(2026, 11, 1, 15, 45, 0, 0, time.UTC)
		m := AgentVersionMetrics{AgentID: agentID, Version: 2, Day: day,
			Runs: 3, Succeeded: 2, Failed: 1, Tokens: 41000, CostUSD: 0.21}
		require.NoError(t, store.UpsertAgentVersionMetrics(ctx, m))
		m.Runs, m.Succeeded, m.Failed = 4, 3, 1 // a re-run overwrites, never accumulates
		require.NoError(t, store.UpsertAgentVersionMetrics(ctx, m))

		rows, err := store.ListAgentVersionMetrics(ctx, agentID, 2, day.AddDate(0, 0, -1))
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, 4, rows[0].Runs)
		require.Equal(t, 3, rows[0].Succeeded)
		require.Equal(t, int64(41000), rows[0].Tokens)
	})

	t.Run("check cases", func(t *testing.T) {
		c := &AgentCheckCase{
			AgentID: agentID, Input: "how do I roll back?",
			MustContain: []string{"rollback"}, MustNotContain: []string{"DROP"},
			FromRunID: "run-1", CreatedBy: "tester",
		}
		require.NoError(t, store.AddAgentCheckCase(ctx, c))
		require.NotZero(t, c.ID)

		list, err := store.ListAgentCheckCases(ctx, agentID)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, c.Input, list[0].Input)
		require.Equal(t, []string{"rollback"}, list[0].MustContain)
		require.Equal(t, []string{"DROP"}, list[0].MustNotContain)

		require.NoError(t, store.DeleteAgentCheckCase(ctx, agentID, c.ID))
		err = store.DeleteAgentCheckCase(ctx, agentID, c.ID)
		require.True(t, errors.Is(err, ErrNotFound))

		err = store.AddAgentCheckCase(ctx, &AgentCheckCase{AgentID: agentID})
		require.True(t, errors.Is(err, ErrConflict))
	})

	t.Run("observation window", func(t *testing.T) {
		require.NoError(t, store.SetAgentObservation(ctx, agentID, ObservationConfig{Days: 14, MinRuns: 5}))
		cfg, err := store.AgentObservation(ctx, agentID)
		require.NoError(t, err)
		require.Equal(t, ObservationConfig{Days: 14, MinRuns: 5}, cfg)

		err = store.SetAgentObservation(ctx, agentID, ObservationConfig{Days: 0, MinRuns: 5})
		require.True(t, errors.Is(err, ErrConflict))
	})
}
