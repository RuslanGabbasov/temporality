// Package memory provides an in-memory observation.Store for tests and
// single-binary deployments that do not want a Postgres dependency.
package memory

import (
	"context"
	"reflect"
	"sort"
	"sync"

	"github.com/temporality-project/temporality/observation"
)

type Store struct {
	mu                sync.RWMutex
	observationEvents map[string]observation.Event
}

func New() *Store {
	return &Store{observationEvents: make(map[string]observation.Event)}
}

func observationKey(sourceID, eventID string) string { return sourceID + "\x00" + eventID }

func stringDataValue(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return value
}

func containsStringValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Store) AppendObservation(_ context.Context, event observation.Event) (bool, error) {
	if err := event.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := observationKey(event.Source.ID, event.EventID)
	if previous, exists := s.observationEvents[key]; exists {
		event.ReceivedAt = previous.ReceivedAt
		if reflect.DeepEqual(previous, event) {
			return false, nil
		}
		return false, observation.ErrConflict
	}
	s.observationEvents[key] = event
	return true, nil
}

func (s *Store) GetObservation(_ context.Context, sourceID, eventID string) (observation.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	event, ok := s.observationEvents[observationKey(sourceID, eventID)]
	if !ok {
		return observation.Event{}, observation.ErrNotFound
	}
	return event, nil
}

func (s *Store) ListObservations(ctx context.Context, filter observation.Filter) ([]observation.Event, error) {
	page, err := s.ListObservationPage(ctx, filter, "")
	return page.Events, err
}

func (s *Store) listObservationsAll(filter observation.Filter) []observation.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]observation.Event, 0)
	for _, event := range s.observationEvents {
		if filter.Project != "" && event.Context.Project != filter.Project ||
			filter.SourceID != "" && event.Source.ID != filter.SourceID ||
			filter.EventID != "" && event.EventID != filter.EventID ||
			filter.Run != "" && event.Context.Run != filter.Run ||
			filter.Task != "" && event.Context.Task != filter.Task ||
			filter.Actor != "" && event.Context.Actor.ID != filter.Actor ||
			filter.Type != "" && event.Type != filter.Type ||
			filter.ScopeKind != "" && stringDataValue(event.Data, "scope_kind") != filter.ScopeKind ||
			len(filter.ScopeIDs) > 0 && !containsStringValue(filter.ScopeIDs, stringDataValue(event.Data, "scope_id")) ||
			len(filter.KnowledgeIDs) > 0 && !containsStringValue(filter.KnowledgeIDs, stringDataValue(event.Data, "knowledge_id")) ||
			filter.Since != nil && event.OccurredAt.Before(*filter.Since) ||
			filter.Until != nil && event.OccurredAt.After(*filter.Until) ||
			filter.KnownAt != nil && event.ReceivedAt.After(*filter.KnownAt) {
			continue
		}
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].OccurredAt.Before(result[j].OccurredAt)
		}
		if !result[i].ReceivedAt.Equal(result[j].ReceivedAt) {
			return result[i].ReceivedAt.Before(result[j].ReceivedAt)
		}
		if result[i].Source.ID != result[j].Source.ID {
			return result[i].Source.ID < result[j].Source.ID
		}
		return result[i].EventID < result[j].EventID
	})
	return result
}

func (s *Store) ListObservationPage(ctx context.Context, filter observation.Filter, encodedCursor string) (observation.Page, error) {
	limit, err := observation.NormalizeLimit(filter.Limit)
	if err != nil {
		return observation.Page{}, err
	}
	cursor, err := observation.DecodeCursor(encodedCursor)
	if err != nil {
		return observation.Page{}, err
	}
	events := s.listObservationsAll(filter)
	if cursor != nil {
		start := 0
		for start < len(events) && !observation.AfterCursor(events[start], *cursor) {
			start++
		}
		events = events[start:]
	}
	page := observation.Page{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		page.NextCursor = observation.EncodeCursor(page.Events[len(page.Events)-1])
	}
	return page, nil
}
