package httpapi_test

import (
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
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("model path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
	}))
	defer modelServer.Close()
	t.Setenv("TEMPORALITY_MODEL_BASE_URL", modelServer.URL+"/v1")
	t.Setenv("TEMPORALITY_MODEL_ID", "test-model")
	t.Setenv("TEMPORALITY_MODEL_API_KEY", apiKey)
	t.Setenv("TEMPORALITY_MODEL_TEMPERATURE", "")
	t.Setenv("TEMPORALITY_MODEL_TIMEOUT", "")

	handler := httpapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	config := serve(handler, http.MethodGet, "/v1/model/config", nil)
	if config.Code != http.StatusOK || strings.Contains(config.Body.String(), apiKey) {
		t.Fatalf("unsafe model config: status=%d body=%s", config.Code, config.Body.String())
	}
	var publicConfig struct {
		Configured bool `json:"configured"`
		Provenance struct {
			Model       string  `json:"model"`
			Temperature float64 `json:"temperature"`
			TimeoutMS   int64   `json:"timeout_ms"`
		} `json:"provenance"`
	}
	if err = json.Unmarshal(config.Body.Bytes(), &publicConfig); err != nil {
		t.Fatal(err)
	}
	if !publicConfig.Configured || publicConfig.Provenance.Model != "test-model" || publicConfig.Provenance.Temperature != 0 || publicConfig.Provenance.TimeoutMS != 180000 {
		t.Fatalf("unexpected config: %s", config.Body.String())
	}

	body := []byte(`{"frame_id":"` + current.FrameID + `","objective_id":"` + goal.ObjectiveID + `","budget_tokens":8000,"definitions":[]}`)
	response := serve(handler, http.MethodPost, "/v1/model-step", body)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), apiKey) {
		t.Fatalf("model step: status=%d body=%s", response.Code, response.Body.String())
	}
	var result modelstep.Result
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
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
	config := serve(handler, http.MethodGet, "/v1/model/config", nil)
	if config.Code != http.StatusOK || config.Body.String() != "{\"configured\":false}\n" {
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
