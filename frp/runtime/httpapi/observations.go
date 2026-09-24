package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/temporality-project/temporality/observation"
)

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

func (s *Server) observationStore(w http.ResponseWriter) (observation.Store, bool) {
	store, ok := s.store.(observation.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("universal observation storage is not supported"))
	}
	return store, ok
}

func (s *Server) appendObservations(w http.ResponseWriter, r *http.Request) {
	store, ok := s.observationStore(w)
	if !ok {
		return
	}
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
		inserted, err := store.AppendObservation(r.Context(), event)
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
	store, ok := s.observationStore(w)
	if !ok {
		return
	}
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
	page, err := store.ListObservationPage(r.Context(), observation.Filter{
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
