package httpapi

import (
	"errors"
	"net/http"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/entity"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/ingest"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/projection"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/world"
)

// bootstrapRequest describes a first-contact bootstrap (M15): an agent with an
// empty memory arrives in a declared world and the runtime runs a bounded
// discovery cycle so the agent's first frame already sits on a map of that
// world, built only through the normal observation pipeline.
type bootstrapRequest struct {
	WorldID       string `json:"world_id"`
	ObjectiveText string `json:"objective_text"`
	ResourceID    string `json:"resource_id,omitempty"`
	EpisodeID     string `json:"episode_id,omitempty"`
	BranchID      string `json:"branch_id,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	BudgetTokens  int    `json:"budget_tokens,omitempty"`
	Depth         int    `json:"depth,omitempty"`
	MaxEvents     int    `json:"max_events,omitempty"`
}

type bootstrapIngestion struct {
	ResourceID string        `json:"resource_id"`
	Result     ingest.Result `json:"result"`
	Error      string        `json:"error,omitempty"`
}

type bootstrapResult struct {
	EpisodeID    string               `json:"episode_id"`
	BranchID     string               `json:"branch_id"`
	AgentID      string               `json:"agent_id"`
	ObjectiveID  string               `json:"objective_id"`
	FrameID      string               `json:"frame_id"`
	WorldID      string               `json:"world_id"`
	WorldVersion int                  `json:"world_version"`
	Ingestions   []bootstrapIngestion `json:"ingestions"`
	Regions      int                  `json:"regions"`
	Edges        int                  `json:"edges"`
	Entities     int                  `json:"entities"`
	Relations    int                  `json:"relations"`
	Render       *render.Packet       `json:"render,omitempty"`
	Summary      map[string]any       `json:"summary"`
}

// bootstrap runs the M15 first-contact flow:
//
//	objective -> bounded world discovery (ingestion) -> claims -> regions/
//	edges/entities -> first frame -> first render.
//
// Nothing enters memory outside the Event Log: the discovery reuses the M13
// ingestion pipeline, so observations, evidence-backed claims, and the entity
// graph are produced by the same machinery any later observation uses.
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	worldStore, ok := s.store.(world.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("worlds are not supported"))
		return
	}
	claimStore, ok := s.store.(cognition.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("claims are not supported"))
		return
	}
	objectiveStore, ok := s.store.(objective.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("objectives are not supported"))
		return
	}
	frameStore, ok := s.store.(frame.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("frames are not supported"))
		return
	}
	regionStore, ok := s.store.(projection.RegionStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("region projections are not supported"))
		return
	}
	entityStore, ok := s.store.(entity.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("entities are not supported"))
		return
	}
	var request bootstrapRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.WorldID == "" || request.ObjectiveText == "" {
		writeError(w, http.StatusBadRequest, errors.New("world_id and objective_text are required"))
		return
	}
	bound, err := worldStore.GetWorld(r.Context(), request.WorldID)
	if errors.Is(err, world.ErrWorldNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.log.Error("bootstrap load world", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	// Discovery targets: the requested resource, or every filesystem/git
	// resource the world declares. HTTP endpoints are skipped here; they can
	// be ingested explicitly once the agent knows which URLs matter.
	targets := make([]world.Resource, 0, len(bound.Resources))
	if request.ResourceID != "" {
		resource, found := bound.Resource(request.ResourceID)
		if !found {
			writeError(w, http.StatusNotFound, errors.New("resource not declared by world"))
			return
		}
		targets = append(targets, resource)
	} else {
		for _, resource := range bound.Roots() {
			targets = append(targets, resource)
		}
	}
	if len(targets) == 0 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("world declares no filesystem or git resources to discover"))
		return
	}

	// Bootstrap episode identity.
	episodeID := request.EpisodeID
	if episodeID == "" {
		episodeID = newUUID()
	}
	branchID := request.BranchID
	if branchID == "" {
		branchID = newUUID()
	}
	agentID := request.AgentID
	if agentID == "" {
		agentID = newUUID()
	}
	objectiveID := newUUID()
	frameID := newUUID()
	now := s.now().UTC()

	objectiveValue := objective.Objective{Protocol: protocol.Name, Version: protocol.Version, ObjectiveID: objectiveID, EpisodeID: episodeID, Text: request.ObjectiveText, SuccessConditions: []string{}, Constraints: objective.Constraints{}}
	objectiveValue.ApplyDefaults()
	objectiveEvent := protocol.Event{EventID: newUUID(), TransactionTime: now, ValidTime: now, EpisodeID: episodeID, Type: "episode.started", Payload: map[string]any{"objective_id": objectiveID, "world_id": bound.WorldID, "world_version": bound.StateVersion, "bootstrap": true}, Provenance: map[string]any{"source": "runtime", "flow": "bootstrap"}}
	objectiveEvent.ApplyDefaults(now)
	if err = objectiveStore.CreateObjective(r.Context(), objectiveValue, objectiveEvent); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	frameValue := frame.Frame{Protocol: protocol.Name, Version: protocol.Version, FrameID: frameID, AgentID: agentID, EpisodeID: episodeID, BranchID: branchID, ObjectiveID: objectiveID, AsOf: now, Focus: frame.Focus{Type: "query", Query: request.ObjectiveText}, WorkingSet: []frame.Ref{}, Mode: frame.ModeExplore, Attention: frame.Attention{Policy: "balanced", Deliberate: true, Ambient: true, MaxCandidates: 32}, Filters: frame.Filters{AgentIDs: []string{}, RegionKinds: []string{}}, Budget: frame.Budget{Tokens: 16000}}
	if request.BudgetTokens > 0 {
		frameValue.Budget.Tokens = request.BudgetTokens
	}
	frameValue.ApplyDefaults()
	frameEvent := protocol.Event{EventID: newUUID(), TransactionTime: now, ValidTime: now, EpisodeID: episodeID, BranchID: branchID, Type: "frame.created", Payload: map[string]any{"frame_id": frameID, "objective_id": objectiveID, "bootstrap": true}, Provenance: map[string]any{"source": "runtime", "flow": "bootstrap"}}
	frameEvent.ApplyDefaults(now)
	if err = frameStore.CreateFrame(r.Context(), frameValue, frameEvent); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}

	// Bounded discovery: one ingestion run per target resource. Failures are
	// recorded per resource; observations that did commit stay valid.
	result := bootstrapResult{EpisodeID: episodeID, BranchID: branchID, AgentID: agentID, ObjectiveID: objectiveID, FrameID: frameID, WorldID: bound.WorldID, WorldVersion: bound.StateVersion, Ingestions: []bootstrapIngestion{}}
	failed := 0
	for _, resource := range targets {
		runner := &ingest.Runner{World: bound, Events: s.store, Claims: claimStore, NewID: newUUID, Now: s.now}
		run, runErr := runner.Run(r.Context(), ingest.Request{WorldID: bound.WorldID, ResourceID: resource.ID, EpisodeID: episodeID, BranchID: branchID, Depth: request.Depth, MaxEvents: request.MaxEvents})
		entry := bootstrapIngestion{ResourceID: resource.ID, Result: run}
		if runErr != nil {
			entry.Error = runErr.Error()
			failed++
		}
		result.Ingestions = append(result.Ingestions, entry)
	}
	if failed == len(targets) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "discovery failed for every resource", "bootstrap": result})
		return
	}

	// Rebuild projections so attention and render see the fresh substrate.
	events, err := s.store.List(r.Context(), substrate.EventFilter{EpisodeID: episodeID, BranchID: branchID})
	if err != nil {
		s.log.Error("bootstrap list events", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	regions := projection.BuildRegions(episodeID, branchID, events)
	if err = regionStore.ReplaceRegions(r.Context(), episodeID, branchID, regions); err != nil {
		s.log.Error("bootstrap replace regions", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	result.Regions = len(regions)
	result.Edges = 0
	if edgeStore, supported := s.store.(projection.EdgeStore); supported {
		edges := projection.BuildEdges(episodeID, branchID, events, regions)
		if err = edgeStore.ReplaceEdges(r.Context(), episodeID, branchID, edges); err != nil {
			s.log.Error("bootstrap replace edges", "error", err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		result.Edges = len(edges)
	}
	claims, err := claimStore.ListClaims(r.Context())
	if err != nil {
		s.log.Error("bootstrap list claims", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	entities, relations := projection.BuildEntities(claims)
	if err = entityStore.ReplaceAllEntities(r.Context(), entities, relations); err != nil {
		s.log.Error("bootstrap replace entities", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	result.Entities = len(entities)
	result.Relations = len(relations)

	// First render: the agent opens its eyes already inside a map of the world.
	renderBudget := frameValue.Budget.Tokens
	renderer, supported := s.store.(render.Stores)
	if supported {
		packet, renderErr := render.New(renderer).Render(r.Context(), render.Request{FrameID: frameID, ObjectiveID: objectiveID, BudgetTokens: renderBudget})
		if renderErr != nil {
			s.log.Error("bootstrap render", "error", renderErr)
		} else {
			result.Render = &packet
		}
	}
	observations := 0
	claimsCount := 0
	for _, ingestion := range result.Ingestions {
		observations += len(ingestion.Result.Observations)
		claimsCount += len(ingestion.Result.Claims)
	}
	result.Summary = map[string]any{"observations": observations, "claims": claimsCount, "regions": result.Regions, "edges": result.Edges, "entities": result.Entities, "relations": result.Relations}
	writeJSON(w, http.StatusCreated, result)
}
