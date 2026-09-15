package memory

import (
	"context"
	"errors"
	"sort"

	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/world"
)

// SaveWorld stores the latest state of a world and its canonical state event
// atomically. World state versions must strictly increase.
func (s *Store) SaveWorld(_ context.Context, value world.World, event protocol.Event) error {
	if err := world.ValidateSave(value, event); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[event.EventID]; exists {
		return errors.New("event already exists")
	}
	if current, exists := s.worlds[value.WorldID]; exists && value.StateVersion <= current.StateVersion {
		return world.ErrWorldStateStale
	}
	s.worlds[value.WorldID] = clone(value)
	s.putEvent(clone(event))
	return nil
}

func (s *Store) GetWorld(_ context.Context, id string) (world.World, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.worlds[id]
	if !ok {
		return world.World{}, world.ErrWorldNotFound
	}
	return clone(value), nil
}

func (s *Store) ListWorlds(_ context.Context) ([]world.World, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]world.World, 0, len(s.worlds))
	for _, id := range sortedWorldIDs(s.worlds) {
		result = append(result, clone(s.worlds[id]))
	}
	return result, nil
}

func sortedWorldIDs(worlds map[string]world.World) []string {
	ids := make([]string, 0, len(worlds))
	for id := range worlds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
