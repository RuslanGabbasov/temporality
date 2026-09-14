package timetravel

import (
	"context"

	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
)

// CursorEvent preserves the existing protocol.Event API while exposing its
// durable database order for time-travel reads.
type CursorEvent struct {
	Cursor EventCursor    `json:"cursor"`
	Event  protocol.Event `json:"event"`
}

type SnapshotStore interface {
	CreateSnapshot(context.Context, Snapshot) error
	GetSnapshot(context.Context, string) (Snapshot, error)
	SelectSnapshot(context.Context, string, EventCursor) (Snapshot, bool, error)
}

type ReplayStore interface {
	SnapshotStore
	GetReplayFrame(context.Context, string) (frame.Frame, EventCursor, error)
	ListEventsThrough(context.Context, string, string, EventCursor) ([]CursorEvent, error)
}

type ProvenanceStore interface {
	BuildBlame(context.Context, string, int) (BlameGraph, error)
}
