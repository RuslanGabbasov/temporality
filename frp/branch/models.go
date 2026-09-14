package branch

import (
	"errors"
	"fmt"

	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
)

const (
	ForkVersion       = "fork-0.3.1"
	ComparatorVersion = "branch-comparator-0.3.1"
)

type Status string

const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusAbandoned Status = "abandoned"
)

type Branch struct {
	Protocol      string `json:"protocol"`
	Version       string `json:"version"`
	BranchID      string `json:"branch_id"`
	ForkGroupID   string `json:"fork_group_id"`
	EpisodeID     string `json:"episode_id"`
	ObjectiveID   string `json:"objective_id"`
	SourceFrameID string `json:"source_frame_id"`
	RootFrameID   string `json:"root_frame_id"`
	HeadFrameID   string `json:"head_frame_id"`
	Status        Status `json:"status"`
}

func (b Branch) Validate() error {
	if b.Protocol != protocol.Name || b.Version != protocol.Version {
		return fmt.Errorf("unsupported protocol version %q/%q", b.Protocol, b.Version)
	}
	if b.BranchID == "" || b.ForkGroupID == "" || b.EpisodeID == "" || b.ObjectiveID == "" || b.SourceFrameID == "" || b.RootFrameID == "" || b.HeadFrameID == "" {
		return errors.New("branch identity and frame IDs are required")
	}

	switch b.Status {
	case StatusActive, StatusCompleted, StatusFailed, StatusAbandoned:
		return nil
	default:
		return fmt.Errorf("invalid branch status %q", b.Status)
	}
}

type ForkGroup struct {
	Protocol       string   `json:"protocol"`
	Version        string   `json:"version"`
	ForkVersion    string   `json:"fork_version"`
	ForkGroupID    string   `json:"fork_group_id"`
	EpisodeID      string   `json:"episode_id"`
	ObjectiveID    string   `json:"objective_id"`
	SourceFrameID  string   `json:"source_frame_id"`
	SourceBranchID string   `json:"source_branch_id"`
	Branches       []Branch `json:"branches"`
}

func (g ForkGroup) Validate() error {
	if g.Protocol != protocol.Name || g.Version != protocol.Version {
		return fmt.Errorf("unsupported protocol version %q/%q", g.Protocol, g.Version)
	}
	if g.ForkVersion != ForkVersion {
		return fmt.Errorf("unsupported fork version %q", g.ForkVersion)
	}
	if g.ForkGroupID == "" || g.EpisodeID == "" || g.ObjectiveID == "" || g.SourceFrameID == "" || g.SourceBranchID == "" {
		return errors.New("fork group identity and source IDs are required")
	}
	if len(g.Branches) < 2 {
		return errors.New("fork group requires at least two branches")
	}
	seen := make(map[string]struct{}, len(g.Branches))
	for _, b := range g.Branches {
		if err := b.Validate(); err != nil {
			return err
		}
		if b.ForkGroupID != g.ForkGroupID || b.EpisodeID != g.EpisodeID || b.ObjectiveID != g.ObjectiveID || b.SourceFrameID != g.SourceFrameID {
			return fmt.Errorf("branch %q is not isolated within its fork group", b.BranchID)
		}
		if b.BranchID == g.SourceBranchID {
			return errors.New("fork branch must differ from source branch")
		}
		if _, exists := seen[b.BranchID]; exists {
			return fmt.Errorf("duplicate branch_id %q", b.BranchID)
		}
		seen[b.BranchID] = struct{}{}
	}
	return nil
}

// ValidateFrameIsolation ensures a frame can only be attached to its owning branch.
func ValidateFrameIsolation(b Branch, candidate frame.Frame) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("invalid frame: %w", err)
	}
	if candidate.BranchID != b.BranchID || candidate.EpisodeID != b.EpisodeID || candidate.ObjectiveID != b.ObjectiveID {
		return errors.New("frame crosses branch ownership boundary")
	}
	return nil
}
