package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/model"
	"github.com/temporality-project/temporality/frp/runtime/modelstep"
	runtimeStep "github.com/temporality-project/temporality/frp/runtime/step"
)

const modelConfigHint = "set TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID"

type modelStepRequest struct {
	FrameID      string                  `json:"frame_id"`
	ObjectiveID  string                  `json:"objective_id"`
	BudgetTokens int                     `json:"budget_tokens"`
	Definitions  []affordance.Definition `json:"definitions"`
}

type modelConfigResponse struct {
	Configured bool              `json:"configured"`
	Provenance *model.Provenance `json:"provenance,omitempty"`
}

func (s *Server) modelConfig(w http.ResponseWriter, _ *http.Request) {
	adapter, err := modelAdapterFromEnv()
	if err != nil {
		writeJSON(w, http.StatusOK, modelConfigResponse{Configured: false})
		return
	}
	provenance := adapter.Provenance()
	writeJSON(w, http.StatusOK, modelConfigResponse{Configured: true, Provenance: &provenance})
}

func (s *Server) modelStep(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(modelstep.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("model step is not supported by this store"))
		return
	}
	adapter, err := modelAdapterFromEnv()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("model is not configured: %w; %s", err, modelConfigHint))
		return
	}
	var request modelStepRequest
	if err = decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	definitions := make(map[string]affordance.Definition, len(request.Definitions))
	for _, definition := range request.Definitions {
		definition.ApplyDefaults()
		if _, exists := definitions[definition.ID]; exists {
			writeError(w, http.StatusBadRequest, errors.New("duplicate affordance definition"))
			return
		}
		definitions[definition.ID] = definition
	}
	startedAt := time.Now()
	result, err := (modelstep.Service{
		Store:           store,
		Adapter:         adapter,
		ModelProvenance: adapter.Provenance(),
		NewID:           newUUID,
		Now:             func() time.Time { return s.now().UTC() },
	}).Run(r.Context(), modelstep.Input{
		FrameID:      request.FrameID,
		ObjectiveID:  request.ObjectiveID,
		BudgetTokens: request.BudgetTokens,
		Definitions:  definitions,
	})
	if err == nil {
		s.log.Info("model step completed", "frame_id", request.FrameID, "model", adapter.Provenance().Model, "duration_ms", time.Since(startedAt).Milliseconds())
		writeJSON(w, http.StatusCreated, result)
		return
	}
	s.log.Warn("model step failed", "frame_id", request.FrameID, "model", adapter.Provenance().Model, "duration_ms", time.Since(startedAt).Milliseconds(), "error", err)
	if strings.HasPrefix(err.Error(), "model emit:") {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if errors.Is(err, frame.ErrFrameNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, runtimeStep.ErrCurrentFrameChanged) {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeError(w, http.StatusUnprocessableEntity, err)
}

func modelAdapterFromEnv() (*model.OpenAIAdapter, error) {
	baseURL := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_BASE_URL"))
	modelID := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_ID"))
	if baseURL == "" || modelID == "" {
		return nil, errors.New("TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID are required")
	}
	temperature := 0.0
	if value := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_TEMPERATURE")); value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid TEMPORALITY_MODEL_TEMPERATURE: %w", err)
		}
		temperature = parsed
	}
	timeout := 180 * time.Second
	if value := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("invalid TEMPORALITY_MODEL_TIMEOUT: %w", err)
		}
		timeout = parsed
	}
	return model.NewOpenAIAdapter(model.Config{
		BaseURL:     baseURL,
		Model:       modelID,
		Temperature: temperature,
		Timeout:     timeout,
	}, model.Credentials{APIKey: os.Getenv("TEMPORALITY_MODEL_API_KEY")}, nil)
}
