package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/branch"
	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/projection"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/replay"
	runtimeStep "github.com/temporality-project/temporality/frp/runtime/step"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/timetravel"
	"github.com/temporality-project/temporality/frp/world"
)

type Server struct {
	store   substrate.EventStore
	replay  *replay.Service
	log     *slog.Logger
	now     func() time.Time
	metrics *metrics
}

func New(store substrate.EventStore, log *slog.Logger) http.Handler {
	s := &Server{store: store, replay: replay.New(store), log: log, now: time.Now, metrics: &metrics{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/events", s.appendEvent)
	mux.HandleFunc("GET /v1/events", s.listEvents)
	mux.HandleFunc("GET /v1/events/{id}", s.getEvent)
	mux.HandleFunc("POST /v1/observations/events", s.appendObservations)
	mux.HandleFunc("GET /v1/observations/events", s.listObservations)
	mux.HandleFunc("GET /v1/observations/knowledge", s.listObservationKnowledge)
	mux.HandleFunc("POST /v1/observations/knowledge/invalidate", s.invalidateObservationKnowledge)
	mux.HandleFunc("POST /v1/observations/hints", s.activateObservationHints)
	mux.HandleFunc("POST /v1/replay", s.replayEvents)
	mux.HandleFunc("POST /v1/snapshots", s.createSnapshot)
	mux.HandleFunc("GET /v1/snapshots/{id}", s.getSnapshot)
	mux.HandleFunc("POST /v1/blame", s.buildBlame)
	mux.HandleFunc("POST /v1/step", s.step)
	mux.HandleFunc("POST /v1/model-step", s.modelStep)
	mux.HandleFunc("GET /v1/model/config", s.modelConfig)
	mux.HandleFunc("POST /v1/render", s.renderFrame)
	mux.HandleFunc("POST /v1/objectives", s.createObjective)
	mux.HandleFunc("GET /v1/objectives/{id}", s.getObjective)
	mux.HandleFunc("POST /v1/projections/regions/rebuild", s.rebuildRegions)
	mux.HandleFunc("POST /v1/projections/procedures/rebuild", s.rebuildProcedures)
	mux.HandleFunc("GET /v1/regions", s.listRegions)
	mux.HandleFunc("GET /v1/procedures", s.listProcedures)
	mux.HandleFunc("GET /v1/procedures/{id}", s.getProcedure)
	mux.HandleFunc("POST /v1/procedures/match", s.matchProcedures)
	mux.HandleFunc("POST /v1/claims", s.commitClaim)
	mux.HandleFunc("POST /v1/claims/{id}/transitions", s.transitionClaim)
	mux.HandleFunc("GET /v1/claims/{id}", s.getClaim)
	mux.HandleFunc("GET /v1/claims/{id}/evidence", s.claimEvidence)
	mux.HandleFunc("POST /v1/executions", s.createExecution)
	mux.HandleFunc("GET /v1/executions/{id}", s.getExecution)
	mux.HandleFunc("POST /internal/v1/executions/{id}/transitions", s.transitionExecution)
	mux.HandleFunc("POST /v1/worlds", s.saveWorld)
	mux.HandleFunc("GET /v1/worlds", s.listWorlds)
	mux.HandleFunc("GET /v1/worlds/{id}", s.getWorld)
	mux.HandleFunc("GET /v1/affordances", s.listAffordances)
	mux.HandleFunc("POST /v1/projections/entities/rebuild", s.rebuildEntities)
	mux.HandleFunc("GET /v1/entities", s.listEntities)
	mux.HandleFunc("GET /v1/entities/{id}", s.getEntity)
	mux.HandleFunc("POST /v1/ingest", s.ingestSource)
	mux.HandleFunc("POST /v1/bootstrap", s.bootstrap)
	mux.HandleFunc("POST /v1/frames", s.createFrame)
	mux.HandleFunc("POST /v1/frames/{id}/transitions", s.transitionFrame)
	mux.HandleFunc("POST /v1/frames/{id}/emissions", s.reduceEmission)
	mux.HandleFunc("GET /v1/frames/{id}", s.getFrame)
	mux.HandleFunc("POST /v1/fork", s.fork)
	mux.HandleFunc("GET /v1/branches/{id}", s.getBranch)
	mux.HandleFunc("GET /v1/fork-groups/{id}", s.getForkGroup)
	mux.HandleFunc("POST /v1/branches/{id}/head", s.updateBranchHead)
	mux.HandleFunc("POST /v1/branch-comparisons", s.compareBranches)
	mux.HandleFunc("GET /v1/branch-comparisons/{id}", s.getBranchComparison)
	mux.HandleFunc("GET /metrics", s.metrics.handler)
	return s.metrics.count(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "protocol": protocol.Name, "version": protocol.Version})
}

type forkRequest struct {
	SourceFrameID string `json:"source_frame_id"`
	ForkGroupID   string `json:"fork_group_id"`
	Branches      []struct {
		BranchID    string         `json:"branch_id"`
		Label       string         `json:"label,omitempty"`
		ModelConfig map[string]any `json:"model_config"`
	} `json:"branches"`
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(branch.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("branches are not supported"))
		return
	}
	replayStore, ok := s.store.(timetravel.ReplayStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("frame replay is not supported"))
		return
	}
	var input forkRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.SourceFrameID == "" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("source_frame_id is required"))
		return
	}
	if _, err := timetravel.ReplayFrame(r.Context(), replayStore, input.SourceFrameID); errors.Is(err, frame.ErrFrameNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	} else if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if input.ForkGroupID == "" {
		input.ForkGroupID = newUUID()
	}
	request := branch.ForkRequest{ForkGroupID: input.ForkGroupID, Branches: make([]branch.BranchSpec, len(input.Branches))}
	for i, spec := range input.Branches {
		if spec.BranchID == "" {
			spec.BranchID = newUUID()
		}
		request.Branches[i] = branch.BranchSpec{BranchID: spec.BranchID, ModelConfig: spec.ModelConfig}
	}
	group, err := store.Fork(r.Context(), input.SourceFrameID, request)
	if errors.Is(err, frame.ErrFrameNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, group)
}

func (s *Server) getBranch(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(branch.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("branches are not supported"))
		return
	}
	value, err := store.GetBranch(r.Context(), r.PathValue("id"))
	if errors.Is(err, branch.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) getForkGroup(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(branch.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("branches are not supported"))
		return
	}
	value, err := store.GetForkGroup(r.Context(), r.PathValue("id"))
	if errors.Is(err, branch.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

type branchHeadRequest struct {
	ExpectedFrameID string `json:"expected_frame_id"`
	NextFrameID     string `json:"next_frame_id"`
}

func (s *Server) updateBranchHead(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(branch.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("branches are not supported"))
		return
	}
	var input branchHeadRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value, err := store.UpdateHead(r.Context(), r.PathValue("id"), input.ExpectedFrameID, input.NextFrameID)
	if errors.Is(err, branch.ErrCASConflict) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if errors.Is(err, branch.ErrNotFound) || errors.Is(err, frame.ErrFrameNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

type branchComparisonRequest struct {
	ForkGroupID string            `json:"fork_group_id"`
	ForkGroup   *branch.ForkGroup `json:"fork_group,omitempty"`
	Left        branch.Trajectory `json:"left"`
	Right       branch.Trajectory `json:"right"`
}

func (s *Server) compareBranches(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(branch.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("branches are not supported"))
		return
	}
	var input branchComparisonRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var group branch.ForkGroup
	var err error
	if input.ForkGroup != nil {
		group = *input.ForkGroup
	} else {
		groupID := input.ForkGroupID
		if groupID == "" && input.Left.BranchID != "" {
			var value branch.Branch
			value, err = store.GetBranch(r.Context(), input.Left.BranchID)
			groupID = value.ForkGroupID
		}
		if err == nil {
			group, err = store.GetForkGroup(r.Context(), groupID)
		}
	}
	if errors.Is(err, branch.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if _, err = branch.Compare(group, input.Left, input.Right); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	result, err := store.PutComparison(r.Context(), group, input.Left, input.Right)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) getBranchComparison(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(branch.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("branches are not supported"))
		return
	}
	value, err := store.GetComparison(r.Context(), r.PathValue("id"))
	if errors.Is(err, branch.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) appendEvent(w http.ResponseWriter, r *http.Request) {
	var event protocol.Event
	if err := decodeJSON(r, &event); err != nil {
		s.metrics.appendErrors.Add(1)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if event.EventID == "" {
		event.EventID = newUUID()
	}
	event.ApplyDefaults(s.now())
	if err := event.Validate(); err != nil {
		s.metrics.appendErrors.Add(1)
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := s.store.Append(r.Context(), event); err != nil {
		s.metrics.appendErrors.Add(1)
		writeError(w, http.StatusConflict, err)
		return
	}
	s.metrics.eventsAppended.Add(1)
	writeJSON(w, http.StatusCreated, event)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	pageStore, ok := s.store.(substrate.PageStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("event pagination is not supported"))
		return
	}
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "0"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("limit must be an integer"))
		return
	}
	var asOf *time.Time
	if value := r.URL.Query().Get("as_of"); value != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, value)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, errors.New("as_of must be RFC3339"))
			return
		}
		asOf = &parsed
	}
	page, err := pageStore.ListPage(r.Context(), substrate.PageRequest{Filter: substrate.EventFilter{EpisodeID: r.URL.Query().Get("episode_id"), BranchID: r.URL.Query().Get("branch_id"), AsOf: asOf}, Cursor: r.URL.Query().Get("cursor"), Limit: limit})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	event, err := s.store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, substrate.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.log.Error("get event", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, event)
}

type stepRequest struct {
	FrameID     string                      `json:"frame_id"`
	Emission    cognition.CognitiveEmission `json:"emission"`
	Definitions []affordance.Definition     `json:"definitions"`
	WorldID     string                      `json:"world_id"`
}

func (s *Server) step(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(runtimeStep.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("atomic step is not supported"))
		return
	}
	frames, ok := s.store.(frame.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("frames are not supported"))
		return
	}
	var request stepRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	current, err := frames.GetFrame(r.Context(), request.FrameID)
	if errors.Is(err, frame.ErrFrameNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	definitions := make(map[string]affordance.Definition, len(request.Definitions))
	for _, definition := range request.Definitions {
		definition.ApplyDefaults()
		if _, exists := definitions[definition.ID]; exists {
			writeError(w, http.StatusBadRequest, errors.New("duplicate affordance definition"))
			return
		}
		definitions[definition.ID] = definition
	}
	boundWorld, err := s.worldForExecution(r, request.WorldID, requestCapabilities(definitions))
	if errors.Is(err, world.ErrWorldNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	result, err := runtimeStep.Run(r.Context(), store, runtimeStep.Input{Current: current, Emission: request.Emission, Definitions: definitions, WorldID: boundWorld.WorldID, WorldVersion: boundWorld.StateVersion, NewID: newUUID, Now: func() time.Time { return s.now().UTC() }})
	if errors.Is(err, runtimeStep.ErrCurrentFrameChanged) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

type executionCreateRequest struct {
	Definition affordance.Definition `json:"definition"`
	EpisodeID  string                `json:"episode_id"`
	BranchID   string                `json:"branch_id"`
	WorldID    string                `json:"world_id"`
	Arguments  map[string]any        `json:"arguments"`
}

func (s *Server) createExecution(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(execution.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("executions are not supported"))
		return
	}
	var input executionCreateRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.Definition.ApplyDefaults()
	boundWorld, err := s.worldForExecution(r, input.WorldID, input.Definition.Capabilities)
	if errors.Is(err, world.ErrWorldNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	now := s.now().UTC()
	request := affordance.Request{RequestID: newUUID(), EpisodeID: input.EpisodeID, AffordanceID: input.Definition.ID, Arguments: input.Arguments}
	request.ApplyDefaults()
	requested := protocol.Event{EventID: newUUID(), ValidTime: now, EpisodeID: input.EpisodeID, BranchID: input.BranchID, Type: affordance.EventRequested, Payload: map[string]any{"request_id": request.RequestID, "affordance": request.AffordanceID}, Provenance: map[string]any{"source": "runtime"}}
	requested.ApplyDefaults(now)
	created := protocol.Event{EventID: newUUID(), ValidTime: now, EpisodeID: input.EpisodeID, BranchID: input.BranchID, Type: execution.EventExecutionCreated, Payload: map[string]any{}, Provenance: map[string]any{"source": "runtime"}}
	created.ApplyDefaults(now)
	value := execution.Execution{Protocol: protocol.Name, Version: protocol.Version, ExecutionID: newUUID(), RequestID: request.RequestID, EpisodeID: input.EpisodeID, BranchID: input.BranchID, AffordanceID: input.Definition.ID, WorldID: boundWorld.WorldID, WorldVersion: boundWorld.StateVersion, Status: execution.StatusCreated, CreatedEventID: created.EventID, IntentPersistedAt: created.TransactionTime}
	created.Payload["execution_id"] = value.ExecutionID
	if err := store.CreateExecution(r.Context(), input.Definition, request, value, requested, created); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"request": request, "execution": value, "events": []protocol.Event{requested, created}})
}

type executionTransitionRequest struct {
	Status execution.Status          `json:"status"`
	Error  *execution.ExecutionError `json:"error,omitempty"`
}

func (s *Server) transitionExecution(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(execution.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("executions are not supported"))
		return
	}
	var input executionTransitionRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	current, err := store.GetExecution(r.Context(), r.PathValue("id"))
	if errors.Is(err, execution.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	eventType, ok := execution.EventTypeForStatus(input.Status)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, errors.New("invalid execution status"))
		return
	}
	now := s.now().UTC()
	event := protocol.Event{EventID: newUUID(), ValidTime: now, EpisodeID: current.EpisodeID, Type: eventType, Payload: map[string]any{"execution_id": current.ExecutionID}, Provenance: map[string]any{"source": "executor"}}
	event.ApplyDefaults(now)
	updated, err := store.TransitionExecution(r.Context(), current.ExecutionID, input.Status, now, input.Error, event)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"execution": updated, "event": event})
}
func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(execution.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("executions are not supported"))
		return
	}
	value, err := store.GetExecution(r.Context(), r.PathValue("id"))
	if errors.Is(err, execution.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

type regionRebuildRequest struct {
	EpisodeID string     `json:"episode_id"`
	BranchID  string     `json:"branch_id"`
	AsOf      *time.Time `json:"as_of,omitempty"`
}

func (s *Server) rebuildRegions(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(projection.RegionStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("region projections are not supported"))
		return
	}
	var request regionRebuildRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.EpisodeID == "" || request.BranchID == "" {
		writeError(w, http.StatusBadRequest, errors.New("episode_id and branch_id are required"))
		return
	}
	events, err := s.store.List(r.Context(), substrate.EventFilter{EpisodeID: request.EpisodeID, BranchID: request.BranchID, AsOf: request.AsOf})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	regions := projection.BuildRegions(request.EpisodeID, request.BranchID, events)
	if err = store.ReplaceRegions(r.Context(), request.EpisodeID, request.BranchID, regions); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	edges := []projection.Edge{}
	if edgeStore, supported := s.store.(projection.EdgeStore); supported {
		edges = projection.BuildEdges(request.EpisodeID, request.BranchID, events, regions)
		if err = edgeStore.ReplaceEdges(r.Context(), request.EpisodeID, request.BranchID, edges); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projection_version": projection.RegionProjectorVersion, "edge_projection_version": projection.EdgeProjectorVersion, "regions": regions, "edges": edges})
}
func (s *Server) listRegions(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(projection.RegionStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("region projections are not supported"))
		return
	}
	var asOf *time.Time
	if value := r.URL.Query().Get("as_of"); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("as_of must be RFC3339"))
			return
		}
		asOf = &parsed
	}
	regions, err := store.ListRegions(r.Context(), projection.RegionFilter{EpisodeID: r.URL.Query().Get("episode_id"), BranchID: r.URL.Query().Get("branch_id"), AsOf: asOf})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"regions": regions})
}

type objectiveCreateRequest struct {
	Objective objective.Objective `json:"objective"`
	Event     protocol.Event      `json:"event"`
}

func (s *Server) createObjective(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(objective.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("objectives are not supported"))
		return
	}
	var request objectiveCreateRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	request.Objective.ApplyDefaults()
	if request.Objective.ObjectiveID == "" {
		request.Objective.ObjectiveID = newUUID()
	}
	now := s.now().UTC()
	if request.Event.EventID == "" {
		request.Event.EventID = newUUID()
	}
	request.Event.ApplyDefaults(now)
	request.Event.Type = "episode.started"
	request.Event.EpisodeID = request.Objective.EpisodeID
	request.Event.Payload["objective_id"] = request.Objective.ObjectiveID
	if err := store.CreateObjective(r.Context(), request.Objective, request.Event); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, request)
}
func (s *Server) getObjective(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(objective.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("objectives are not supported"))
		return
	}
	value, err := store.GetObjective(r.Context(), r.PathValue("id"))
	if errors.Is(err, objective.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func (s *Server) renderFrame(w http.ResponseWriter, r *http.Request) {
	s.metrics.renders.Add(1)
	stores, ok := s.store.(render.Stores)
	if !ok {
		s.metrics.renderErrors.Add(1)
		writeError(w, http.StatusNotImplemented, errors.New("render is not supported"))
		return
	}
	var request render.Request
	if err := decodeJSON(r, &request); err != nil {
		s.metrics.renderErrors.Add(1)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	packet, err := render.New(stores).Render(r.Context(), request)
	if errors.Is(err, frame.ErrFrameNotFound) || errors.Is(err, objective.ErrNotFound) {
		s.metrics.renderErrors.Add(1)
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.metrics.renderErrors.Add(1)
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	s.metrics.observeRender(packet)
	writeJSON(w, http.StatusOK, packet)
}

type frameCreateRequest struct {
	Frame frame.Frame    `json:"frame"`
	Event protocol.Event `json:"event"`
}

func (s *Server) createFrame(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(frame.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("frames are not supported"))
		return
	}
	var request frameCreateRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	now := s.now().UTC()
	request.Frame.ApplyDefaults()
	if request.Frame.FrameID == "" {
		request.Frame.FrameID = newUUID()
	}
	if request.Frame.AsOf.IsZero() {
		request.Frame.AsOf = now
	}
	if request.Event.EventID == "" {
		request.Event.EventID = newUUID()
	}
	request.Event.ApplyDefaults(now)
	request.Event.Type = "frame.created"
	request.Event.EpisodeID = request.Frame.EpisodeID
	request.Event.BranchID = request.Frame.BranchID
	request.Event.Payload["frame_id"] = request.Frame.FrameID
	if err := store.CreateFrame(r.Context(), request.Frame, request.Event); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, request)
}

type frameTransitionRequest struct {
	Transition frame.Transition `json:"transition"`
	Event      protocol.Event   `json:"event"`
}

func (s *Server) transitionFrame(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(frame.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("frames are not supported"))
		return
	}
	var request frameTransitionRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	now := s.now().UTC()
	if request.Event.EventID == "" {
		request.Event.EventID = newUUID()
	}
	request.Event.ApplyDefaults(now)
	request.Event.Type = "frame.transitioned"
	request.Event.Payload["parent_frame_id"] = r.PathValue("id")
	result, err := store.TransitionFrame(r.Context(), r.PathValue("id"), request.Transition, request.Event)
	if errors.Is(err, frame.ErrFrameNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) reduceEmission(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(frame.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("frames are not supported"))
		return
	}
	var emission cognition.CognitiveEmission
	if err := decodeJSON(r, &emission); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	emission.ApplyDefaults()
	current, err := store.GetFrame(r.Context(), r.PathValue("id"))
	if errors.Is(err, frame.ErrFrameNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	now := s.now().UTC()
	decision, err := cognition.ReduceEmission(current, emission, now)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if len(decision.Claims) > 0 || len(decision.Actions) > 0 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("claims and actions require atomic runtime orchestration not available in M2"))
		return
	}
	event := protocol.Event{EventID: newUUID(), ValidTime: now, Type: "frame.transitioned", EpisodeID: current.EpisodeID, BranchID: current.BranchID, Payload: map[string]any{"parent_frame_id": current.FrameID, "emission_id": emission.EmissionID}, Provenance: map[string]any{"source": "cognitive_emission", "emission_schema": emission.Schema}}
	event.ApplyDefaults(now)
	result, err := store.TransitionFrame(r.Context(), current.FrameID, decision.Transition, event)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if result.Frame.Focus != current.Focus {
		s.metrics.focusSwitches.Add(1)
	}
	decision.Frame = result.Frame
	writeJSON(w, http.StatusCreated, map[string]any{"decision": decision, "event": result.Event})
}

func (s *Server) getFrame(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(frame.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("frames are not supported"))
		return
	}
	value, err := store.GetFrame(r.Context(), r.PathValue("id"))
	if errors.Is(err, frame.ErrFrameNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.log.Error("get frame", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, value)
}

type claimRequest struct {
	Event     protocol.Event            `json:"event"`
	Claim     cognition.Claim           `json:"claim"`
	Relations []cognition.ClaimRelation `json:"relations,omitempty"`
}

func (s *Server) commitClaim(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(cognition.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("claims are not supported"))
		return
	}
	var request claimRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	now := s.now()
	if request.Event.EventID == "" {
		request.Event.EventID = newUUID()
	}
	if request.Claim.ClaimID == "" {
		request.Claim.ClaimID = newUUID()
	}
	request.Claim.ApplyDefaults(now)
	request.Event.ApplyDefaults(now)
	request.Event.Type = "claim." + string(request.Claim.Status)
	request.Claim.CreatedEvent = request.Event.EventID
	for i := range request.Relations {
		request.Relations[i].SourceClaim = request.Claim.ClaimID
		request.Relations[i].EvidenceEvent = request.Event.EventID
	}
	commit := cognition.Commit{Event: request.Event, Claim: request.Claim, Relations: request.Relations}
	if err := store.CommitClaim(r.Context(), commit); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, commit)
}

type transitionRequest struct {
	Event      protocol.Event        `json:"event"`
	ToStatus   cognition.ClaimStatus `json:"to_status"`
	Confidence *float32              `json:"confidence,omitempty"`
	ValidAt    time.Time             `json:"valid_at,omitempty"`
}

func (s *Server) transitionClaim(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(cognition.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("claims are not supported"))
		return
	}
	var request transitionRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	now := s.now().UTC()
	if request.ValidAt.IsZero() {
		request.ValidAt = now
	}
	if request.Event.EventID == "" {
		request.Event.EventID = newUUID()
	}
	request.Event.ApplyDefaults(now)
	request.Event.ValidTime = request.ValidAt
	request.Event.Type = "claim." + string(request.ToStatus)
	if request.Event.Payload == nil {
		request.Event.Payload = map[string]any{}
	}
	request.Event.Payload["claim_id"] = r.PathValue("id")
	request.Event.Payload["status"] = request.ToStatus
	transition := cognition.Transition{Event: request.Event, ClaimID: r.PathValue("id"), ToStatus: request.ToStatus, Confidence: request.Confidence, ValidAt: request.ValidAt}
	claim, err := store.TransitionClaim(r.Context(), transition)
	if errors.Is(err, cognition.ErrClaimNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": transition.Event, "claim": claim})
}

func (s *Server) getClaim(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(cognition.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("claims are not supported"))
		return
	}
	claim, err := store.GetClaim(r.Context(), r.PathValue("id"))
	if errors.Is(err, cognition.ErrClaimNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.log.Error("get claim", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	relations, err := store.ListRelations(r.Context(), claim.ClaimID)
	if err != nil {
		s.log.Error("list claim relations", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"claim": claim, "relations": relations})
}

type replayRequest struct {
	FrameID   string          `json:"frame_id,omitempty"`
	EpisodeID string          `json:"episode_id"`
	BranchID  string          `json:"branch_id"`
	AsOf      *time.Time      `json:"as_of,omitempty"`
	Manifest  replay.Manifest `json:"manifest,omitempty"`
}

func (s *Server) replayEvents(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	s.metrics.replays.Add(1)
	defer func() { s.metrics.replayDurationNanos.Add(uint64(time.Since(started))) }()
	var request replayRequest
	if err := decodeJSON(r, &request); err != nil {
		s.metrics.replayErrors.Add(1)
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.FrameID != "" {
		store, ok := s.store.(timetravel.ReplayStore)
		if !ok {
			s.metrics.replayErrors.Add(1)
			writeError(w, http.StatusUnprocessableEntity, errors.New("frame replay is not supported"))
			return
		}
		result, err := timetravel.ReplayFrame(r.Context(), store, request.FrameID)
		if errors.Is(err, frame.ErrFrameNotFound) {
			s.metrics.replayErrors.Add(1)
			writeError(w, http.StatusNotFound, err)
			return
		}
		if err != nil {
			s.metrics.replayErrors.Add(1)
			s.log.Error("replay frame", "error", err)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	result, err := s.replay.ReplayWithManifest(r.Context(), substrate.EventFilter{EpisodeID: request.EpisodeID, BranchID: request.BranchID, AsOf: request.AsOf}, request.Manifest)
	if err != nil {
		s.metrics.replayErrors.Add(1)
		s.log.Error("replay", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type snapshotRequest struct {
	FrameID string `json:"frame_id"`
}

func (s *Server) createSnapshot(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(timetravel.ReplayStore)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, errors.New("snapshots are not supported"))
		return
	}
	var request snapshotRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.FrameID == "" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("frame_id is required"))
		return
	}
	replayed, err := timetravel.ReplayFrame(r.Context(), store, request.FrameID)
	if errors.Is(err, frame.ErrFrameNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.log.Error("replay frame for snapshot", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	content, err := json.Marshal(replayed.Frame)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	snapshot, err := timetravel.NewSnapshot(timetravel.SnapshotMetadata{SnapshotID: newUUID(), EpisodeID: replayed.Frame.EpisodeID, CreatedAt: s.now().UTC(), Through: replayed.Through}, content)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := store.CreateSnapshot(r.Context(), snapshot); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, snapshot)
}

func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(timetravel.SnapshotStore)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, errors.New("snapshots are not supported"))
		return
	}
	snapshot, err := store.GetSnapshot(r.Context(), r.PathValue("id"))
	if errors.Is(err, substrate.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.log.Error("get snapshot", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

type blameRequest struct {
	RootID   string `json:"root_id"`
	MaxDepth int    `json:"max_depth"`
}

func (s *Server) buildBlame(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(timetravel.ProvenanceStore)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, errors.New("provenance is not supported"))
		return
	}
	var request blameRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.RootID == "" || request.MaxDepth < 0 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("root_id is required and max_depth cannot be negative"))
		return
	}
	graph, err := store.BuildBlame(r.Context(), request.RootID, request.MaxDepth)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return strings.Join([]string{h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]}, "-")
}
