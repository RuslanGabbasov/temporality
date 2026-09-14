package branch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
)

type ForkRequest struct {
	ForkGroupID string   `json:"fork_group_id"`
	BranchIDs   []string `json:"branch_ids"`
}

// Fork deterministically creates branch-local root frames without mutating source.
func Fork(source frame.Frame, request ForkRequest) (ForkGroup, []frame.Frame, error) {
	if err := source.Validate(); err != nil {
		return ForkGroup{}, nil, fmt.Errorf("invalid source frame: %w", err)
	}
	if request.ForkGroupID == "" {
		return ForkGroup{}, nil, errors.New("fork_group_id is required")
	}
	if len(request.BranchIDs) < 2 {
		return ForkGroup{}, nil, errors.New("at least two branch_ids are required")
	}

	group := ForkGroup{
		Protocol: protocol.Name, Version: protocol.Version, ForkVersion: ForkVersion,
		ForkGroupID: request.ForkGroupID, EpisodeID: source.EpisodeID,
		ObjectiveID: source.ObjectiveID, SourceFrameID: source.FrameID,
		SourceBranchID: source.BranchID, Branches: make([]Branch, 0, len(request.BranchIDs)),
	}
	roots := make([]frame.Frame, 0, len(request.BranchIDs))
	seen := make(map[string]struct{}, len(request.BranchIDs))
	for _, branchID := range request.BranchIDs {
		if branchID == "" {
			return ForkGroup{}, nil, errors.New("branch_id is required")
		}
		if branchID == source.BranchID {
			return ForkGroup{}, nil, errors.New("fork branch must differ from source branch")
		}
		if _, exists := seen[branchID]; exists {
			return ForkGroup{}, nil, fmt.Errorf("duplicate branch_id %q", branchID)
		}
		seen[branchID] = struct{}{}

		root := cloneFrame(source)
		root.ParentFrameID = source.FrameID
		root.BranchID = branchID
		root.Revision = source.Revision + 1
		root.FrameID = forkRootID(source.FrameID, request.ForkGroupID, branchID)
		if err := root.Validate(); err != nil {
			return ForkGroup{}, nil, fmt.Errorf("invalid fork root: %w", err)
		}
		branch := Branch{
			Protocol: protocol.Name, Version: protocol.Version, BranchID: branchID,
			ForkGroupID: request.ForkGroupID, EpisodeID: source.EpisodeID,
			ObjectiveID: source.ObjectiveID, SourceFrameID: source.FrameID,
			RootFrameID: root.FrameID, HeadFrameID: root.FrameID, Status: StatusActive,
		}
		group.Branches = append(group.Branches, branch)
		roots = append(roots, root)
	}
	if err := group.Validate(); err != nil {
		return ForkGroup{}, nil, err
	}
	return group, roots, nil
}

func forkRootID(sourceFrameID, forkGroupID, branchID string) string {
	data, _ := json.Marshal(struct {
		Version       string `json:"fork_version"`
		SourceFrameID string `json:"source_frame_id"`
		ForkGroupID   string `json:"fork_group_id"`
		BranchID      string `json:"branch_id"`
	}{ForkVersion, sourceFrameID, forkGroupID, branchID})
	return uuidFromDigest(sha256.Sum256(data))
}

func cloneFrame(source frame.Frame) frame.Frame {
	result := source
	result.WorkingSet = append([]frame.Ref(nil), source.WorkingSet...)
	result.Filters.AgentIDs = append([]string(nil), source.Filters.AgentIDs...)
	result.Filters.RegionKinds = append([]string(nil), source.Filters.RegionKinds...)
	return result
}

func uuidFromDigest(sum [32]byte) string {
	raw := append([]byte(nil), sum[:16]...)
	raw[6] = (raw[6] & 0x0f) | 0x50
	raw[8] = (raw[8] & 0x3f) | 0x80
	h := hex.EncodeToString(raw)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
