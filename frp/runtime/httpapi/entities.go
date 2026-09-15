package httpapi

import (
	"errors"
	"net/http"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/entity"
	"github.com/temporality-project/temporality/frp/projection"
)

// rebuildEntities rebuilds the global entity graph from ALL claims with
// triples, regardless of episode. The projection is replaced atomically.
func (s *Server) rebuildEntities(w http.ResponseWriter, r *http.Request) {
	claimStore, ok := s.store.(cognition.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("claims are not supported"))
		return
	}
	entityStore, ok := s.store.(entity.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("entities are not supported"))
		return
	}
	claims, err := claimStore.ListClaims(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	entities, relations := projection.BuildEntities(claims)
	if err = entityStore.ReplaceAllEntities(r.Context(), entities, relations); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"projection_version": projection.EntityProjectorVersion,
		"entities":           entities,
		"relations":          relations,
	})
}

func (s *Server) listEntities(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(entity.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("entities are not supported"))
		return
	}
	filter := entity.Filter{Type: r.URL.Query().Get("type"), Name: r.URL.Query().Get("name")}
	if filter.Type != "" {
		if err := entity.ValidateType(filter.Type); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	entities, err := store.ListEntities(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": entities})
}

func (s *Server) getEntity(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(entity.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("entities are not supported"))
		return
	}
	id := r.PathValue("id")
	value, err := store.GetEntity(r.Context(), id)
	if errors.Is(err, entity.ErrEntityNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	relations, err := store.ListEntityRelations(r.Context(), entity.RelationFilter{EntityID: id})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entity": value, "relations": relations})
}
