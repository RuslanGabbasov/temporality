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

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/render"
	"github.com/temporality-project/temporality/frp/replay"
	"github.com/temporality-project/temporality/frp/substrate"
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
	mux.HandleFunc("POST /v1/replay", s.replayEvents)
	mux.HandleFunc("POST /v1/render", s.renderFrame)
	mux.HandleFunc("POST /v1/objectives", s.createObjective)
	mux.HandleFunc("GET /v1/objectives/{id}", s.getObjective)
	mux.HandleFunc("POST /v1/claims", s.commitClaim)
	mux.HandleFunc("POST /v1/claims/{id}/transitions", s.transitionClaim)
	mux.HandleFunc("GET /v1/claims/{id}", s.getClaim)
	mux.HandleFunc("POST /v1/frames", s.createFrame)
	mux.HandleFunc("POST /v1/frames/{id}/transitions", s.transitionFrame)
	mux.HandleFunc("POST /v1/frames/{id}/emissions", s.reduceEmission)
	mux.HandleFunc("GET /v1/frames/{id}", s.getFrame)
	mux.HandleFunc("GET /metrics", s.metrics.handler)
	return s.metrics.count(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "protocol": protocol.Name, "version": protocol.Version})
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
	stores, ok := s.store.(render.Stores)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("render is not supported"))
		return
	}
	var request render.Request
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	packet, err := render.New(stores).Render(r.Context(), request)
	if errors.Is(err, frame.ErrFrameNotFound) || errors.Is(err, objective.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
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
	decision, err := cognition.ReduceEmission(current, emission)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if len(decision.Claims) > 0 || len(decision.Actions) > 0 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("claims and actions require atomic runtime orchestration not available in M2"))
		return
	}
	now := s.now().UTC()
	event := protocol.Event{EventID: newUUID(), ValidTime: now, Type: "frame.transitioned", EpisodeID: current.EpisodeID, BranchID: current.BranchID, Payload: map[string]any{"parent_frame_id": current.FrameID, "emission_id": emission.EmissionID}, Provenance: map[string]any{"source": "cognitive_emission", "emission_schema": emission.Schema}}
	event.ApplyDefaults(now)
	result, err := store.TransitionFrame(r.Context(), current.FrameID, decision.Transition, event)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
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
	result, err := s.replay.ReplayWithManifest(r.Context(), substrate.EventFilter{EpisodeID: request.EpisodeID, BranchID: request.BranchID, AsOf: request.AsOf}, request.Manifest)
	if err != nil {
		s.metrics.replayErrors.Add(1)
		s.log.Error("replay", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, result)
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
