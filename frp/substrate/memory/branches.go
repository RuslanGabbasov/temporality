package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/temporality-project/temporality/frp/branch"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/timetravel"
)

func (s *Store) Fork(_ context.Context, sourceFrameID string, request branch.ForkRequest) (branch.ForkGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.forkGroups[request.ForkGroupID]; exists {
		return branch.ForkGroup{}, errors.New("fork group already exists")
	}
	source, exists := s.frames[sourceFrameID]
	if !exists {
		return branch.ForkGroup{}, frame.ErrFrameNotFound
	}
	group, roots, err := branch.Fork(source, request)
	if err != nil {
		return branch.ForkGroup{}, err
	}
	eventID := s.frameEvents[sourceFrameID]
	sourceEvent := s.events[eventID]
	cursor := timetravel.EventCursor{EventSeq: s.eventSeq[eventID], EventID: eventID, TransactionTime: sourceEvent.TransactionTime}
	group.SourceCursor = cursor
	for i := range group.Branches {
		group.Branches[i].SourceCursor = cursor
		created := branchEvent(branch.EventBranchCreated, group, group.Branches[i].BranchID, group.Branches[i], time.Time{})
		forked := branchEvent(branch.EventFrameForked, group, group.Branches[i].BranchID, roots[i], time.Time{})
		if _, ok := s.branches[group.Branches[i].BranchID]; ok {
			return branch.ForkGroup{}, errors.New("branch already exists")
		}
		if _, ok := s.events[created.EventID]; ok {
			return branch.ForkGroup{}, errors.New("event already exists")
		}
		if _, ok := s.events[forked.EventID]; ok {
			return branch.ForkGroup{}, errors.New("event already exists")
		}
		if _, ok := s.frames[roots[i].FrameID]; ok {
			return branch.ForkGroup{}, frame.ErrFrameExists
		}
	}
	now := time.Now().UTC()
	for i := range group.Branches {
		created := branchEvent(branch.EventBranchCreated, group, group.Branches[i].BranchID, group.Branches[i], now)
		forked := branchEvent(branch.EventFrameForked, group, group.Branches[i].BranchID, roots[i], now)
		s.putEvent(created)
		s.putEvent(forked)
		s.frames[roots[i].FrameID] = copyFrame(roots[i])
		s.frameEvents[roots[i].FrameID] = forked.EventID
		s.branches[group.Branches[i].BranchID] = clone(group.Branches[i])
	}
	s.forkGroups[group.ForkGroupID] = clone(group)
	return clone(group), nil
}

func branchEvent(kind string, group branch.ForkGroup, branchID string, payload any, at time.Time) protocol.Event {
	data, _ := json.Marshal(struct{ Kind, Group, Branch string }{kind, group.ForkGroupID, branchID})
	sum := sha256.Sum256(data)
	raw := append([]byte(nil), sum[:16]...)
	raw[6] = raw[6]&0x0f | 0x50
	raw[8] = raw[8]&0x3f | 0x80
	h := hex.EncodeToString(raw)
	id := h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	encoded, _ := json.Marshal(payload)
	var body map[string]any
	_ = json.Unmarshal(encoded, &body)
	return protocol.Event{Protocol: protocol.Name, Version: protocol.Version, EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: group.EpisodeID, BranchID: branchID, Type: kind, Payload: body, Provenance: map[string]any{"source": "temporality-branch-store"}}
}

func (s *Store) GetBranch(_ context.Context, id string) (branch.Branch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.branches[id]
	if !ok {
		return v, branch.ErrNotFound
	}
	return clone(v), nil
}
func (s *Store) GetForkGroup(_ context.Context, id string) (branch.ForkGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.forkGroups[id]
	if !ok {
		return v, branch.ErrNotFound
	}
	return clone(v), nil
}

func (s *Store) UpdateHead(_ context.Context, branchID, expected, next string) (branch.Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.branches[branchID]
	if !ok {
		return value, branch.ErrNotFound
	}
	if value.HeadFrameID != expected {
		return value, branch.ErrCASConflict
	}
	candidate, ok := s.frames[next]
	if !ok {
		return value, frame.ErrFrameNotFound
	}
	if err := branch.ValidateFrameIsolation(value, candidate); err != nil {
		return value, err
	}
	value.HeadFrameID = next
	s.branches[branchID] = clone(value)
	group := s.forkGroups[value.ForkGroupID]
	for i := range group.Branches {
		if group.Branches[i].BranchID == branchID {
			group.Branches[i] = value
		}
	}
	s.forkGroups[group.ForkGroupID] = group
	return clone(value), nil
}

func (s *Store) PutComparison(_ context.Context, group branch.ForkGroup, left, right branch.Trajectory) (branch.ComparisonResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.forkGroups[group.ForkGroupID]
	if !ok {
		return branch.ComparisonResult{}, branch.ErrNotFound
	}
	result, err := branch.Compare(stored, left, right)
	if err != nil {
		return result, err
	}
	if old, ok := s.comparisons[result.ComparisonID]; ok {
		return clone(old), nil
	}
	e := branchEvent(branch.EventBranchCompared, stored, "", result, time.Now().UTC())
	s.putEvent(e)
	s.comparisons[result.ComparisonID] = clone(result)
	return clone(result), nil
}
func (s *Store) GetComparison(_ context.Context, id string) (branch.ComparisonResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.comparisons[id]
	if !ok {
		return v, branch.ErrNotFound
	}
	return clone(v), nil
}
