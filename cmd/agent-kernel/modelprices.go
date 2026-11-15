package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/cost"
	"github.com/temporality-project/temporality/workspace"
)

// Model token prices — the settings surface for what KERNEL_MODEL_PRICES used
// to be the only way to say (docs/plan-cost-accounting.md). The env value is
// the seed; a list saved here replaces it until it is cleared, and a save is
// applied to the RUNNING kernel through the shared cost.Config table, so the
// next run is already priced by it.

// maxModelPrices bounds one request: every catalog the UI can render fits
// well below it, and it keeps a hostile payload small.
const maxModelPrices = 500

// activeModelPrices picks the list the kernel runs on: whatever was saved in
// the UI replaces the KERNEL_MODEL_PRICES seed wholesale, an empty saved list
// falls back to the seed. A repeated model keeps its last row (the same rule
// SetPrices applies) and the result is ordered by model, so the payload is
// diffable whatever order the request arrived in.
func activeModelPrices(stored []workspace.ModelPrice, seed []cost.Price) ([]cost.Price, string) {
	if len(stored) == 0 {
		return seed, "env"
	}
	byModel := make(map[string]cost.Price, len(stored))
	for _, price := range stored {
		model := strings.TrimSpace(price.Model)
		byModel[model] = cost.Price{Model: model, PromptPer1M: price.PromptPer1M, CompPer1M: price.CompPer1M}
	}
	list := make([]cost.Price, 0, len(byModel))
	for _, price := range byModel {
		list = append(list, price)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Model < list[j].Model })
	return list, "db"
}

// RegisterModelPriceRoutes mounts GET/PUT /v1/agent/cost/prices. Reads are
// reader-gated (the price list is installation-wide, like the supervisor
// panel's), writes operator-gated.
func RegisterModelPriceRoutes(mux *http.ServeMux, gate *controlplane.Gate, ws *workspace.Store, seed []cost.Price, live cost.Config) {
	mux.HandleFunc("GET /v1/agent/cost/prices", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleReader) {
			return
		}
		stored, err := ws.ModelPrices(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		list, source := activeModelPrices(stored, seed)
		writeJSON(w, 200, map[string]any{"prices": list, "source": source})
	})

	mux.HandleFunc("PUT /v1/agent/cost/prices", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			Prices []workspace.ModelPrice `json:"prices"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, 400, err)
			return
		}
		if len(req.Prices) > maxModelPrices {
			writeError(w, 422, fmt.Errorf("%d prices, the cap is %d", len(req.Prices), maxModelPrices))
			return
		}
		for i := range req.Prices {
			req.Prices[i].Model = strings.TrimSpace(req.Prices[i].Model)
		}
		list, source := activeModelPrices(req.Prices, seed)
		// Rules first (nothing is written for a bad list), persistence second,
		// live application last — a failed save must not leave the kernel
		// running on prices the journal never recorded.
		if err := cost.ValidatePrices(list); err != nil {
			writeError(w, 422, err)
			return
		}
		if err := ws.SaveModelPrices(r.Context(), req.Prices); err != nil {
			writeError(w, 500, err)
			return
		}
		if err := live.SetPrices(list); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"prices": list, "source": source})
	})
}
