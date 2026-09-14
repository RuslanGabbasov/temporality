package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/procedure"
)

type procedureRebuildRequest struct {
	EpisodeID       string `json:"episode_id"`
	MinimumEvidence int    `json:"minimum_evidence"`
}

func (s *Server) rebuildProcedures(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(procedure.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("procedure projections are not supported"))
		return
	}
	source, ok := s.store.(procedure.Source)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("procedure evidence is not supported"))
		return
	}
	var request procedureRebuildRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(request.EpisodeID) == "" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("episode_id is required"))
		return
	}
	if request.MinimumEvidence < 0 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("minimum_evidence must not be negative"))
		return
	}
	projector := procedure.Projector{Config: procedure.BuildConfig{MinimumEvidence: request.MinimumEvidence}}
	if err := projector.Rebuild(r.Context(), request.EpisodeID, source, store); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	values, err := store.ListProcedures(r.Context(), procedure.Filter{EpisodeID: request.EpisodeID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projection_version": projector.Version(), "procedures": values})
}

func (s *Server) listProcedures(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(procedure.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("procedures are not supported"))
		return
	}
	values, err := store.ListProcedures(r.Context(), procedure.Filter{EpisodeID: r.URL.Query().Get("episode_id")})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"procedures": values})
}

func (s *Server) getProcedure(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(procedure.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("procedures are not supported"))
		return
	}
	value, err := store.GetProcedure(r.Context(), r.PathValue("id"))
	if errors.Is(err, procedure.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

type procedureMatchRequest struct {
	FrameID     string  `json:"frame_id"`
	ObjectiveID string  `json:"objective_id"`
	Limit       int     `json:"limit"`
	Threshold   float64 `json:"threshold"`
}

type procedureMatch struct {
	Procedure procedure.Procedure `json:"procedure"`
	Score     float64             `json:"score"`
}

func (s *Server) matchProcedures(w http.ResponseWriter, r *http.Request) {
	procedures, ok := s.store.(procedure.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("procedures are not supported"))
		return
	}
	frames, ok := s.store.(frame.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("frames are not supported"))
		return
	}
	objectives, ok := s.store.(objective.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("objectives are not supported"))
		return
	}
	var request procedureMatchRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(request.FrameID) == "" || strings.TrimSpace(request.ObjectiveID) == "" {
		writeError(w, http.StatusUnprocessableEntity, errors.New("frame_id and objective_id are required"))
		return
	}
	if request.Limit < 0 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("limit must not be negative"))
		return
	}
	if request.Threshold < 0 || request.Threshold > 1 {
		writeError(w, http.StatusUnprocessableEntity, errors.New("threshold must be between 0 and 1"))
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
	goal, err := objectives.GetObjective(r.Context(), request.ObjectiveID)
	if errors.Is(err, objective.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if current.EpisodeID != goal.EpisodeID {
		writeError(w, http.StatusUnprocessableEntity, errors.New("frame and objective must belong to the same episode"))
		return
	}
	values, err := procedures.ListProcedures(r.Context(), procedure.Filter{EpisodeID: goal.EpisodeID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	matches := procedure.MatchProcedures(values, goal, current, procedure.MatchConfig{Threshold: request.Threshold})
	if request.Limit > 0 && len(matches) > request.Limit {
		matches = matches[:request.Limit]
	}
	response := make([]procedureMatch, len(matches))
	for i, match := range matches {
		response[i] = procedureMatch{Procedure: match.Procedure, Score: match.Score}
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": response})
}
