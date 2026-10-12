package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

type captureHitlOutbox struct {
	events []observation.Event
}

func (c *captureHitlOutbox) Enqueue(_ context.Context, event observation.Event) error {
	c.events = append(c.events, event)
	return nil
}

// TestEmitHitlVerdictRecordsEvent: a terminal transition with a verdict
// produces one hitl.verdict.recorded through the durable outbox with the
// §9 payload, and repeated emissions never share an event id (UnixNano).
func TestEmitHitlVerdictRecordsEvent(t *testing.T) {
	outbox := &captureHitlOutbox{}
	row := workspace.HumanRequest{
		ID: "run-1/turn/01/ask-1", RunID: "run-1", ProjectID: "proj", AgentID: "agent-a",
		AgentVersion: 4, Status: workspace.HumanStatusAnswered, Response: "yes",
		Options: []string{"yes", "no"}, AnsweredBy: "user:ivan",
	}
	emitHitlVerdict(context.Background(), outbox, row)
	emitHitlVerdict(context.Background(), outbox, row)
	require.Len(t, outbox.events, 2)
	require.NotEqual(t, outbox.events[0].EventID, outbox.events[1].EventID)

	event := outbox.events[0]
	require.Equal(t, "hitl.verdict.recorded", event.Type)
	require.Equal(t, "run-1", event.Context.Run)
	require.Equal(t, "proj", event.Context.Project)
	require.Equal(t, "user:ivan", event.Context.Actor.ID)
	require.Equal(t, "human", event.Context.Actor.Type)
	require.Equal(t, "run-1", event.Data["run_id"])
	require.Equal(t, "agent-a", event.Data["agent_id"])
	require.Equal(t, 4, event.Data["agent_version"])
	require.Equal(t, workspace.VerdictAccepted, event.Data["type"])
	require.Equal(t, workspace.VerdictSourceHumanRequest, event.Data["source"])
	require.Equal(t, "user:ivan", event.Data["answered_by"])
}

// TestEmitHitlVerdictSkipsVerdictlessTransitions: statuses without a verdict
// (§3.3) and a missing outbox never emit.
func TestEmitHitlVerdictSkipsVerdictlessTransitions(t *testing.T) {
	outbox := &captureHitlOutbox{}
	for _, status := range []string{
		workspace.HumanStatusPending, workspace.HumanStatusDelivered,
		workspace.HumanStatusExpired, workspace.HumanStatusCancelled,
	} {
		emitHitlVerdict(context.Background(), outbox, workspace.HumanRequest{ID: "op", Status: status})
	}
	emitHitlVerdict(context.Background(), nil, workspace.HumanRequest{
		ID: "op", Status: workspace.HumanStatusAnswered, Response: "yes", Options: []string{"yes"},
	})
	require.Empty(t, outbox.events)
}
