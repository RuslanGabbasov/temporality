package branch

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/temporality-project/temporality/frp/protocol"
)

type Trajectory struct {
	BranchID       string   `json:"branch_id"`
	EpisodeID      string   `json:"episode_id"`
	ObjectiveID    string   `json:"objective_id"`
	SourceFrameID  string   `json:"source_frame_id"`
	Attention      []string `json:"attention"`
	Claims         []string `json:"claims"`
	Affordances    []string `json:"affordances"`
	Executions     []string `json:"executions"`
	MissedEvidence []string `json:"missed_evidence"`
	Cost           float64  `json:"cost"`
	Success        bool     `json:"success"`
}

func (t Trajectory) Validate() error {
	if t.BranchID == "" || t.EpisodeID == "" || t.ObjectiveID == "" || t.SourceFrameID == "" {
		return errors.New("trajectory ownership IDs are required")
	}
	if t.Cost < 0 || math.IsNaN(t.Cost) || math.IsInf(t.Cost, 0) {
		return errors.New("trajectory cost must be finite and non-negative")
	}
	for name, values := range map[string][]string{
		"attention": t.Attention, "claims": t.Claims, "affordances": t.Affordances,
		"executions": t.Executions, "missed_evidence": t.MissedEvidence,
	} {
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			if value == "" {
				return fmt.Errorf("%s contains an empty ID", name)
			}
			if _, exists := seen[value]; exists {
				return fmt.Errorf("%s contains duplicate ID %q", name, value)
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

type SetComparison struct {
	Shared    []string `json:"shared"`
	OnlyLeft  []string `json:"only_left"`
	OnlyRight []string `json:"only_right"`
}

type ComparisonResult struct {
	Protocol          string        `json:"protocol"`
	Version           string        `json:"version"`
	ComparatorVersion string        `json:"comparator_version"`
	ComparisonID      string        `json:"comparison_id"`
	ForkGroupID       string        `json:"fork_group_id"`
	LeftBranchID      string        `json:"left_branch_id"`
	RightBranchID     string        `json:"right_branch_id"`
	Attention         SetComparison `json:"attention"`
	Claims            SetComparison `json:"claims"`
	Affordances       SetComparison `json:"affordances"`
	Executions        SetComparison `json:"executions"`
	MissedEvidence    SetComparison `json:"missed_evidence"`
	LeftCost          float64       `json:"left_cost"`
	RightCost         float64       `json:"right_cost"`
	CostDelta         float64       `json:"cost_delta"`
	LeftSuccess       bool          `json:"left_success"`
	RightSuccess      bool          `json:"right_success"`
}

// Compare computes an order-stable comparison. Left and right remain semantically significant.
func Compare(group ForkGroup, left, right Trajectory) (ComparisonResult, error) {
	if err := group.Validate(); err != nil {
		return ComparisonResult{}, err
	}
	if err := validateTrajectoryIsolation(group, left); err != nil {
		return ComparisonResult{}, fmt.Errorf("left trajectory: %w", err)
	}
	if err := validateTrajectoryIsolation(group, right); err != nil {
		return ComparisonResult{}, fmt.Errorf("right trajectory: %w", err)
	}
	if left.BranchID == right.BranchID {
		return ComparisonResult{}, errors.New("comparison requires two distinct branches")
	}
	result := ComparisonResult{
		Protocol: protocol.Name, Version: protocol.Version, ComparatorVersion: ComparatorVersion,
		ForkGroupID: group.ForkGroupID, LeftBranchID: left.BranchID, RightBranchID: right.BranchID,
		Attention:      compareSets(left.Attention, right.Attention),
		Claims:         compareSets(left.Claims, right.Claims),
		Affordances:    compareSets(left.Affordances, right.Affordances),
		Executions:     compareSets(left.Executions, right.Executions),
		MissedEvidence: compareSets(left.MissedEvidence, right.MissedEvidence),
		LeftCost:       left.Cost, RightCost: right.Cost, CostDelta: right.Cost - left.Cost,
		LeftSuccess: left.Success, RightSuccess: right.Success,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return ComparisonResult{}, err
	}
	result.ComparisonID = uuidFromDigest(sha256.Sum256(encoded))
	return result, nil
}

func validateTrajectoryIsolation(group ForkGroup, trajectory Trajectory) error {
	if err := trajectory.Validate(); err != nil {
		return err
	}
	if trajectory.EpisodeID != group.EpisodeID || trajectory.ObjectiveID != group.ObjectiveID || trajectory.SourceFrameID != group.SourceFrameID {
		return errors.New("trajectory crosses fork group ownership boundary")
	}
	for _, b := range group.Branches {
		if b.BranchID == trajectory.BranchID {
			return nil
		}
	}
	return fmt.Errorf("branch %q is not in fork group", trajectory.BranchID)
}

func compareSets(left, right []string) SetComparison {
	leftSet := make(map[string]struct{}, len(left))
	rightSet := make(map[string]struct{}, len(right))
	for _, value := range left {
		leftSet[value] = struct{}{}
	}
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	result := SetComparison{Shared: []string{}, OnlyLeft: []string{}, OnlyRight: []string{}}
	for value := range leftSet {
		if _, exists := rightSet[value]; exists {
			result.Shared = append(result.Shared, value)
		} else {
			result.OnlyLeft = append(result.OnlyLeft, value)
		}
	}
	for value := range rightSet {
		if _, exists := leftSet[value]; !exists {
			result.OnlyRight = append(result.OnlyRight, value)
		}
	}
	sort.Strings(result.Shared)
	sort.Strings(result.OnlyLeft)
	sort.Strings(result.OnlyRight)
	return result
}
