// Package httpapi serves the Temporality observation journal API: the
// universal event ingestion endpoint, knowledge projection and hint
// retrieval. Handlers carry the exact semantics they had inside the FRP
// runtime server (era 1) at the moment of extraction.
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

	"github.com/temporality-project/temporality/observation"
)

type Server struct {
	store observation.Store
	log   *slog.Logger
	now   func() time.Time
}

func New(store observation.Store, log *slog.Logger) http.Handler {
	s := &Server{store: store, log: log, now: time.Now}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/observations/events", s.appendObservations)
	mux.HandleFunc("GET /v1/observations/events", s.listObservations)
	mux.HandleFunc("GET /v1/observations/knowledge", s.listObservationKnowledge)
	mux.HandleFunc("POST /v1/observations/knowledge/invalidate", s.invalidateObservationKnowledge)
	mux.HandleFunc("POST /v1/observations/hints", s.activateObservationHints)
	return logRequests(log, mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "temporality-journal"})
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		log.Info("request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
	})
}

type observationBatch struct {
	Events []observation.Event `json:"events"`
}

type observationIngestResult struct {
	Accepted int                      `json:"accepted"`
	Repeated int                      `json:"repeated"`
	Results  []observationEventResult `json:"results"`
}

type observationEventResult struct {
	SourceID string `json:"source_id"`
	EventID  string `json:"event_id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

func (s *Server) appendObservations(w http.ResponseWriter, r *http.Request) {
	var input observationBatch
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(input.Events) == 0 || len(input.Events) > 100 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("events must contain between 1 and 100 items"))
		return
	}
	for i := range input.Events {
		if err := input.Events[i].Validate(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, errors.New("events["+strconv.Itoa(i)+"]: "+err.Error()))
			return
		}
	}
	result := observationIngestResult{Results: make([]observationEventResult, 0, len(input.Events))}
	partialFailure := false
	for _, event := range input.Events {
		event.ReceivedAt = s.now().UTC()
		inserted, err := s.store.AppendObservation(r.Context(), event)
		entry := observationEventResult{SourceID: event.Source.ID, EventID: event.EventID}
		switch {
		case err != nil:
			entry.Status = "rejected"
			if errors.Is(err, observation.ErrConflict) {
				entry.Error = observation.ErrConflict.Error()
			} else {
				entry.Error = "storage error"
			}
			partialFailure = true
		case inserted:
			entry.Status = "accepted"
			result.Accepted++
		default:
			entry.Status = "repeated"
			result.Repeated++
		}
		result.Results = append(result.Results, entry)
	}
	status := http.StatusOK
	if partialFailure {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, result)
}

func (s *Server) listObservations(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be between 1 and 500"))
			return
		}
		limit = parsed
	}
	cursor := r.URL.Query().Get("cursor")
	if _, err := observation.DecodeCursor(cursor); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var since, until, knownAt *time.Time
	for name, target := range map[string]**time.Time{"since": &since, "until": &until, "known_at": &knownAt} {
		if value := r.URL.Query().Get(name); value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New(name+" must be RFC3339"))
				return
			}
			*target = &parsed
		}
	}
	page, err := s.store.ListObservationPage(r.Context(), observation.Filter{
		Project: r.URL.Query().Get("project"), SourceID: r.URL.Query().Get("source_id"), EventID: r.URL.Query().Get("event_id"), Run: r.URL.Query().Get("run"),
		Task: r.URL.Query().Get("task"), Actor: r.URL.Query().Get("actor"),
		Type: r.URL.Query().Get("type"), Since: since, Until: until, KnownAt: knownAt, Limit: limit,
	}, cursor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not list observation events"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": page.Events, "count": len(page.Events), "next_cursor": page.NextCursor})
}

type invalidateObservationRequest struct {
	KnowledgeID string                 `json:"knowledge_id"`
	Project     string                 `json:"project"`
	Run         string                 `json:"run,omitempty"`
	Actor       observation.Actor      `json:"actor"`
	Reason      string                 `json:"reason"`
	Evidence    []observation.Evidence `json:"evidence,omitempty"`
}

func (s *Server) projectKnowledge(r *http.Request, project string, asOf, knownAt *time.Time) ([]observation.Event, error) {
	filter := observation.Filter{Project: project, Until: asOf, KnownAt: knownAt, Limit: 500}
	var result []observation.Event
	cursor := ""
	for {
		page, err := s.store.ListObservationPage(r.Context(), filter, cursor)
		if err != nil {
			return nil, err
		}
		result = append(result, page.Events...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return result, nil
}

func (s *Server) listObservationKnowledge(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		writeError(w, http.StatusBadRequest, errors.New("project is required"))
		return
	}
	var asOf, knownAt *time.Time
	for name, target := range map[string]**time.Time{"as_of": &asOf, "known_at": &knownAt} {
		if value := r.URL.Query().Get(name); value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New(name+" must be RFC3339"))
				return
			}
			*target = &parsed
		}
	}
	events, err := s.projectKnowledge(r, project, asOf, knownAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load project knowledge history"))
		return
	}
	knowledge, err := observation.ProjectKnowledge(events)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if knowledgeID := r.URL.Query().Get("knowledge_id"); knowledgeID != "" {
		filtered := knowledge[:0]
		for _, item := range knowledge {
			if item.ID == knowledgeID {
				filtered = append(filtered, item)
			}
		}
		if len(filtered) == 0 {
			writeError(w, http.StatusNotFound, observation.ErrKnowledgeNotFound)
			return
		}
		knowledge = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge": knowledge, "count": len(knowledge)})
}

func (s *Server) invalidateObservationKnowledge(w http.ResponseWriter, r *http.Request) {
	var input invalidateObservationRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.KnowledgeID = strings.TrimSpace(input.KnowledgeID)
	input.Project = strings.TrimSpace(input.Project)
	input.Actor.ID = strings.TrimSpace(input.Actor.ID)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.KnowledgeID == "" || input.Project == "" || input.Actor.ID == "" || input.Reason == "" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("knowledge_id, project, actor.id, and reason are required"))
		return
	}
	projectEvents, err := s.projectKnowledge(r, input.Project, nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load project knowledge history"))
		return
	}
	knowledge, err := observation.ProjectKnowledge(projectEvents)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	var target *observation.Knowledge
	for i := range knowledge {
		if knowledge[i].ID == input.KnowledgeID {
			target = &knowledge[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, observation.ErrKnowledgeNotFound)
		return
	}
	if target.State == "corrected" || target.State == "superseded" || target.State == "invalidated" {
		writeError(w, http.StatusConflict, errors.New("knowledge is already retired"))
		return
	}
	event := observation.Event{
		Schema: observation.Schema, EventID: newUUID(), OccurredAt: s.now().UTC(),
		Source:   observation.Source{ID: "temporality-manual", Integration: "temporality", Version: "1"},
		Context:  observation.Context{Project: input.Project, Run: input.Run, Actor: input.Actor},
		Type:     "knowledge.invalidated",
		Data:     map[string]any{"knowledge_id": input.KnowledgeID, "reason": input.Reason, "method": "manual"},
		Evidence: input.Evidence,
	}
	if err = event.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	event.ReceivedAt = s.now().UTC()
	if _, err = s.store.AppendObservation(r.Context(), event); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not record invalidation"))
		return
	}
	writeJSON(w, http.StatusCreated, event)
}

type observationHintRequest struct {
	Project    string            `json:"project"`
	Run        string            `json:"run,omitempty"`
	Task       string            `json:"task,omitempty"`
	Actor      observation.Actor `json:"actor,omitempty"`
	Query      string            `json:"query,omitempty"`
	Tool       string            `json:"tool,omitempty"`
	ToolResult string            `json:"tool_result,omitempty"`
	Entities   []string          `json:"entities,omitempty"`
	Topics     []string          `json:"topics,omitempty"`
	Limit      int               `json:"limit,omitempty"`
}

func (s *Server) activateObservationHints(w http.ResponseWriter, r *http.Request) {
	var input observationHintRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	input.Project = strings.TrimSpace(input.Project)
	input.Query = strings.TrimSpace(input.Query)
	input.Tool = strings.TrimSpace(input.Tool)
	if len([]rune(input.Query)) > 4096 || len([]rune(input.ToolResult)) > 8192 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("query exceeds the 4096 character limit or tool_result exceeds the 8192 character limit"))
		return
	}
	queryText := strings.TrimSpace(strings.Join([]string{input.Query, input.Tool, input.ToolResult}, " "))
	if input.Project == "" || (queryText == "" && len(input.Entities) == 0 && len(input.Topics) == 0) {
		writeError(w, http.StatusUnprocessableEntity, errors.New("project and at least one query/entity/topic are required"))
		return
	}
	if input.Limit < 0 || input.Limit > 20 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("limit must be between 1 and 20"))
		return
	}
	events, err := s.projectKnowledge(r, input.Project, nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not load project knowledge history"))
		return
	}
	knowledge, err := observation.ProjectKnowledge(events)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	hints := observation.FindHints(knowledge, observation.HintQuery{Text: queryText, Entities: input.Entities, Topics: input.Topics, Limit: input.Limit})
	activationID := newUUID()
	context := observation.Context{Project: input.Project, Run: input.Run, Task: input.Task, Actor: input.Actor}
	activationEvent := observation.Event{
		Schema: observation.Schema, EventID: activationID, OccurredAt: s.now().UTC(),
		Source:  observation.Source{ID: "temporality-activation", Integration: "temporality", Version: "1"},
		Context: context, Type: "hint.query",
		Data: map[string]any{"activation_id": activationID, "candidate_count": len(hints), "matcher": "lexical-entity-topic.v1", "has_text_query": queryText != ""},
	}
	if err = appendObservationNow(r, s.store, s.now, activationEvent); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not record hint activation"))
		return
	}
	for i := range hints {
		offerID := newUUID()
		hints[i].HintID = offerID
		hints[i].OfferEventID = offerID
		offered := observation.Event{
			Schema: observation.Schema, EventID: offerID, OccurredAt: s.now().UTC(),
			Source:  observation.Source{ID: "temporality-activation", Integration: "temporality", Version: "1"},
			Context: context, Type: "hint.offered",
			Data: map[string]any{"hint_id": offerID, "activation_id": activationID, "knowledge_id": hints[i].KnowledgeID, "state": hints[i].State, "matched_by": hints[i].MatchedBy},
		}
		if err = appendObservationNow(r, s.store, s.now, offered); err != nil {
			writeError(w, http.StatusInternalServerError, errors.New("could not record offered hint"))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"activation_id": activationID, "matcher": "lexical-entity-topic.v1",
		"context_block": map[string]any{"type": "temporality.memory", "activation_id": activationID, "items": hints},
		"hints":         hints, "count": len(hints),
	})
}

func appendObservationNow(r *http.Request, store observation.Store, now func() time.Time, event observation.Event) error {
	event.ReceivedAt = now().UTC()
	if err := event.Validate(); err != nil {
		return err
	}
	_, err := store.AppendObservation(r.Context(), event)
	return err
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
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
