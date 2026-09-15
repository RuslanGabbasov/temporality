package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/frame"
	"github.com/temporality-project/temporality/frp/objective"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/runtime/httpapi"
	"github.com/temporality-project/temporality/frp/runtime/modelstep"
	runtimeStep "github.com/temporality-project/temporality/frp/runtime/step"
	"github.com/temporality-project/temporality/frp/substrate/memory"
)

func TestModelStepFromEnvironment(t *testing.T) {
	const apiKey = "model-secret-that-must-not-leak"
	store, current, goal := modelStepStore(t)
	emission := cognition.CognitiveEmission{
		Schema:     cognition.EmissionSchema,
		EmissionID: "model-emission",
		FrameID:    current.FrameID,
		Claims: []cognition.EmittedClaim{{
			Proposition: "model step committed atomically",
			Confidence:  0.9,
			Status:      cognition.ClaimCandidate,
		}},
	}
	content, err := json.Marshal(emission)
	if err != nil {
		t.Fatal(err)
	}
	var maxTokens float64
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("model path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("authorization = %q", got)
		}
		var requestBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Error(err)
		}
		maxTokens, _ = requestBody["max_tokens"].(float64)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
	}))
	defer modelServer.Close()
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", modelServer.URL+"/v1")
	t.Setenv("TEMPORALITY_MODEL_ID", "test-model")
	t.Setenv("TEMPORALITY_MODEL_API_KEY", apiKey)
	t.Setenv("TEMPORALITY_MODEL_TEMPERATURE", "")
	t.Setenv("TEMPORALITY_MODEL_TIMEOUT", "")
	t.Setenv("TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS", "")
	t.Setenv("TEMPORALITY_MODEL_LOG_PAYLOADS", "false")
	t.Setenv("TEMPORALITY_MODEL_LOG_MAX_BYTES", "")

	var logs bytes.Buffer
	handler := httpapi.New(store, slog.New(slog.NewTextHandler(&logs, nil)))
	config := serve(handler, http.MethodGet, "/v1/model/config", nil)
	if config.Code != http.StatusOK || strings.Contains(config.Body.String(), apiKey) {
		t.Fatalf("unsafe model config: status=%d body=%s", config.Code, config.Body.String())
	}
	var publicConfig struct {
		Configured bool `json:"configured"`
		Provenance struct {
			Model           string  `json:"model"`
			Temperature     float64 `json:"temperature"`
			TimeoutMS       int64   `json:"timeout_ms"`
			MaxOutputTokens int     `json:"max_output_tokens"`
		} `json:"provenance"`
	}
	if err = json.Unmarshal(config.Body.Bytes(), &publicConfig); err != nil {
		t.Fatal(err)
	}
	if !publicConfig.Configured || publicConfig.Provenance.Model != "test-model" || publicConfig.Provenance.Temperature != 0 || publicConfig.Provenance.TimeoutMS != 180000 || publicConfig.Provenance.MaxOutputTokens != 1024 {
		t.Fatalf("unexpected config: %s", config.Body.String())
	}

	body := []byte(`{"frame_id":"` + current.FrameID + `","objective_id":"` + goal.ObjectiveID + `","budget_tokens":8000,"definitions":[]}`)
	response := serve(handler, http.MethodPost, "/v1/model-step", body)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), apiKey) || maxTokens != 1024 {
		t.Fatalf("model step: status=%d body=%s", response.Code, response.Body.String())
	}
	var result modelstep.Result
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "model request payload") || strings.Contains(logs.String(), "model response payload") || strings.Contains(logs.String(), apiKey) {
		t.Fatalf("disabled logging leaked payload or key: %s", logs.String())
	}
	if len(result.Step.Claims) != 1 || result.Step.Frame.Revision != current.Revision+1 {
		t.Fatalf("atomic output was not committed: %#v", result.Step)
	}
	suggestions := 0
	for _, event := range result.Step.Events {
		if event.Type == runtimeStep.EventAttentionSuggested {
			suggestions++
		}
	}
	if suggestions == 0 {
		t.Fatal("render attention suggestions were not committed")
	}
	if _, err = store.GetCognitiveStep(context.Background(), emission.EmissionID); err != nil {
		t.Fatalf("cognitive step was not persisted atomically: %v", err)
	}
}

func TestModelStepUnavailableAndBadGateway(t *testing.T) {
	store, current, goal := modelStepStore(t)
	handler := httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", "")
	t.Setenv("TEMPORALITY_MODEL_ID", "")
	t.Setenv("TEMPORALITY_MODEL_LOG_PAYLOADS", "false")
	t.Setenv("TEMPORALITY_MODEL_LOG_MAX_BYTES", "")
	config := serve(handler, http.MethodGet, "/v1/model/config", nil)
	if config.Code != http.StatusOK || config.Body.String() != "{\"configured\":false,\"payload_logging\":false,\"log_max_bytes\":65536}\n" {
		t.Fatalf("unconfigured config: status=%d body=%s", config.Code, config.Body.String())
	}
	unavailable := serve(handler, http.MethodPost, "/v1/model-step", []byte(`{}`))
	if unavailable.Code != http.StatusServiceUnavailable || !strings.Contains(unavailable.Body.String(), "TEMPORALITY_MODEL_BASE_URL") {
		t.Fatalf("unavailable response: status=%d body=%s", unavailable.Code, unavailable.Body.String())
	}

	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream failed", http.StatusTooManyRequests)
	}))
	defer modelServer.Close()
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", modelServer.URL+"/v1")
	t.Setenv("TEMPORALITY_MODEL_ID", "test-model")
	body := []byte(`{"frame_id":"` + current.FrameID + `","objective_id":"` + goal.ObjectiveID + `","budget_tokens":8000,"definitions":[]}`)
	badGateway := serve(handler, http.MethodPost, "/v1/model-step", body)
	if badGateway.Code != http.StatusBadGateway {
		t.Fatalf("bad gateway response: status=%d body=%s", badGateway.Code, badGateway.Body.String())
	}
}

func TestModelStepProviderTimeoutReturnsJSON(t *testing.T) {
	store, current, goal := modelStepStore(t)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{}})
	}))
	defer modelServer.Close()
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", modelServer.URL+"/v1")
	t.Setenv("TEMPORALITY_MODEL_ID", "slow-model")
	t.Setenv("TEMPORALITY_MODEL_TIMEOUT", "10ms")
	t.Setenv("TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS", "1024")
	t.Setenv("TEMPORALITY_MODEL_LOG_PAYLOADS", "false")
	t.Setenv("TEMPORALITY_MODEL_LOG_MAX_BYTES", "")
	handler := httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := []byte(`{"frame_id":"` + current.FrameID + `","objective_id":"` + goal.ObjectiveID + `","budget_tokens":8000,"definitions":[]}`)
	response := serve(handler, http.MethodPost, "/v1/model-step", body)
	if response.Code != http.StatusGatewayTimeout || response.Header().Get("Content-Type") != "application/json" || !strings.Contains(response.Body.String(), "provider did not respond within configured timeout") {
		t.Fatalf("timeout response: status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestModelPayloadLoggingAndInvalidConfiguration(t *testing.T) {
	const apiKey = "authorization-secret-never-logged"
	store, current, goal := modelStepStore(t)
	emission := cognition.CognitiveEmission{Schema: cognition.EmissionSchema, EmissionID: "logged-emission", FrameID: current.FrameID}
	content, err := json.Marshal(emission)
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, []byte(strings.Repeat(" ", 2000))...)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
	}))
	defer modelServer.Close()
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", modelServer.URL+"/v1")
	t.Setenv("TEMPORALITY_MODEL_ID", "logged-model")
	t.Setenv("TEMPORALITY_MODEL_API_KEY", apiKey)
	t.Setenv("TEMPORALITY_MODEL_TEMPERATURE", "")
	t.Setenv("TEMPORALITY_MODEL_TIMEOUT", "")
	t.Setenv("TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS", "")
	t.Setenv("TEMPORALITY_MODEL_LOG_PAYLOADS", "true")
	t.Setenv("TEMPORALITY_MODEL_LOG_MAX_BYTES", "1024")
	var logs bytes.Buffer
	handler := httpapi.New(store, slog.New(slog.NewJSONHandler(&logs, nil)))
	config := serve(handler, http.MethodGet, "/v1/model/config", nil)
	if !strings.Contains(config.Body.String(), `"payload_logging":true`) || !strings.Contains(config.Body.String(), `"log_max_bytes":1024`) || strings.Contains(config.Body.String(), apiKey) {
		t.Fatalf("unsafe config: %s", config.Body.String())
	}
	body := []byte(`{"frame_id":"` + current.FrameID + `","objective_id":"` + goal.ObjectiveID + `","budget_tokens":8000,"definitions":[]}`)
	response := serve(handler, http.MethodPost, "/v1/model-step", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("model step: status=%d body=%s", response.Code, response.Body.String())
	}
	logged := logs.String()
	for _, want := range []string{"model request payload", "model response payload", `"frame_id":"` + current.FrameID + `"`, `"model":"logged-model"`, `"truncated":true`, `"original_bytes":`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log lacks %q: %s", want, logged)
		}
	}
	if strings.Contains(logged, apiKey) || strings.Contains(logged, "Authorization") {
		t.Fatalf("credential leaked: %s", logged)
	}

	t.Setenv("TEMPORALITY_MODEL_LOG_PAYLOADS", "1")
	invalid := serve(handler, http.MethodPost, "/v1/model-step", body)
	if invalid.Code != http.StatusServiceUnavailable || !strings.Contains(invalid.Body.String(), "exactly true or false") {
		t.Fatalf("invalid boolean was not actionable: status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	invalidConfig := serve(handler, http.MethodGet, "/v1/model/config", nil)
	if invalidConfig.Code != http.StatusServiceUnavailable || !strings.Contains(invalidConfig.Body.String(), `"configured":false`) || strings.Contains(invalidConfig.Body.String(), apiKey) {
		t.Fatalf("invalid public config: status=%d body=%s", invalidConfig.Code, invalidConfig.Body.String())
	}

	t.Setenv("TEMPORALITY_MODEL_LOG_PAYLOADS", "false")
	t.Setenv("TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS", "63")
	invalidTokens := serve(handler, http.MethodGet, "/v1/model/config", nil)
	if invalidTokens.Code != http.StatusServiceUnavailable || !strings.Contains(invalidTokens.Body.String(), "between 64 and 65536") {
		t.Fatalf("invalid max output tokens: status=%d body=%s", invalidTokens.Code, invalidTokens.Body.String())
	}
}

func modelStepStore(t *testing.T) (*memory.Store, frame.Frame, objective.Objective) {
	t.Helper()
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	current := frame.Frame{FrameID: "10000000-0000-4000-8000-000000000001", AgentID: "10000000-0000-4000-8000-000000000002", EpisodeID: "10000000-0000-4000-8000-000000000003", BranchID: "10000000-0000-4000-8000-000000000004", ObjectiveID: "10000000-0000-4000-8000-000000000005", AsOf: now, Focus: frame.Focus{Type: frame.RefQuery, Query: "inspect work"}, Attention: frame.Attention{Ambient: true}}
	current.ApplyDefaults()
	goal := objective.Objective{ObjectiveID: current.ObjectiveID, EpisodeID: current.EpisodeID, Text: "inspect work"}
	goal.ApplyDefaults()
	event := func(id, kind string, payload map[string]any) protocol.Event {
		value := protocol.Event{EventID: id, TransactionTime: now, ValidTime: now, AgentID: current.AgentID, EpisodeID: current.EpisodeID, BranchID: current.BranchID, Type: kind, Payload: payload, Provenance: map[string]any{"source": "httpapi-model-test"}}
		value.ApplyDefaults(now)
		return value
	}
	if err := store.CreateObjective(ctx, goal, event("10000000-0000-4000-8000-000000000006", "episode.started", map[string]any{"objective_id": goal.ObjectiveID})); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFrame(ctx, current, event("10000000-0000-4000-8000-000000000007", "frame.created", map[string]any{"frame_id": current.FrameID})); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, event("10000000-0000-4000-8000-000000000008", "observation.recorded", map[string]any{"text": "work ready"})); err != nil {
		t.Fatal(err)
	}
	return store, current, goal
}
