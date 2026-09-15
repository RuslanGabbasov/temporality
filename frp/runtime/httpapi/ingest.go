package httpapi

import (
	"errors"
	"net/http"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/ingest"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/world"
)

// ingestSource runs one bounded ingestion run over a declared world resource
// (M13). Observations are committed as canonical world.observation events and
// deterministic extractors create candidate claims citing those events, so the
// substrate is populated only through the event log.
func (s *Server) ingestSource(w http.ResponseWriter, r *http.Request) {
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
	var request ingest.Request
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value, err := worldStore.GetWorld(r.Context(), request.WorldID)
	if errors.Is(err, world.ErrWorldNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.log.Error("ingest load world", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	runner := &ingest.Runner{World: value, Events: s.store, Claims: claimStore, NewID: newUUID, Now: s.now}
	result, runErr := runner.Run(r.Context(), request)
	if runErr != nil {
		// Partial runs still committed observations; return them alongside the
		// error so callers know exactly what entered the substrate.
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": runErr.Error(), "result": result})
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// claimEvidence returns the events backing a claim, excluding the claim's own
// creation event (M13 provenance chain: claim -> evidence -> observations).
func (s *Server) claimEvidence(w http.ResponseWriter, r *http.Request) {
	claimStore, ok := s.store.(cognition.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("claims are not supported"))
		return
	}
	claimID := r.PathValue("id")
	ids, err := claimStore.ListClaimEvidence(r.Context(), claimID)
	if errors.Is(err, cognition.ErrClaimNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.log.Error("list claim evidence", "error", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
		return
	}
	events := make([]protocol.Event, 0, len(ids))
	for _, id := range ids {
		event, eventErr := s.store.Get(r.Context(), id)
		if eventErr != nil {
			s.log.Error("load evidence event", "error", eventErr)
			writeError(w, http.StatusInternalServerError, errors.New("internal error"))
			return
		}
		events = append(events, event)
	}
	writeJSON(w, http.StatusOK, map[string]any{"claim_id": claimID, "evidence": ids, "events": events})
}
