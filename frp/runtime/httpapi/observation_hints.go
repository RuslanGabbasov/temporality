package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
)

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
	store, ok := s.observationStore(w)
	if !ok {
		return
	}
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
	if err = appendObservationNow(r, store, s.now, activationEvent); err != nil {
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
		if err = appendObservationNow(r, store, s.now, offered); err != nil {
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
