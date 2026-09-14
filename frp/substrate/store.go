package substrate

import (
	"context"
	"errors"
	"time"

	"github.com/temporality-project/temporality/frp/protocol"
)

var ErrNotFound = errors.New("event not found")

// EventFilter defines a stable event stream boundary for replay.
type EventFilter struct {
	EpisodeID string
	BranchID  string
	AsOf      *time.Time
}

type EventStore interface {
	Append(context.Context, protocol.Event) error
	Get(context.Context, string) (protocol.Event, error)
	List(context.Context, EventFilter) ([]protocol.Event, error)
}
