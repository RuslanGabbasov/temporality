package procedure_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/procedure"
)

func evidence(id string, status execution.Status, arguments map[string]any) procedure.Evidence {
	now := time.Unix(1, 0).UTC()
	return procedure.Evidence{
		Execution: execution.Execution{ExecutionID: id, RequestID: "r-" + id, EpisodeID: "episode", AffordanceID: "run_test", Status: status, IntentPersistedAt: now, CreatedEventID: "event-" + id, StartedAt: &now, FinishedAt: &now},
		Request:   affordance.Request{RequestID: "r-" + id, EpisodeID: "episode", AffordanceID: "run_test", Arguments: arguments},
	}
}

func TestBuildIsOrderIndependent(t *testing.T) {
	one := evidence("one", execution.StatusCompleted, map[string]any{"suite": "unit"})
	two := evidence("two", execution.StatusFailed, map[string]any{"suite": "unit"})
	forward := procedure.Build("episode", []procedure.Evidence{one, two}, procedure.BuildConfig{MinimumEvidence: 2})
	reverse := procedure.Build("episode", []procedure.Evidence{two, one}, procedure.BuildConfig{MinimumEvidence: 2})
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("order changed projection: %#v != %#v", forward, reverse)
	}
	if len(forward) != 1 || !reflect.DeepEqual(forward[0].EvidenceExecutionIDs, []string{"one", "two"}) {
		t.Fatalf("unexpected projection: %#v", forward)
	}
}

func TestFailureLowersConfidenceAndInsufficientEvidenceIsExcluded(t *testing.T) {
	success := evidence("one", execution.StatusCompleted, map[string]any{})
	good := procedure.Build("episode", []procedure.Evidence{success, evidence("two", execution.StatusCompleted, map[string]any{})}, procedure.BuildConfig{MinimumEvidence: 2})
	poisoned := procedure.Build("episode", []procedure.Evidence{success, evidence("two", execution.StatusFailed, map[string]any{})}, procedure.BuildConfig{MinimumEvidence: 2})
	if len(good) != 1 || len(poisoned) != 1 || poisoned[0].SuccessRate >= good[0].SuccessRate {
		t.Fatalf("failure did not lower confidence: %#v %#v", good, poisoned)
	}
	if got := procedure.Build("episode", []procedure.Evidence{success}, procedure.BuildConfig{MinimumEvidence: 2}); len(got) != 0 {
		t.Fatalf("insufficient evidence included: %#v", got)
	}
}

func TestMatchingIsStable(t *testing.T) {
	values := procedure.Build("episode", []procedure.Evidence{evidence("one", execution.StatusCompleted, map[string]any{})}, procedure.BuildConfig{})
	goal := objective.Objective{Text: "Run test for release"}
	current := frame.Frame{Focus: frame.Focus{Type: frame.RefQuery, Query: "test suite"}}
	first := procedure.MatchProcedures(values, goal, current, procedure.MatchConfig{Threshold: 0.1})
	second := procedure.MatchProcedures(values, goal, current, procedure.MatchConfig{Threshold: 0.1})
	if len(first) != 1 || !reflect.DeepEqual(first, second) {
		t.Fatalf("unstable match: %#v %#v", first, second)
	}
	failed := procedure.Build("episode", []procedure.Evidence{evidence("one", execution.StatusFailed, map[string]any{})}, procedure.BuildConfig{})
	if procedure.MatchProcedures(failed, goal, current, procedure.MatchConfig{})[0].Score >= first[0].Score {
		t.Fatal("failed evidence did not lower match score")
	}
}
