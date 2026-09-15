package world

import (
	"context"

	"github.com/temporality-project/temporality/frp/protocol"
)

// Store persists versioned world descriptions. The worlds table is a
// projection: the registered/state_updated events embedded in the event log
// carry the full snapshot and remain the canonical source.
type Store interface {
	SaveWorld(context.Context, World, protocol.Event) error
	GetWorld(context.Context, string) (World, error)
	ListWorlds(context.Context) ([]World, error)
}

// ValidateSave enforces the state machine shared by all store adapters:
// version 1 registers a new world, later versions must strictly increase.
func ValidateSave(value World, event protocol.Event) error {
	if err := value.Validate(); err != nil {
		return err
	}
	return ValidateStateEvent(event, value)
}

// NextStateVersion is a helper for clients bumping a world state.
func NextStateVersion(current World) int { return current.StateVersion + 1 }
