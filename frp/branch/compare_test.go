package branch

import (
	"math"
	"reflect"
	"testing"
)

func testGroup(t *testing.T) ForkGroup {
	t.Helper()
	group, _, err := Fork(sourceFrame(), ForkRequest{ForkGroupID: "fork-1", BranchIDs: []string{"left", "right"}})
	if err != nil {
		t.Fatal(err)
	}
	return group
}

func trajectory(branchID string) Trajectory {
	return Trajectory{
		BranchID: branchID, EpisodeID: "episode-1", ObjectiveID: "objective-1", SourceFrameID: "source-frame",
		Attention: []string{"region-shared", "region-" + branchID}, Claims: []string{"claim-shared", "claim-" + branchID},
		Affordances:    []string{"affordance-shared", "affordance-" + branchID},
		Executions:     []string{"execution-shared", "execution-" + branchID},
		MissedEvidence: []string{"evidence-shared", "evidence-" + branchID},
		Cost:           100, Success: branchID == "left",
	}
}

func TestCompareCoversTrajectoryAndIsDeterministic(t *testing.T) {
	group := testGroup(t)
	left, right := trajectory("left"), trajectory("right")
	right.Cost = 135.5

	result, err := Compare(group, left, right)
	if err != nil {
		t.Fatal(err)
	}
	if result.ComparatorVersion != ComparatorVersion || result.ComparisonID == "" {
		t.Fatalf("missing result version or ID: %#v", result)
	}
	if result.CostDelta != 35.5 || !result.LeftSuccess || result.RightSuccess {
		t.Fatalf("incorrect scalar comparison: %#v", result)
	}
	comparisons := []SetComparison{result.Attention, result.Claims, result.Affordances, result.Executions, result.MissedEvidence}
	for _, comparison := range comparisons {
		if len(comparison.Shared) != 1 || len(comparison.OnlyLeft) != 1 || len(comparison.OnlyRight) != 1 {
			t.Fatalf("incomplete set comparison: %#v", comparison)
		}
	}

	left.Attention = []string{"region-left", "region-shared"}
	right.Attention = []string{"region-right", "region-shared"}
	second, err := Compare(group, left, right)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, second) {
		t.Fatalf("input ordering changed deterministic result:\n%#v\n%#v", result, second)
	}
}

func TestCompareRejectsIsolationViolations(t *testing.T) {
	group := testGroup(t)
	left, right := trajectory("left"), trajectory("right")

	right.EpisodeID = "foreign-episode"
	if _, err := Compare(group, left, right); err == nil {
		t.Fatal("foreign episode trajectory was accepted")
	}
	right = trajectory("not-in-group")
	if _, err := Compare(group, left, right); err == nil {
		t.Fatal("foreign branch trajectory was accepted")
	}
	right = trajectory("left")
	if _, err := Compare(group, left, right); err == nil {
		t.Fatal("same branch comparison was accepted")
	}
}

func TestTrajectoryRejectsInvalidValues(t *testing.T) {
	value := trajectory("left")
	value.Claims = []string{"claim-1", "claim-1"}
	if err := value.Validate(); err == nil {
		t.Fatal("duplicate trajectory IDs were accepted")
	}
	value = trajectory("left")
	value.Cost = math.NaN()
	if err := value.Validate(); err == nil {
		t.Fatal("NaN cost was accepted")
	}
}
