package planner

import (
	"encoding/json"
	"errors"
	"sync"
)

// ArgumentAdapter replays a bounded planner trace supplied in the execution
// environment. It is intended for deterministic PoC and replay, not inference.
type ArgumentAdapter struct {
	mu      sync.Mutex
	indexes map[string]int
}

func (a *ArgumentAdapter) Next(ctx Context) (Proposal, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	raw, ok := ctx.Environment["planner_proposals"]
	if !ok {
		return Proposal{}, errors.New("planner_proposals trace is required")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return Proposal{}, err
	}
	var proposals []Proposal
	if err = json.Unmarshal(encoded, &proposals); err != nil {
		return Proposal{}, err
	}
	if a.indexes == nil {
		a.indexes = map[string]int{}
	}
	index := a.indexes[ctx.ExecutionID]
	if index >= len(proposals) {
		return Proposal{}, errors.New("planner trace exhausted")
	}
	a.indexes[ctx.ExecutionID] = index + 1
	return proposals[index], nil
}
