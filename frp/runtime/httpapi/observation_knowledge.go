package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
)

type invalidateObservationRequest struct {
	KnowledgeID string                 `json:"knowledge_id"`
	Project     string                 `json:"project"`
	Run         string                 `json:"run,omitempty"`
	Actor       observation.Actor      `json:"actor"`
	Reason      string                 `json:"reason"`
	Evidence    []observation.Evidence `json:"evidence,omitempty"`
}

func (s *Server) projectKnowledge(r *http.Request, project string, asOf, knownAt *time.Time) ([]observation.Event, error) {
	store, ok := s.store.(observation.Store)
	if !ok {
		return nil, errors.New("universal observation storage is not supported")
	}
	filter := observation.Filter{Project: project, Until: asOf, KnownAt: knownAt, Limit: 500}
	var result []observation.Event
	cursor := ""
	for {
		page, err := store.ListObservationPage(r.Context(), filter, cursor)
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
	store, ok := s.observationStore(w)
	if !ok {
		return
	}
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
	if _, err = store.AppendObservation(r.Context(), event); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("could not record invalidation"))
		return
	}
	writeJSON(w, http.StatusCreated, event)
}
