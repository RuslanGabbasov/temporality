package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/branch"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/entity"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/planner"
	"github.com/temporality-project/temporality/frp/procedure"
	"github.com/temporality-project/temporality/frp/projection"
	"github.com/temporality-project/temporality/frp/protocol"
	stepRuntime "github.com/temporality-project/temporality/frp/runtime/step"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/timetravel"
	"github.com/temporality-project/temporality/frp/world"
)

type Store struct {
	mu                 sync.RWMutex
	events             map[string]protocol.Event
	claims             map[string]cognition.Claim
	relations          []cognition.ClaimRelation
	claimEvidence      map[string][]string
	frames             map[string]frame.Frame
	objectives         map[string]objective.Objective
	regions            []projection.Region
	edges              []projection.Edge
	procedures         []procedure.Procedure
	definitions        map[string]affordance.Definition
	requests           map[string]affordance.Request
	executions         map[string]execution.Execution
	plannerRuns        map[string]planner.Run
	plannerSteps       map[string][]planner.DurableStep
	cognitiveSteps     map[string]stepRuntime.Prepared
	worlds             map[string]world.World
	entityRows         []entity.Entity
	entityRelationRows []entity.EntityRelation
	eventSeq           map[string]int64
	nextEventSeq       int64
	frameEvents        map[string]string
	snapshots          map[string]timetravel.Snapshot
	forkGroups         map[string]branch.ForkGroup
	branches           map[string]branch.Branch
	comparisons        map[string]branch.ComparisonResult
}

func New() *Store {
	return &Store{events: make(map[string]protocol.Event), claims: make(map[string]cognition.Claim), claimEvidence: make(map[string][]string), frames: make(map[string]frame.Frame), objectives: make(map[string]objective.Objective), definitions: make(map[string]affordance.Definition), requests: make(map[string]affordance.Request), executions: make(map[string]execution.Execution), plannerRuns: make(map[string]planner.Run), plannerSteps: make(map[string][]planner.DurableStep), cognitiveSteps: make(map[string]stepRuntime.Prepared), worlds: make(map[string]world.World), eventSeq: make(map[string]int64), frameEvents: make(map[string]string), snapshots: make(map[string]timetravel.Snapshot), forkGroups: make(map[string]branch.ForkGroup), branches: make(map[string]branch.Branch), comparisons: make(map[string]branch.ComparisonResult)}
}

func (s *Store) putEvent(event protocol.Event) {
	s.nextEventSeq++
	s.events[event.EventID] = event
	s.eventSeq[event.EventID] = s.nextEventSeq
}

func (s *Store) Append(_ context.Context, event protocol.Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[event.EventID]; exists {
		return errors.New("event already exists")
	}
	s.putEvent(event)
	return nil
}

func (s *Store) Get(_ context.Context, id string) (protocol.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	event, ok := s.events[id]
	if !ok {
		return protocol.Event{}, substrate.ErrNotFound
	}
	return event, nil
}

func (s *Store) List(_ context.Context, filter substrate.EventFilter) ([]protocol.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]protocol.Event, 0)
	for _, event := range s.events {
		if filter.EpisodeID != "" && event.EpisodeID != filter.EpisodeID {
			continue
		}
		if filter.BranchID != "" && event.BranchID != filter.BranchID {
			continue
		}
		if filter.AsOf != nil && event.ValidTime.After(*filter.AsOf) {
			continue
		}
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].ValidTime.Equal(result[j].ValidTime) {
			return result[i].ValidTime.Before(result[j].ValidTime)
		}
		if !result[i].TransactionTime.Equal(result[j].TransactionTime) {
			return result[i].TransactionTime.Before(result[j].TransactionTime)
		}
		return result[i].EventID < result[j].EventID
	})
	return result, nil
}

func (s *Store) CommitClaim(_ context.Context, commit cognition.Commit) error {
	if err := commit.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[commit.Event.EventID]; exists {
		return errors.New("event already exists")
	}
	if _, exists := s.claims[commit.Claim.ClaimID]; exists {
		return errors.New("claim already exists")
	}
	for _, relation := range commit.Relations {
		if _, exists := s.claims[relation.DestinationClaim]; !exists {
			return cognition.ErrClaimNotFound
		}
	}
	for _, id := range commit.Evidence {
		if _, exists := s.events[id]; !exists {
			return cognition.ErrEvidenceNotFound
		}
	}
	s.putEvent(commit.Event)
	s.claims[commit.Claim.ClaimID] = commit.Claim
	s.relations = append(s.relations, commit.Relations...)
	if len(commit.Evidence) > 0 {
		s.claimEvidence[commit.Claim.ClaimID] = append([]string(nil), commit.Evidence...)
	}
	return nil
}

func (s *Store) TransitionClaim(_ context.Context, transition cognition.Transition) (cognition.Claim, error) {
	if err := transition.Validate(); err != nil {
		return cognition.Claim{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	claim, exists := s.claims[transition.ClaimID]
	if !exists {
		return cognition.Claim{}, cognition.ErrClaimNotFound
	}
	if _, exists = s.events[transition.Event.EventID]; exists {
		return cognition.Claim{}, errors.New("event already exists")
	}
	if !cognition.CanTransition(claim.Status, transition.ToStatus) {
		return cognition.Claim{}, fmt.Errorf("invalid claim transition %s -> %s", claim.Status, transition.ToStatus)
	}
	if transition.ValidAt.Before(claim.ValidFrom) {
		return cognition.Claim{}, errors.New("transition valid_at cannot precede claim valid_from")
	}
	claim.Status = transition.ToStatus
	if transition.Confidence != nil {
		claim.Confidence = *transition.Confidence
	}
	if transition.ToStatus == cognition.ClaimRefuted || transition.ToStatus == cognition.ClaimSuperseded {
		validTo := transition.ValidAt
		claim.ValidTo = &validTo
	}
	s.putEvent(transition.Event)
	s.claims[claim.ClaimID] = claim
	return claim, nil
}

func (s *Store) GetClaim(_ context.Context, id string) (cognition.Claim, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	claim, exists := s.claims[id]
	if !exists {
		return cognition.Claim{}, cognition.ErrClaimNotFound
	}
	return claim, nil
}

func (s *Store) ListClaims(_ context.Context) ([]cognition.Claim, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]cognition.Claim, 0, len(s.claims))
	for _, claim := range s.claims {
		result = append(result, claim)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ClaimID < result[j].ClaimID })
	return result, nil
}

func (s *Store) ListRelations(_ context.Context, id string) ([]cognition.ClaimRelation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]cognition.ClaimRelation, 0)
	for _, relation := range s.relations {
		if relation.SourceClaim == id || relation.DestinationClaim == id {
			result = append(result, relation)
		}
	}
	return result, nil
}

func (s *Store) ListClaimEvidence(_ context.Context, id string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.claims[id]; !exists {
		return nil, cognition.ErrClaimNotFound
	}
	return append([]string(nil), s.claimEvidence[id]...), nil
}

func (s *Store) ReplaceAllEntities(_ context.Context, entities []entity.Entity, relations []entity.EntityRelation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entityRows = append([]entity.Entity(nil), entities...)
	s.entityRelationRows = append([]entity.EntityRelation(nil), relations...)
	return nil
}

func (s *Store) ListEntities(_ context.Context, filter entity.Filter) ([]entity.Entity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]entity.Entity, 0, len(s.entityRows))
	for _, e := range s.entityRows {
		if filter.Type != "" && e.Type != filter.Type {
			continue
		}
		if filter.Name != "" && e.Name != filter.Name {
			continue
		}
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type < result[j].Type
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (s *Store) GetEntity(_ context.Context, id string) (entity.Entity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entityRows {
		if e.EntityID == id {
			return e, nil
		}
	}
	return entity.Entity{}, entity.ErrEntityNotFound
}

func (s *Store) ListEntityRelations(_ context.Context, filter entity.RelationFilter) ([]entity.EntityRelation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]entity.EntityRelation, 0, len(s.entityRelationRows))
	for _, r := range s.entityRelationRows {
		if filter.EntityID != "" && r.SourceID != filter.EntityID && r.TargetID != filter.EntityID {
			continue
		}
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SourceID != result[j].SourceID {
			return result[i].SourceID < result[j].SourceID
		}
		if result[i].TargetID != result[j].TargetID {
			return result[i].TargetID < result[j].TargetID
		}
		if result[i].Predicate != result[j].Predicate {
			return result[i].Predicate < result[j].Predicate
		}
		return result[i].ClaimID < result[j].ClaimID
	})
	return result, nil
}

func (s *Store) ReplaceRegions(_ context.Context, episodeID, branchID string, regions []projection.Region) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]projection.Region, 0, len(s.regions)+len(regions))
	for _, region := range s.regions {
		if region.EpisodeID != episodeID || region.BranchID != branchID {
			kept = append(kept, region)
		}
	}
	for _, region := range regions {
		copy := region
		copy.MemberEventIDs = append([]string(nil), region.MemberEventIDs...)
		kept = append(kept, copy)
	}
	s.regions = kept
	return nil
}
func (s *Store) ListRegions(_ context.Context, filter projection.RegionFilter) ([]projection.Region, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]projection.Region, 0)
	for _, region := range s.regions {
		if filter.EpisodeID != "" && region.EpisodeID != filter.EpisodeID {
			continue
		}
		if filter.BranchID != "" && region.BranchID != filter.BranchID {
			continue
		}
		if filter.AsOf != nil && region.ValidFrom.After(*filter.AsOf) {
			continue
		}
		copy := region
		copy.MemberEventIDs = append([]string(nil), region.MemberEventIDs...)
		result = append(result, copy)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RegionID < result[j].RegionID })
	return result, nil
}

func (s *Store) ReplaceEdges(_ context.Context, episodeID, branchID string, edges []projection.Edge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]projection.Edge, 0, len(s.edges)+len(edges))
	for _, edge := range s.edges {
		if edge.EpisodeID != episodeID || edge.BranchID != branchID {
			kept = append(kept, edge)
		}
	}
	kept = append(kept, edges...)
	s.edges = kept
	return nil
}

func (s *Store) ListEdges(_ context.Context, filter projection.EdgeFilter) ([]projection.Edge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]projection.Edge, 0)
	for _, edge := range s.edges {
		if filter.EpisodeID != "" && edge.EpisodeID != filter.EpisodeID {
			continue
		}
		if filter.BranchID != "" && edge.BranchID != filter.BranchID {
			continue
		}
		result = append(result, edge)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SourceRegionID != result[j].SourceRegionID {
			return result[i].SourceRegionID < result[j].SourceRegionID
		}
		if result[i].TargetRegionID != result[j].TargetRegionID {
			return result[i].TargetRegionID < result[j].TargetRegionID
		}
		if result[i].Type != result[j].Type {
			return result[i].Type < result[j].Type
		}
		return result[i].EdgeID < result[j].EdgeID
	})
	return result, nil
}

func (s *Store) ReplaceProcedures(_ context.Context, episodeID string, values []procedure.Procedure) error {
	for _, value := range values {
		if err := value.Validate(); err != nil || value.EpisodeID != episodeID {
			if err != nil {
				return err
			}
			return errors.New("procedure episode does not match replacement scope")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]procedure.Procedure, 0, len(s.procedures)+len(values))
	for _, value := range s.procedures {
		if value.EpisodeID != episodeID {
			kept = append(kept, clone(value))
		}
	}
	for _, value := range values {
		kept = append(kept, clone(value))
	}
	s.procedures = kept
	return nil
}

func (s *Store) ListProcedures(_ context.Context, filter procedure.Filter) ([]procedure.Procedure, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]procedure.Procedure, 0)
	for _, value := range s.procedures {
		if filter.EpisodeID == "" || value.EpisodeID == filter.EpisodeID {
			result = append(result, clone(value))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ProcedureID < result[j].ProcedureID })
	return result, nil
}

func (s *Store) GetProcedure(_ context.Context, id string) (procedure.Procedure, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.procedures {
		if value.ProcedureID == id {
			return clone(value), nil
		}
	}
	return procedure.Procedure{}, procedure.ErrNotFound
}

func (s *Store) ListProcedureEvidence(_ context.Context, episodeID string) ([]procedure.Evidence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]procedure.Evidence, 0)
	for _, value := range s.executions {
		if value.EpisodeID != episodeID || !value.Status.Terminal() {
			continue
		}
		request, ok := s.requests[value.RequestID]
		if !ok {
			continue
		}
		result = append(result, procedure.Evidence{Execution: clone(value), Request: clone(request)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Execution.ExecutionID < result[j].Execution.ExecutionID })
	return result, nil
}

func (s *Store) CreateObjective(_ context.Context, value objective.Objective, event protocol.Event) error {
	if err := objective.ValidateCreate(value, event); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.objectives[value.ObjectiveID]; exists {
		return errors.New("objective already exists")
	}
	if _, exists := s.events[event.EventID]; exists {
		return errors.New("event already exists")
	}
	s.putEvent(event)
	s.objectives[value.ObjectiveID] = value
	return nil
}
func (s *Store) GetObjective(_ context.Context, id string) (objective.Objective, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.objectives[id]
	if !exists {
		return objective.Objective{}, objective.ErrNotFound
	}
	value.SuccessConditions = append([]string(nil), value.SuccessConditions...)
	return value, nil
}

func (s *Store) CreateFrame(_ context.Context, value frame.Frame, event protocol.Event) error {
	if err := frame.ValidateCreate(value, event); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.frames[value.FrameID]; exists {
		return frame.ErrFrameExists
	}
	if _, exists := s.events[event.EventID]; exists {
		return errors.New("event already exists")
	}
	s.putEvent(event)
	s.frameEvents[value.FrameID] = event.EventID
	s.frames[value.FrameID] = copyFrame(value)
	return nil
}

func (s *Store) TransitionFrame(_ context.Context, parentID string, transition frame.Transition, event protocol.Event) (frame.TransitionResult, error) {
	if err := frame.ValidateTransition(parentID, transition, event); err != nil {
		return frame.TransitionResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent, exists := s.frames[parentID]
	if !exists {
		return frame.TransitionResult{}, frame.ErrFrameNotFound
	}
	if _, exists = s.events[event.EventID]; exists {
		return frame.TransitionResult{}, errors.New("event already exists")
	}
	next, err := frame.Reduce(copyFrame(parent), transition)
	if err != nil {
		return frame.TransitionResult{}, err
	}
	event.Payload["frame_id"] = next.FrameID
	event.EpisodeID = next.EpisodeID
	event.BranchID = next.BranchID
	if err = frame.ValidateCreateEventForTransition(next, event); err != nil {
		return frame.TransitionResult{}, err
	}
	if _, exists = s.frames[next.FrameID]; exists {
		return frame.TransitionResult{}, frame.ErrFrameExists
	}
	s.putEvent(event)
	s.frameEvents[next.FrameID] = event.EventID
	s.frames[next.FrameID] = copyFrame(next)
	return frame.TransitionResult{Frame: copyFrame(next), Event: event}, nil
}

func (s *Store) GetFrame(_ context.Context, id string) (frame.Frame, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.frames[id]
	if !exists {
		return frame.Frame{}, frame.ErrFrameNotFound
	}
	return copyFrame(value), nil
}
func copyFrame(value frame.Frame) frame.Frame {
	result := value
	result.WorkingSet = append(make([]frame.Ref, 0, len(value.WorkingSet)), value.WorkingSet...)
	result.Filters.AgentIDs = append(make([]string, 0, len(value.Filters.AgentIDs)), value.Filters.AgentIDs...)
	result.Filters.RegionKinds = append(make([]string, 0, len(value.Filters.RegionKinds)), value.Filters.RegionKinds...)
	return result
}

func (s *Store) CreateExecution(_ context.Context, def affordance.Definition, request affordance.Request, value execution.Execution, requested, created protocol.Event) error {
	if err := execution.ValidateCreate(def, request, value, requested, created); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.definitions[def.ID]; ok && !existing.MatchesRegistry(def) {
		return affordance.ErrDefinitionFrozen
	}
	if _, ok := s.requests[request.RequestID]; ok {
		return errors.New("affordance request already exists")
	}
	if _, ok := s.executions[value.ExecutionID]; ok {
		return errors.New("execution already exists")
	}
	if _, ok := s.events[requested.EventID]; ok {
		return errors.New("event already exists")
	}
	if _, ok := s.events[created.EventID]; ok {
		return errors.New("event already exists")
	}
	s.definitions[def.ID] = clone(def)
	s.requests[request.RequestID] = clone(request)
	s.executions[value.ExecutionID] = clone(value)
	s.putEvent(clone(requested))
	s.putEvent(clone(created))
	return nil
}

func (s *Store) TransitionExecution(_ context.Context, id string, next execution.Status, at time.Time, executionError *execution.ExecutionError, event protocol.Event) (execution.Execution, error) {
	return s.transitionExecution(id, "", next, at, executionError, event, nil)
}

// ClaimExecution mirrors the postgres lease claim under the store mutex.
func (s *Store) ClaimExecution(_ context.Context, id, executorID string, at time.Time, lease time.Duration, event protocol.Event) (execution.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.executions[id]
	if !ok {
		return execution.Execution{}, execution.ErrNotFound
	}
	claimed, err := current.Claim(executorID, at, lease)
	if err != nil {
		return execution.Execution{}, err
	}
	if err = execution.ValidateClaimEvent(current, at, event); err != nil {
		return execution.Execution{}, err
	}
	if _, exists := s.events[event.EventID]; exists {
		return execution.Execution{}, errors.New("event already exists")
	}
	s.putEvent(clone(event))
	s.executions[id] = clone(claimed)
	return clone(claimed), nil
}

// TransitionExecutionOwned fences terminal transitions by lease ownership.
func (s *Store) TransitionExecutionOwned(_ context.Context, id, executorID string, next execution.Status, at time.Time, executionError *execution.ExecutionError, event protocol.Event) (execution.Execution, error) {
	return s.transitionExecution(id, executorID, next, at, executionError, event, nil)
}

// TransitionExecutionWithObservations commits a terminal transition together
// with the world.observation events produced by its steps in one atomic write.
func (s *Store) TransitionExecutionWithObservations(_ context.Context, id string, next execution.Status, at time.Time, executionError *execution.ExecutionError, event protocol.Event, observations []protocol.Event) (execution.Execution, error) {
	return s.transitionExecution(id, "", next, at, executionError, event, observations)
}

// TransitionExecutionOwnedWithObservations is the fenced flavor of the atomic
// terminal-transition-plus-observations commit: stale executors can neither
// overwrite the outcome nor leak duplicate observations.
func (s *Store) TransitionExecutionOwnedWithObservations(_ context.Context, id, executorID string, next execution.Status, at time.Time, executionError *execution.ExecutionError, event protocol.Event, observations []protocol.Event) (execution.Execution, error) {
	return s.transitionExecution(id, executorID, next, at, executionError, event, observations)
}

func (s *Store) transitionExecution(id, owner string, next execution.Status, at time.Time, executionError *execution.ExecutionError, event protocol.Event, observations []protocol.Event) (execution.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.executions[id]
	if !ok {
		return execution.Execution{}, execution.ErrNotFound
	}
	if owner != "" {
		if err := current.ValidateOwnedBy(owner); err != nil {
			return execution.Execution{}, err
		}
	}
	updated, err := current.Transition(next, at, executionError)
	if err != nil {
		return execution.Execution{}, err
	}
	if err = execution.ValidateTransitionEvent(current, next, at, event); err != nil {
		return execution.Execution{}, err
	}
	if _, ok = s.events[event.EventID]; ok {
		return execution.Execution{}, errors.New("event already exists")
	}
	for _, observation := range observations {
		if _, exists := s.events[observation.EventID]; exists {
			return execution.Execution{}, errors.New("event already exists")
		}
	}
	for _, observation := range observations {
		s.putEvent(clone(observation))
	}
	s.putEvent(clone(event))
	s.executions[id] = clone(updated)
	return clone(updated), nil
}

func (s *Store) GetDefinition(_ context.Context, id string) (affordance.Definition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.definitions[id]
	if !ok {
		return affordance.Definition{}, affordance.ErrDefinitionNotFound
	}
	return clone(value), nil
}

func (s *Store) GetRequest(_ context.Context, id string) (affordance.Request, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.requests[id]
	if !ok {
		return affordance.Request{}, affordance.ErrRequestNotFound
	}
	return clone(value), nil
}

func (s *Store) GetExecution(_ context.Context, id string) (execution.Execution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.executions[id]
	if !ok {
		return execution.Execution{}, execution.ErrNotFound
	}
	return clone(value), nil
}

func (s *Store) ListActiveExecutions(_ context.Context, episodeID string) ([]execution.Execution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]execution.Execution, 0)
	for _, value := range s.executions {
		if (episodeID == "" || value.EpisodeID == episodeID) && !value.Status.Terminal() {
			result = append(result, clone(value))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ExecutionID < result[j].ExecutionID })
	return result, nil
}

func (s *Store) EnsurePlannerRun(_ context.Context, value planner.Run) (planner.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.plannerRuns[value.ExecutionID]; ok {
		return clone(existing), nil
	}
	if _, ok := s.executions[value.ExecutionID]; !ok {
		return planner.Run{}, execution.ErrNotFound
	}
	s.plannerRuns[value.ExecutionID] = clone(value)
	return clone(value), nil
}

func (s *Store) GetPlannerRun(_ context.Context, executionID string) (planner.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.plannerRuns[executionID]
	if !ok {
		return planner.Run{}, planner.ErrNotFound
	}
	return clone(value), nil
}

func (s *Store) ListPlannerSteps(_ context.Context, executionID string) ([]planner.DurableStep, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.plannerRuns[executionID]; !ok {
		return nil, planner.ErrNotFound
	}
	return clone(s.plannerSteps[executionID]), nil
}

func (s *Store) RecordPlannerProposal(_ context.Context, executionID string, proposal planner.Proposal, state planner.State, at time.Time) (planner.DurableStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.plannerRuns[executionID]
	if !ok {
		return planner.DurableStep{}, planner.ErrNotFound
	}
	if run.Status != planner.RunRunning {
		return planner.DurableStep{}, errors.New("planner run is terminal")
	}
	steps := s.plannerSteps[executionID]
	status := planner.StepProposed
	if proposal.Complete {
		status = planner.StepCompleted
	}
	value := planner.DurableStep{ExecutionID: executionID, Ordinal: len(steps) + 1, Proposal: clone(proposal), Status: status, CreatedAt: at, UpdatedAt: at}
	s.plannerSteps[executionID] = append(steps, value)
	run.State, run.UpdatedAt = clone(state), at
	s.plannerRuns[executionID] = run
	return clone(value), nil
}

func (s *Store) RecordPlannerResult(_ context.Context, executionID string, ordinal int, result planner.StepResult, state planner.State, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	steps := s.plannerSteps[executionID]
	if ordinal <= 0 || ordinal > len(steps) {
		return errors.New("planner step not found")
	}
	step := steps[ordinal-1]
	if step.Status != planner.StepProposed {
		return errors.New("planner step already has result")
	}
	step.Result, step.UpdatedAt = &result, at
	if result.Success {
		step.Status = planner.StepCompleted
	} else {
		step.Status = planner.StepFailed
	}
	steps[ordinal-1] = step
	s.plannerSteps[executionID] = steps
	run := s.plannerRuns[executionID]
	run.State, run.UpdatedAt = clone(state), at
	s.plannerRuns[executionID] = run
	return nil
}

func (s *Store) FinishPlannerRun(_ context.Context, executionID string, status planner.RunStatus, state planner.State, message string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.plannerRuns[executionID]
	if !ok {
		return planner.ErrNotFound
	}
	if status != planner.RunCompleted && status != planner.RunFailed {
		return errors.New("invalid terminal planner status")
	}
	run.Status, run.State, run.Error, run.UpdatedAt = status, clone(state), message, at
	s.plannerRuns[executionID] = run
	return nil
}

func (s *Store) CommitStep(_ context.Context, prepared stepRuntime.Prepared) (stepRuntime.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	persisted, ok := s.frames[prepared.Current.FrameID]
	if !ok {
		return stepRuntime.Result{}, frame.ErrFrameNotFound
	}
	if !reflect.DeepEqual(persisted, prepared.Current) {
		return stepRuntime.Result{}, stepRuntime.ErrCurrentFrameChanged
	}
	for _, candidate := range s.frames {
		if candidate.ParentFrameID == prepared.Current.FrameID {
			return stepRuntime.Result{}, stepRuntime.ErrCurrentFrameChanged
		}
	}
	if _, ok = s.cognitiveSteps[prepared.Emission.EmissionID]; ok {
		return stepRuntime.Result{}, errors.New("cognitive step already exists")
	}
	if _, ok = s.frames[prepared.Decision.Frame.FrameID]; ok {
		return stepRuntime.Result{}, frame.ErrFrameExists
	}
	if err := s.validateStepRefsLocked(prepared); err != nil {
		return stepRuntime.Result{}, err
	}
	for _, event := range prepared.Events {
		if _, exists := s.events[event.EventID]; exists {
			return stepRuntime.Result{}, errors.New("event already exists")
		}
	}
	for _, candidate := range prepared.Claims {
		if _, exists := s.claims[candidate.Value.ClaimID]; exists {
			return stepRuntime.Result{}, errors.New("claim already exists")
		}
	}
	// Claim reconciliation (M16): refute/supersede transitions ride the same
	// atomic commit. Their events were already appended by the attention-range
	// loop above; this block only retires the claims themselves. A guard miss
	// here (claim missing, wrong state) fails the whole step — the runtime
	// resolved the ops against this very state a moment before the commit.
	for _, transition := range prepared.ClaimTransitions {
		claim, exists := s.claims[transition.ClaimID]
		if !exists {
			return stepRuntime.Result{}, cognition.ErrClaimNotFound
		}
		if !cognition.CanTransition(claim.Status, transition.ToStatus) {
			return stepRuntime.Result{}, fmt.Errorf("invalid claim transition %s -> %s", claim.Status, transition.ToStatus)
		}
		if transition.ValidAt.Before(claim.ValidFrom) {
			return stepRuntime.Result{}, errors.New("transition valid_at cannot precede claim valid_from")
		}
		claim.Status = transition.ToStatus
		if transition.Confidence != nil {
			claim.Confidence = *transition.Confidence
		}
		if transition.ToStatus == cognition.ClaimRefuted || transition.ToStatus == cognition.ClaimSuperseded {
			validTo := transition.ValidAt
			claim.ValidTo = &validTo
		}
		s.claims[claim.ClaimID] = claim
	}
	for _, action := range prepared.Actions {
		if existing, exists := s.definitions[action.Definition.ID]; exists && !existing.MatchesRegistry(action.Definition) {
			return stepRuntime.Result{}, affordance.ErrDefinitionFrozen
		}
		if _, exists := s.requests[action.Request.RequestID]; exists {
			return stepRuntime.Result{}, errors.New("affordance request already exists")
		}
		if _, exists := s.executions[action.Execution.ExecutionID]; exists {
			return stepRuntime.Result{}, errors.New("execution already exists")
		}
	}

	s.cognitiveSteps[prepared.Emission.EmissionID] = clone(prepared)
	claims := make([]cognition.Claim, 0, len(prepared.Claims))
	for _, candidate := range prepared.Claims {
		s.putEvent(clone(candidate.Event))
		s.claims[candidate.Value.ClaimID] = clone(candidate.Value)
		claims = append(claims, clone(candidate.Value))
	}
	claimEvents := len(prepared.Claims)
	actionEvents := len(prepared.Actions) * 2
	attentionEnd := len(prepared.Events) - actionEvents - 1
	for i := claimEvents; i < attentionEnd; i++ {
		s.putEvent(clone(prepared.Events[i]))
	}
	executions := make([]execution.Execution, 0, len(prepared.Actions))
	for _, action := range prepared.Actions {
		s.definitions[action.Definition.ID] = clone(action.Definition)
		s.requests[action.Request.RequestID] = clone(action.Request)
		s.executions[action.Execution.ExecutionID] = clone(action.Execution)
		s.putEvent(clone(action.Requested))
		s.putEvent(clone(action.Created))
		executions = append(executions, clone(action.Execution))
	}
	s.putEvent(clone(prepared.Transition))
	s.frameEvents[prepared.Decision.Frame.FrameID] = prepared.Transition.EventID
	s.frames[prepared.Decision.Frame.FrameID] = copyFrame(prepared.Decision.Frame)
	return stepRuntime.Result{Decision: clone(prepared.Decision), Frame: copyFrame(prepared.Decision.Frame), Claims: claims, Executions: executions, Events: clone(prepared.Events)}, nil
}

func (s *Store) GetCognitiveStep(_ context.Context, emissionID string) (stepRuntime.Prepared, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.cognitiveSteps[emissionID]
	if !ok {
		return stepRuntime.Prepared{}, substrate.ErrNotFound
	}
	return clone(value), nil
}

func (s *Store) validateStepRefsLocked(prepared stepRuntime.Prepared) error {
	check := func(ref frame.Ref) error {
		var exists bool
		switch ref.Type {
		case frame.RefEvent:
			_, exists = s.events[ref.ID]
		case frame.RefClaim:
			_, exists = s.claims[ref.ID]
		case frame.RefExecution:
			_, exists = s.executions[ref.ID]
		case frame.RefQuery:
			exists = prepared.Current.Focus.Type == frame.RefQuery && strings.EqualFold(strings.TrimSpace(prepared.Current.Focus.Query), strings.TrimSpace(ref.ID))
		case frame.RefRegion:
			for _, region := range s.regions {
				if region.RegionID == ref.ID {
					exists = true
					break
				}
			}
		default:
			return fmt.Errorf("unsupported ref type %q", ref.Type)
		}
		if !exists {
			return errors.New("reference not found")
		}
		return nil
	}
	for _, observation := range prepared.Emission.Observation {
		ref, _ := cognition.ParseRef(observation.Ref, false)
		if err := check(ref); err != nil {
			return fmt.Errorf("observation ref %s: %w", observation.Ref, err)
		}
	}
	for _, operation := range prepared.Decision.Transition.Operations {
		if operation.Ref != nil {
			if err := check(*operation.Ref); err != nil {
				return fmt.Errorf("frame ref %s:%s: %w", operation.Ref.Type, operation.Ref.ID, err)
			}
		}
		if operation.Focus != nil && operation.Focus.Type != frame.RefQuery {
			if err := check(frame.Ref{Type: operation.Focus.Type, ID: operation.Focus.ID}); err != nil {
				return fmt.Errorf("attention ref %s:%s: %w", operation.Focus.Type, operation.Focus.ID, err)
			}
		}
	}
	return nil
}

func clone[T any](value T) T {
	data, _ := json.Marshal(value)
	var result T
	_ = json.Unmarshal(data, &result)
	return result
}

func (s *Store) ListPage(ctx context.Context, request substrate.PageRequest) (substrate.EventPage, error) {
	limit, err := substrate.NormalizePageSize(request.Limit)
	if err != nil {
		return substrate.EventPage{}, err
	}
	cursor, err := substrate.DecodeCursor(request.Cursor)
	if err != nil {
		return substrate.EventPage{}, err
	}
	events, err := s.List(ctx, request.Filter)
	if err != nil {
		return substrate.EventPage{}, err
	}
	if cursor != nil {
		start := sort.Search(len(events), func(i int) bool {
			e := events[i]
			return e.ValidTime.After(cursor.ValidTime) || (e.ValidTime.Equal(cursor.ValidTime) && (e.TransactionTime.After(cursor.TransactionTime) || (e.TransactionTime.Equal(cursor.TransactionTime) && e.EventID > cursor.EventID)))
		})
		events = events[start:]
	}
	if len(events) > limit+1 {
		events = events[:limit+1]
	}
	return substrate.BuildPage(events, limit), nil
}
