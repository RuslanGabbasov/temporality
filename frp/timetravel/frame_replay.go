package timetravel

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/temporality-project/temporality/frp/frame"
)

type FrameReplay struct {
	Frame             frame.Frame       `json:"frame"`
	Through           EventCursor       `json:"through"`
	Events            []CursorEvent     `json:"events"`
	Snapshot          *SnapshotMetadata `json:"snapshot,omitempty"`
	FrameHash         string            `json:"frame_hash"`
	DeterministicHash bool              `json:"deterministic_hash"`
}

// ReplayFrame is read-only by construction: its dependency exposes no append,
// executor, network, or other effectful operation.
func ReplayFrame(ctx context.Context, store ReplayStore, frameID string) (FrameReplay, error) {
	if frameID == "" {
		return FrameReplay{}, fmt.Errorf("frame_id is required")
	}
	value, through, err := store.GetReplayFrame(ctx, frameID)
	if err != nil {
		return FrameReplay{}, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return FrameReplay{}, err
	}
	hash, err := CanonicalJSONHash(encoded)
	if err != nil {
		return FrameReplay{}, err
	}
	events, err := store.ListEventsThrough(ctx, value.EpisodeID, value.BranchID, through)
	if err != nil {
		return FrameReplay{}, err
	}
	result := FrameReplay{Frame: value, Through: through, Events: events, FrameHash: hash, DeterministicHash: true}
	if snapshot, ok, selectErr := store.SelectSnapshot(ctx, value.EpisodeID, through); selectErr != nil {
		return FrameReplay{}, selectErr
	} else if ok {
		metadata := snapshot.Metadata
		result.Snapshot = &metadata
	}
	return result, nil
}
