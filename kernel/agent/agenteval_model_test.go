package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
)

// evalFakeWorkspace fakes the workspace endpoints RunAgentEvaluationMeta
// reads: the agent header, its current prompt, its evaluation suite and the
// evaluation-runs recorder. The recorded payload is captured for assertions.
func evalFakeWorkspace(t *testing.T, recorded *map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/workspace/agents/lead", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "lead", "name": "Lead", "definition_version": 3})
	})
	mux.HandleFunc("/v1/workspace/agents/lead/prompt", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt": "You are lead.", "version": 3})
	})
	mux.HandleFunc("/v1/workspace/agents/lead/evaluation-suite", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"cases": []any{
			map[string]any{"name": "greet", "input": "say ok", "must_contain": []string{"ok"}, "check": "contains"},
		}})
	})
	mux.HandleFunc("/v1/workspace/agents/lead/evaluation-runs", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		*recorded = payload
		_, _ = w.Write([]byte(`{"id": 11, "verdict": "pass"}`))
	})
	return httptest.NewServer(mux)
}

// evalFakeModel fakes the chat-completions endpoint the resolved model client
// calls; it records the model id it was asked for (provenance check).
func evalFakeModel(t *testing.T, answer string, askedModel *string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		*askedModel = req.Model
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": answer}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 2},
		})
	}))
}

func evalTestActivities(workspace, model *httptest.Server) *Activities {
	return &Activities{
		HTTP:         &http.Client{Timeout: 5 * time.Second},
		WorkspaceURL: workspace.URL,
		EvalModel: func(ctx context.Context) (*llm.Client, error) {
			return llm.New(llm.Config{BaseURL: model.URL, Model: "judge-fallback", APIKey: "k"}), nil
		},
	}
}

// The regression behind gate_run_aborted on fresh installs: with no env
// gateway (a.Model == nil) the runner must run cases on the injected quality
// chain instead of aborting before the first case.
func TestAgentEvaluationResolvesModelWithoutGateway(t *testing.T) {
	var recorded map[string]any
	workspace := evalFakeWorkspace(t, &recorded)
	defer workspace.Close()
	var askedModel string
	model := evalFakeModel(t, "ok done", &askedModel)
	defer model.Close()

	activities := evalTestActivities(workspace, model) // a.Model intentionally nil
	summary, err := activities.RunAgentEvaluation(context.Background(), "lead", 0)
	if err != nil {
		t.Fatalf("evaluation must run on the fallback chain: %v", err)
	}
	if !strings.Contains(summary, "1 passed") {
		t.Fatalf("summary = %q, want 1 passed", summary)
	}
	if askedModel != "judge-fallback" {
		t.Errorf("cases ran on model %q, want judge-fallback", askedModel)
	}
	if recorded["tested_model"] != "judge-fallback" {
		t.Errorf("tested_model provenance = %v, want judge-fallback", recorded["tested_model"])
	}
}

// The env gateway keeps precedence when present: the fallback chain must not
// hijack installs that configured TEMPORALITY_MODEL_*.
func TestAgentEvaluationGatewayWinsOverChain(t *testing.T) {
	var recorded map[string]any
	workspace := evalFakeWorkspace(t, &recorded)
	defer workspace.Close()
	var chainHits int
	chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chainHits++
		http.Error(w, "chain must not be called", http.StatusForbidden)
	}))
	defer chain.Close()
	var askedModel string
	gateway := evalFakeModel(t, "ok fine", &askedModel)
	defer gateway.Close()

	activities := evalTestActivities(workspace, chain)
	activities.Model = llm.New(llm.Config{BaseURL: gateway.URL, Model: "env-gateway", APIKey: "k"})
	if _, err := activities.RunAgentEvaluation(context.Background(), "lead", 0); err != nil {
		t.Fatalf("evaluation must run on the gateway: %v", err)
	}
	if askedModel != "env-gateway" {
		t.Errorf("cases ran on model %q, want env-gateway", askedModel)
	}
	if chainHits != 0 {
		t.Errorf("fallback chain calls = %d, want 0", chainHits)
	}
}

// A chain that cannot resolve anything must fail loud with the chain's
// message — not the generic unconfigured-client error — so the gate row and
// the kernel log tell the operator what to configure.
func TestAgentEvaluationFailsLoudWhenChainErrors(t *testing.T) {
	var recorded map[string]any
	workspace := evalFakeWorkspace(t, &recorded)
	defer workspace.Close()

	activities := evalTestActivities(workspace, nil)
	activities.EvalModel = func(ctx context.Context) (*llm.Client, error) {
		return nil, errors.New("judge offline")
	}
	_, err := activities.RunAgentEvaluation(context.Background(), "lead", 0)
	if err == nil {
		t.Fatal("an unresolvable chain must fail the run")
	}
	if !strings.Contains(err.Error(), "resolve evaluation model") || !strings.Contains(err.Error(), "judge offline") {
		t.Errorf("error = %v, want the wrapped chain message", err)
	}
}
