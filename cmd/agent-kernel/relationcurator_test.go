package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
)

// The relation curator resolves candidate pairs without a human: usefulness
// confirms, the judge resolves, dismissals stick.

type curatorActivitiesFake struct {
	emitted []relationVerdictRecord
}

type relationVerdictRecord struct {
	project, knowledgeID, targetID string
	dismissed                      bool
	method, explanation            string
}

func (f *curatorActivitiesFake) EmitRelationVerdict(_ context.Context, project, knowledgeID, targetID string, dismissed bool, method, explanation string, _ map[string]any) error {
	f.emitted = append(f.emitted, relationVerdictRecord{
		project: project, knowledgeID: knowledgeID, targetID: targetID,
		dismissed: dismissed, method: method, explanation: explanation,
	})
	return nil
}

func curatorFixture(meta []relationKnowledgeMeta, telemetry []hintEvent, verdicts map[string]bool, judge func(ctx context.Context, a, b relationKnowledgeMeta) (bool, string, error)) (relationCuratorSweep, *curatorActivitiesFake) {
	fake := &curatorActivitiesFake{}
	deps := relationCuratorDeps{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		fetchMeta: func(context.Context) ([]relationKnowledgeMeta, error) {
			return meta, nil
		},
		fetchTelemetry: func(context.Context) ([]hintEvent, error) {
			return telemetry, nil
		},
		fetchVerdicts: func(context.Context) (map[string]bool, error) {
			return verdicts, nil
		},
		judge: judge,
	}
	return relationCuratorSweep{deps: deps, emit: fake}, fake
}

func metaItem(id, proposition string, topics ...string) relationKnowledgeMeta {
	return relationKnowledgeMeta{ID: id, Project: "proj", Proposition: proposition, Topics: topics, States: "confirmed"}
}

func TestCurateRelationsConfirmsByUsage(t *testing.T) {
	// Three co-offers, all graded helpful — the threshold. No judge needed:
	// the system bet on the relation three times and every graded outcome
	// agreed.
	telemetry := []hintEvent{
		{HintID: "h1", KnowledgeID: "k1", Outcome: "offered"},
		{HintID: "h1", KnowledgeID: "k2", Outcome: "offered"},
		{HintID: "h1", KnowledgeID: "k1", Outcome: "helpful"},
		{HintID: "h2", KnowledgeID: "k1", Outcome: "offered"},
		{HintID: "h2", KnowledgeID: "k2", Outcome: "offered"},
		{HintID: "h2", KnowledgeID: "k2", Outcome: "helpful"},
		{HintID: "h3", KnowledgeID: "k1", Outcome: "offered"},
		{HintID: "h3", KnowledgeID: "k2", Outcome: "offered"},
		{HintID: "h3", KnowledgeID: "k1", Outcome: "helpful"},
	}
	sweep, fake := curatorFixture(
		[]relationKnowledgeMeta{metaItem("k1", "деплой идёт через CI", "deploy"), metaItem("k2", "откат релиза — revert тегом", "deploy")},
		telemetry, map[string]bool{}, nil)
	sweep.curate(context.Background())

	require.Len(t, fake.emitted, 1)
	require.False(t, fake.emitted[0].dismissed)
	require.Equal(t, "hint_usage", fake.emitted[0].method)
}

func TestCurateRelationsDismissesHarmful(t *testing.T) {
	telemetry := []hintEvent{
		{HintID: "h1", KnowledgeID: "k1", Outcome: "offered"},
		{HintID: "h1", KnowledgeID: "k2", Outcome: "offered"},
		{HintID: "h1", KnowledgeID: "k1", Outcome: "harmful"},
		{HintID: "h2", KnowledgeID: "k1", Outcome: "offered"},
		{HintID: "h2", KnowledgeID: "k2", Outcome: "offered"},
		{HintID: "h2", KnowledgeID: "k2", Outcome: "harmful"},
	}
	sweep, fake := curatorFixture(
		[]relationKnowledgeMeta{metaItem("k1", "A", "topic"), metaItem("k2", "B", "topic")},
		telemetry, map[string]bool{}, nil)
	sweep.curate(context.Background())

	require.Len(t, fake.emitted, 1)
	require.True(t, fake.emitted[0].dismissed)
	require.Equal(t, "harmful_usage", fake.emitted[0].method)
}

func TestCurateRelationsJudgeResolvesStrongPairs(t *testing.T) {
	// k1~k2 was co-offered → judged and confirmed. k1~k3 / k2~k3 were never
	// co-offered → no model call, no verdict: pure topic noise does not earn
	// a judge.
	judged := 0
	judge := func(ctx context.Context, a, b relationKnowledgeMeta) (bool, string, error) {
		judged++
		return true, "one statement explains when to apply the other", nil
	}
	sweep, fake := curatorFixture(
		[]relationKnowledgeMeta{
			metaItem("k1", "A", "topic"), metaItem("k2", "B", "topic"), metaItem("k3", "C", "topic"),
		},
		[]hintEvent{
			{HintID: "h1", KnowledgeID: "k1", Outcome: "offered"},
			{HintID: "h1", KnowledgeID: "k2", Outcome: "offered"},
			{HintID: "h1", KnowledgeID: "k1", Outcome: "helpful"},
		},
		map[string]bool{}, judge)
	sweep.curate(context.Background())

	require.Equal(t, 1, judged)
	require.Len(t, fake.emitted, 1)
	require.False(t, fake.emitted[0].dismissed)
	require.Equal(t, "judge", fake.emitted[0].method)
}

func TestCurateRelationsVerdictsAreSticky(t *testing.T) {
	// The judge already dismissed this pair — never ask again.
	judge := func(ctx context.Context, a, b relationKnowledgeMeta) (bool, string, error) {
		t.Fatal("judged an already-settled pair")
		return false, "", nil
	}
	sweep, fake := curatorFixture(
		[]relationKnowledgeMeta{metaItem("k1", "A", "topic"), metaItem("k2", "B", "topic")},
		[]hintEvent{
			{HintID: "h1", KnowledgeID: "k1", Outcome: "offered"},
			{HintID: "h1", KnowledgeID: "k2", Outcome: "offered"},
			{HintID: "h1", KnowledgeID: "k1", Outcome: "helpful"},
		},
		map[string]bool{pairKey("k1", "k2"): true}, judge)
	sweep.curate(context.Background())
	require.Empty(t, fake.emitted)
}

// Pair keys are order-independent: a verdict recorded as a|b suppresses b|a.
func TestPairKeySymmetric(t *testing.T) {
	require.Equal(t, pairKey("a", "b"), pairKey("b", "a"))
}

func TestRelationRelatedPromptMentionsBoth(t *testing.T) {
	prompt := agent.RelationRelatedPrompt("первое", "второе")
	require.Contains(t, prompt, "первое")
	require.Contains(t, prompt, "второе")
}

// ── projection: verdicts are sticky in the knowledge base itself ─────────

func TestProjectKnowledgeRelationVerdicts(t *testing.T) {
	reviewEvent := func(eventType, id string) observation.Event {
		return observation.Event{
			Schema: observation.Schema, EventID: id, OccurredAt: time.Now().UTC(),
			Source:  observation.Source{ID: "s", Integration: "test", Version: "1"},
			Context: observation.Context{Project: "proj", Actor: observation.Actor{ID: "relation-curator", Type: "system"}},
			Type:    eventType,
			Data:    map[string]any{"knowledge_id": "k1", "target_id": "k2", "relation": "related_to", "method": "judge", "reason": "test"},
		}
	}
	items, err := observation.ProjectKnowledge([]observation.Event{
		proposedKnowledgeEvent("proj", "evt-1", "k1", "first statement"),
		proposedKnowledgeEvent("proj", "evt-2", "k2", "second statement"),
		reviewEvent("knowledge.relation_confirmed", "evt-3"),
		reviewEvent("knowledge.relation_dismissed", "evt-4"),
	})
	require.NoError(t, err)
	byID := map[string]observation.Knowledge{}
	for _, item := range items {
		byID[item.ID] = item
	}
	// Confirmed: an explicit edge on k1.
	require.Len(t, byID["k1"].Relationships, 1)
	require.Equal(t, "k2", byID["k1"].Relationships[0].TargetID)
	// Dismissed: recorded on BOTH endpoints, so a pair is suppressed from
	// whichever side an explore starts from.
	require.Len(t, byID["k1"].DismissedRelations, 1)
	require.Len(t, byID["k2"].DismissedRelations, 1)
}

// proposedKnowledgeEvent builds the proposal event the projection needs to
// create an item before transitions can apply.
func proposedKnowledgeEvent(project, eventID, knowledgeID, proposition string) observation.Event {
	return observation.Event{
		Schema: observation.Schema, EventID: eventID, OccurredAt: time.Now().UTC(),
		Source:  observation.Source{ID: "s", Integration: "test", Version: "1"},
		Context: observation.Context{Project: project, Actor: observation.Actor{ID: "agent", Type: "agent"}},
		Type:    "knowledge.proposed",
		Data: map[string]any{
			"knowledge_id": knowledgeID, "proposition": proposition,
			"topics": []string{"topic"}, "scope_kind": "project",
		},
	}
}
