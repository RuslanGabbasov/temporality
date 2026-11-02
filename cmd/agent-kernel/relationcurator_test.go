package main

import (
	"context"
	"fmt"
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
	emitted          []relationVerdictRecord
	conflictsEmitted []conflictVerdictRecord
}

type relationVerdictRecord struct {
	project, knowledgeID, targetID string
	dismissed                      bool
	method, explanation            string
}

type conflictVerdictRecord struct {
	project, knowledgeID, targetID string
	confirmed                      bool
	method, explanation            string
}

func (f *curatorActivitiesFake) EmitRelationVerdict(_ context.Context, project, knowledgeID, targetID string, dismissed bool, method, explanation string, _ map[string]any) error {
	f.emitted = append(f.emitted, relationVerdictRecord{
		project: project, knowledgeID: knowledgeID, targetID: targetID,
		dismissed: dismissed, method: method, explanation: explanation,
	})
	return nil
}

func (f *curatorActivitiesFake) EmitContradictionVerdict(_ context.Context, project, knowledgeID, targetID string, confirmed bool, method, explanation string, _ map[string]any) error {
	f.conflictsEmitted = append(f.conflictsEmitted, conflictVerdictRecord{
		project: project, knowledgeID: knowledgeID, targetID: targetID,
		confirmed: confirmed, method: method, explanation: explanation,
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

// The contradiction queue drains through the pair question: «genuinely
// contradict or merely overlap?» — "no" dismisses the pair forever (a
// non-contradiction has no wrong side a human could act on), "yes" promotes
// it to an explicit contradicts edge.
func TestCurateContradictionsJudgesThePairQuestion(t *testing.T) {
	fake := &curatorActivitiesFake{}
	judgeCalls := 0
	verdicts := map[string]bool{} // the journal twin: verdicts persist between passes
	deps := relationCuratorDeps{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		fetchMeta: func(context.Context) ([]relationKnowledgeMeta, error) {
			return []relationKnowledgeMeta{
				metaItem("k1", "репозиторий проекта лежит в /workspace/omni-tfs", "git"),
				metaItem("k2", "ветка flaky-e2e не существует в origin", "git"),
				metaItem("k3", "git fetch требует явных учётных данных", "auth"),
				metaItem("k4", "git fetch без учётных данных отклоняется сервером", "auth"),
			}, nil
		},
		fetchTelemetry: func(context.Context) ([]hintEvent, error) { return nil, nil },
		fetchVerdicts:  func(context.Context) (map[string]bool, error) { return verdicts, nil },
		fetchConflicts: func(_ context.Context, project string) ([]conflictCandidatePair, error) {
			require.Equal(t, "proj", project)
			return []conflictCandidatePair{{A: "k1", B: "k2"}, {A: "k3", B: "k4"}}, nil
		},
		judgeConflict: func(_ context.Context, a, b relationKnowledgeMeta) (bool, string, error) {
			judgeCalls++
			if a.ID == "k1" {
				return false, "different subjects — both can hold", nil
			}
			return true, "one asserts what the other denies", nil
		},
	}
	sweep := relationCuratorSweep{deps: deps, emit: fake, conflicts: fake}
	sweep.curate(context.Background())

	require.Equal(t, 2, judgeCalls)
	require.Len(t, fake.conflictsEmitted, 2)
	byKey := map[string]conflictVerdictRecord{}
	for _, record := range fake.conflictsEmitted {
		byKey[pairKey(record.knowledgeID, record.targetID)] = record
	}
	dismissed := byKey[pairKey("k1", "k2")]
	require.False(t, dismissed.confirmed, "a non-contradiction must be dismissed, not promoted")
	require.Equal(t, "judge", dismissed.method)
	confirmed := byKey[pairKey("k3", "k4")]
	require.True(t, confirmed.confirmed, "a genuine contradiction must become an explicit edge")
	require.Equal(t, "judge", confirmed.method)

	// A second pass never re-judges a settled pair.
	sweep.curate(context.Background())
	require.Equal(t, 2, judgeCalls)
	require.Len(t, fake.conflictsEmitted, 2)
}

// Fail-open on judge errors: the pair stays a candidate for the next pass —
// a model hiccup must cost one tick, never a verdict.
func TestCurateContradictionsFailsOpenOnJudgeError(t *testing.T) {
	fake := &curatorActivitiesFake{}
	deps := relationCuratorDeps{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		fetchMeta: func(context.Context) ([]relationKnowledgeMeta, error) {
			return []relationKnowledgeMeta{
				metaItem("k1", "одно утверждение про ветку", "git"),
				metaItem("k2", "другое утверждение про ветку", "git"),
			}, nil
		},
		fetchTelemetry: func(context.Context) ([]hintEvent, error) { return nil, nil },
		fetchVerdicts:  func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil },
		fetchConflicts: func(context.Context, string) ([]conflictCandidatePair, error) {
			return []conflictCandidatePair{{A: "k1", B: "k2"}}, nil
		},
		judgeConflict: func(context.Context, relationKnowledgeMeta, relationKnowledgeMeta) (bool, string, error) {
			return false, "", fmt.Errorf("judge unavailable")
		},
	}
	sweep := relationCuratorSweep{deps: deps, emit: fake, conflicts: fake}
	sweep.curate(context.Background())
	require.Empty(t, fake.conflictsEmitted)
}
