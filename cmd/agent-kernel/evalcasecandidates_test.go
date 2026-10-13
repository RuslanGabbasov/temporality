package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

type captureCandidateOutbox struct {
	events []observation.Event
}

func (c *captureCandidateOutbox) Enqueue(_ context.Context, event observation.Event) error {
	c.events = append(c.events, event)
	return nil
}

// TestEmitEvalCaseCandidateCreated: a new candidate produces one
// eval.case_candidate.created with the §9 payload, and repeated emissions
// never share an event id (UnixNano, §8).
func TestEmitEvalCaseCandidateCreated(t *testing.T) {
	outbox := &captureCandidateOutbox{}
	candidate := workspace.EvalCaseCandidate{
		ID: 7, AgentID: "agent-a", RunID: "run-1", ProjectID: "proj",
		Source: workspace.EvalCandidateSourceSucceededRun, RedactionApplied: true,
	}
	emitEvalCaseCandidateCreated(context.Background(), outbox, candidate)
	emitEvalCaseCandidateCreated(context.Background(), outbox, candidate)
	require.Len(t, outbox.events, 2)
	require.NotEqual(t, outbox.events[0].EventID, outbox.events[1].EventID)

	event := outbox.events[0]
	require.Equal(t, "eval.case_candidate.created", event.Type)
	require.Equal(t, "proj", event.Context.Project)
	require.Equal(t, "run-1", event.Context.Run)
	require.Equal(t, "agent-a", event.Data["agent_id"])
	require.Equal(t, "run-1", event.Data["run_id"])
	require.Equal(t, workspace.EvalCandidateSourceSucceededRun, event.Data["source"])
	require.Equal(t, true, event.Data["redaction_applied"])
}

// TestEmitEvalCaseCandidateDecided: an accept/reject decision produces
// eval.case_candidate.decided with the §9 payload and a human actor.
func TestEmitEvalCaseCandidateDecided(t *testing.T) {
	outbox := &captureCandidateOutbox{}
	reviewedAt := time.Now()
	candidate := workspace.EvalCaseCandidate{
		ID: 7, AgentID: "agent-a", RunID: "run-1", ProjectID: "proj",
		Verdict: workspace.EvalCandidateVerdictAccepted, ReviewedBy: "user:ivan",
		ReviewedAt: &reviewedAt,
	}
	emitEvalCaseCandidateDecided(context.Background(), outbox, candidate)
	require.Len(t, outbox.events, 1)

	event := outbox.events[0]
	require.Equal(t, "eval.case_candidate.decided", event.Type)
	require.Equal(t, "user:ivan", event.Context.Actor.ID)
	require.Equal(t, "human", event.Context.Actor.Type)
	require.Equal(t, int64(7), event.Data["candidate_id"])
	require.Equal(t, workspace.EvalCandidateVerdictAccepted, event.Data["verdict"])
	require.Equal(t, "user:ivan", event.Data["reviewed_by"])
}

// TestEmitCandidateEventsTolerateMissingOutbox: emission is best-effort —
// a missing sink never panics the caller.
func TestEmitCandidateEventsTolerateMissingOutbox(t *testing.T) {
	candidate := workspace.EvalCaseCandidate{ID: 1, RunID: "run-1"}
	emitEvalCaseCandidateCreated(context.Background(), nil, candidate)
	emitEvalCaseCandidateDecided(context.Background(), nil, candidate)
}
