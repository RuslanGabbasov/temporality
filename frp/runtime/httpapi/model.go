package httpapi

import (
	"context"
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
	"github.com/temporality-project/temporality/frp/world"
)

const (
	modelConfigHint             = "set TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID"
	modelLogDefaultBytes        = 65536
	modelLogMinBytes            = 1024
	modelLogMaxBytes            = 1024 * 1024
	modelMaxOutputTokensDefault = 1024
	modelMaxOutputTokensMin     = 64
	modelMaxOutputTokensMax     = 65536
)

var errModelNotConfigured = errors.New("TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID are required")

type modelStepRequest struct {
	FrameID      string                  `json:"frame_id"`
	ObjectiveID  string                  `json:"objective_id"`
	BudgetTokens int                     `json:"budget_tokens"`
	Definitions  []affordance.Definition `json:"definitions"`
	WorldID      string                  `json:"world_id"`
}

type modelConfigResponse struct {
	Configured     bool              `json:"configured"`
	PayloadLogging bool              `json:"payload_logging"`
	LogMaxBytes    int               `json:"log_max_bytes"`
	Provenance     *model.Provenance `json:"provenance,omitempty"`
	Error          string            `json:"error,omitempty"`
}

type modelRuntimeConfig struct {
	model.Config
	credentials    model.Credentials
	payloadLogging bool
	logMaxBytes    int
}

func (s *Server) modelConfig(w http.ResponseWriter, _ *http.Request) {
	config, err := modelConfigFromEnv()
	response := modelConfigResponse{PayloadLogging: config.payloadLogging, LogMaxBytes: config.logMaxBytes}
	if err != nil {
		if errors.Is(err, errModelNotConfigured) {
			writeJSON(w, http.StatusOK, response)
			return
		}
		response.Error = err.Error()
		writeJSON(w, http.StatusServiceUnavailable, response)
		return
	}
	adapter, err := model.NewOpenAIAdapter(config.Config, config.credentials, nil)
	if err != nil {
		response.Error = err.Error()
		writeJSON(w, http.StatusServiceUnavailable, response)
		return
	}
	provenance := adapter.Provenance()
	response.Configured = true
	response.Provenance = &provenance
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) modelStep(w http.ResponseWriter, r *http.Request) {
	store, ok := s.store.(modelstep.Store)
	if !ok {
		writeError(w, http.StatusNotImplemented, errors.New("model step is not supported by this store"))
		return
	}
	config, err := modelConfigFromEnv()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("model is not configured: %w; %s", err, modelConfigHint))
		return
	}
	var request modelStepRequest
	if err = decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if config.payloadLogging {
		config.Trace = &model.TraceConfig{MaxBytes: config.logMaxBytes, Hook: func(trace model.Trace) {
			s.log.Info("model "+trace.Direction+" payload", "frame_id", request.FrameID, "model", config.Model, "payload", trace.Payload, "truncated", trace.Truncated, "original_bytes", trace.OriginalBytes)
		}}
	}
	adapter, err := model.NewOpenAIAdapter(config.Config, config.credentials, nil)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("model is not configured: %w; %s", err, modelConfigHint))
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
	// Intent-time world validation, mirroring /v1/step: the model's actions run
	// against the world only if it grants every declared capability.
	boundWorld, err := s.worldForExecution(r, request.WorldID, requestCapabilities(definitions))
	if errors.Is(err, world.ErrWorldNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	startedAt := time.Now()
	s.log.Info("model step started", "frame_id", request.FrameID, "model", adapter.Provenance().Model, "timeout_ms", adapter.Provenance().TimeoutMS)
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
		WorldID:      boundWorld.WorldID,
		WorldVersion: boundWorld.StateVersion,
	})
	if err == nil {
		s.log.Info("model step completed", "frame_id", request.FrameID, "model", adapter.Provenance().Model, "duration_ms", time.Since(startedAt).Milliseconds(), "prompt_tokens", result.ModelUsage.PromptTokens, "completion_tokens", result.ModelUsage.CompletionTokens)
		writeJSON(w, http.StatusCreated, result)
		return
	}
	s.log.Warn("model step failed", "frame_id", request.FrameID, "model", adapter.Provenance().Model, "duration_ms", time.Since(startedAt).Milliseconds(), "error", err)
	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, fmt.Errorf("model provider did not respond within configured timeout of %s", config.Timeout))
		return
	}
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

func modelConfigFromEnv() (modelRuntimeConfig, error) {
	config := modelRuntimeConfig{logMaxBytes: modelLogDefaultBytes}
	loggingValue := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_LOG_PAYLOADS"))
	if loggingValue != "" {
		if loggingValue != "true" && loggingValue != "false" {
			return config, errors.New("invalid TEMPORALITY_MODEL_LOG_PAYLOADS: use exactly true or false")
		}
		config.payloadLogging = loggingValue == "true"
	}
	if value := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_LOG_MAX_BYTES")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < modelLogMinBytes || parsed > modelLogMaxBytes {
			return config, fmt.Errorf("invalid TEMPORALITY_MODEL_LOG_MAX_BYTES: must be an integer between %d and %d", modelLogMinBytes, modelLogMaxBytes)
		}
		config.logMaxBytes = parsed
	}
	baseURL := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_BASE_URL"))
	modelID := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_ID"))
	if baseURL == "" || modelID == "" {
		return config, errModelNotConfigured
	}
	temperature := 0.0
	if value := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_TEMPERATURE")); value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return config, fmt.Errorf("invalid TEMPORALITY_MODEL_TEMPERATURE: %w", err)
		}
		temperature = parsed
	}
	timeout := 180 * time.Second
	if value := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return config, fmt.Errorf("invalid TEMPORALITY_MODEL_TIMEOUT: %w", err)
		}
		timeout = parsed
	}
	maxOutputTokens := modelMaxOutputTokensDefault
	if value := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < modelMaxOutputTokensMin || parsed > modelMaxOutputTokensMax {
			return config, fmt.Errorf("invalid TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS: must be an integer between %d and %d", modelMaxOutputTokensMin, modelMaxOutputTokensMax)
		}
		maxOutputTokens = parsed
	}
	config.Config = model.Config{
		BaseURL:         baseURL,
		Model:           modelID,
		Temperature:     temperature,
		Timeout:         timeout,
		MaxOutputTokens: maxOutputTokens,
		Reasoning:       strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_REASONING")),
	}
	config.credentials = model.Credentials{APIKey: os.Getenv("TEMPORALITY_MODEL_API_KEY")}
	return config, nil
}
