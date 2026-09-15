package httpapi

import (
	"errors"
	"net/http"
	"sort"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/world"
)

// worldForExecution loads the current world and validates that the affordance
// capabilities are granted by it (intent-time policy validation, M11.1).
func (s *Server) worldForExecution(r *http.Request, worldID string, capabilities []string) (world.World, error) {
	if worldID == "" {
		return world.World{}, nil
	}
	store, ok := s.store.(world.Store)
	if !ok {
		return world.World{}, errors.New("worlds are not supported")
	}
	value, err := store.GetWorld(r.Context(), worldID)
	if errors.Is(err, world.ErrWorldNotFound) {
		return world.World{}, err
	}
	if err != nil {
		return world.World{}, err
	}
	if err = value.AuthorizeAll(capabilities); err != nil {
		return world.World{}, err
	}
	return value, nil
}

// requestCapabilities unions the capabilities of all submitted definitions so
// intent-time world validation covers every action the step may create.
func requestCapabilities(definitions map[string]affordance.Definition) []string {
	seen := make(map[string]struct{})
	capabilities := make([]string, 0)
	for _, definition := range definitions {
		for _, capability := range definition.Capabilities {
			if _, ok := seen[capability]; ok {
				continue
			}
			seen[capability] = struct{}{}
			capabilities = append(capabilities, capability)
		}
	}
	sort.Strings(capabilities)
	return capabilities
}

func (s *Server) saveWorld(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(world.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("worlds are not supported"))
		return
	}
	var value world.World
	if err := decodeJSON(r, &value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value.ApplyDefaults()
	now := s.now().UTC()
	event, err := world.StateEvent(value, newUUID(), now)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err = store.SaveWorld(r.Context(), value, event); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"world": value, "event": event})
}

func (s *Server) getWorld(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(world.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("worlds are not supported"))
		return
	}
	value, err := store.GetWorld(r.Context(), r.PathValue("id"))
	if errors.Is(err, world.ErrWorldNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) listWorlds(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(world.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("worlds are not supported"))
		return
	}
	values, err := store.ListWorlds(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"worlds": values})
}

// listAffordances serves the canonical affordance registry (read, write, and
// semantic definitions). External callers should submit these definitions
// verbatim: the substrate freezes definitions by id, so hand-crafted variants
// of a standard id are rejected as frozen rather than silently redefined.
func (s *Server) listAffordances(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"definitions": world.StandardDefinitions()})
}
