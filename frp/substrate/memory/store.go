package memory

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
)

type Store struct {
	mu sync.RWMutex
	events map[string]protocol.Event
}

func New() *Store { return &Store{events: make(map[string]protocol.Event)} }

func (s *Store) Append(_ context.Context, event protocol.Event) error {
	if err := event.Validate(); err != nil { return err }
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[event.EventID]; exists { return errors.New("event already exists") }
	s.events[event.EventID] = event
	return nil
}

func (s *Store) Get(_ context.Context, id string) (protocol.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	event, ok := s.events[id]
	if !ok { return protocol.Event{}, substrate.ErrNotFound }
	return event, nil
}

func (s *Store) List(_ context.Context, filter substrate.EventFilter) ([]protocol.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]protocol.Event, 0)
	for _, event := range s.events {
		if filter.EpisodeID != "" && event.EpisodeID != filter.EpisodeID { continue }
		if filter.BranchID != "" && event.BranchID != filter.BranchID { continue }
		if filter.AsOf != nil && event.ValidTime.After(*filter.AsOf) { continue }
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].ValidTime.Equal(result[j].ValidTime) { return result[i].ValidTime.Before(result[j].ValidTime) }
		if !result[i].TransactionTime.Equal(result[j].TransactionTime) { return result[i].TransactionTime.Before(result[j].TransactionTime) }
		return result[i].EventID < result[j].EventID
	})
	return result, nil
}
