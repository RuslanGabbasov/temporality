package memory

import (
	"context"
	"errors"
	"sort"

	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/timetravel"
)

func (s *Store) CreateSnapshot(_ context.Context, value timetravel.Snapshot) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if value.Metadata.Through.EventSeq <= 0 {
		return errors.New("snapshot through event_seq must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.snapshots[value.Metadata.SnapshotID]; ok {
		return errors.New("snapshot already exists")
	}
	s.snapshots[value.Metadata.SnapshotID] = clone(value)
	return nil
}

func (s *Store) GetSnapshot(_ context.Context, id string) (timetravel.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.snapshots[id]
	if !ok {
		return value, substrate.ErrNotFound
	}
	value = clone(value)
	return value, value.Validate()
}

func (s *Store) SelectSnapshot(_ context.Context, episodeID string, target timetravel.EventCursor) (timetravel.Snapshot, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]timetravel.Snapshot, 0)
	for _, value := range s.snapshots {
		if value.Metadata.EpisodeID == episodeID && value.Metadata.Through.EventSeq <= target.EventSeq && value.Validate() == nil {
			values = append(values, clone(value))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Metadata.Through.EventSeq > values[j].Metadata.Through.EventSeq })
	if len(values) == 0 {
		return timetravel.Snapshot{}, false, nil
	}
	return values[0], true, nil
}

func (s *Store) GetReplayFrame(_ context.Context, id string) (frame.Frame, timetravel.EventCursor, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.frames[id]
	if !ok {
		return frame.Frame{}, timetravel.EventCursor{}, frame.ErrFrameNotFound
	}
	eventID := s.frameEvents[id]
	event := s.events[eventID]
	return copyFrame(value), timetravel.EventCursor{EventSeq: s.eventSeq[eventID], TransactionTime: event.TransactionTime, EventID: eventID}, nil
}

func (s *Store) ListEventsThrough(_ context.Context, episodeID, branchID string, target timetravel.EventCursor) ([]timetravel.CursorEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]timetravel.CursorEvent, 0)
	value, forked := s.branches[branchID]
	for id, event := range s.events {
		seq := s.eventSeq[id]
		visible := branchID == "" || event.BranchID == "" || event.BranchID == branchID
		if forked && event.BranchID == value.ParentBranchID && seq <= value.SourceCursor.EventSeq {
			visible = true
		}
		if seq <= target.EventSeq && event.EpisodeID == episodeID && visible {
			result = append(result, timetravel.CursorEvent{Cursor: timetravel.EventCursor{EventSeq: seq, TransactionTime: event.TransactionTime, EventID: id}, Event: clone(event)})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Cursor.EventSeq < result[j].Cursor.EventSeq })
	return result, nil
}

func (s *Store) BuildBlame(_ context.Context, rootID string, maxDepth int) (timetravel.BlameGraph, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes := make([]timetravel.BlameNode, 0)
	edges := make([]timetravel.BlameEdge, 0)
	seen := map[string]bool{}
	add := func(id string, kind timetravel.BlameNodeType) {
		if !seen[id] {
			seen[id] = true
			nodes = append(nodes, timetravel.BlameNode{ID: id, Type: kind})
		}
	}
	for id, claim := range s.claims {
		add(id, timetravel.BlameNodeClaim)
		add(claim.CreatedEvent, timetravel.BlameNodeEvent)
		edges = append(edges, timetravel.BlameEdge{From: claim.CreatedEvent, To: id, Type: timetravel.BlameEdgeProduced})
	}
	for _, relation := range s.relations {
		add(relation.EvidenceEvent, timetravel.BlameNodeEvent)
		add(relation.SourceClaim, timetravel.BlameNodeClaim)
		add(relation.DestinationClaim, timetravel.BlameNodeClaim)
		edges = append(edges, timetravel.BlameEdge{From: relation.EvidenceEvent, To: relation.SourceClaim, Type: timetravel.BlameEdgeDerivedFrom}, timetravel.BlameEdge{From: relation.EvidenceEvent, To: relation.DestinationClaim, Type: timetravel.BlameEdgeDerivedFrom})
	}
	return timetravel.BuildBlameGraph(rootID, nodes, edges, maxDepth)
}
