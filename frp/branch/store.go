package branch

import (
	"context"
	"errors"

	"github.com/temporality-project/temporality/frp/protocol"
)

const (
	EventBranchCreated  = "branch.created"
	EventFrameForked    = "frame.forked"
	EventBranchCompared = "branch.compared"
)

var (
	ErrNotFound    = errors.New("branch object not found")
	ErrCASConflict = errors.New("branch head changed")
)

type Store interface {
	Fork(context.Context, string, ForkRequest) (ForkGroup, error)
	GetBranch(context.Context, string) (Branch, error)
	GetForkGroup(context.Context, string) (ForkGroup, error)
	UpdateHead(context.Context, string, string, string) (Branch, error)
	PutComparison(context.Context, ForkGroup, Trajectory, Trajectory) (ComparisonResult, error)
	GetComparison(context.Context, string) (ComparisonResult, error)
}

func validateForkEvent(event protocol.Event, eventType, episodeID, branchID string) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.Type != eventType || event.EpisodeID != episodeID || event.BranchID != branchID {
		return errors.New("invalid canonical branch event")
	}
	return nil
}
