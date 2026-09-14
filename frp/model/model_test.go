package model_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/model"
	"github.com/temporality-project/temporality/frp/render"
)

func TestOpenAIAdapterSuccessAndDeterministicRequest(t *testing.T) {
	const key = "top-secret"
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+key {
			t.Errorf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		encoded, _ := json.Marshal(body)
		requestBody = string(encoded)
		messages, _ := body["messages"].([]any)
		system, _ := messages[0].(map[string]any)
		systemContent, _ := system["content"].(string)
		if !strings.Contains(systemContent, `"completion":null`) || !strings.Contains(systemContent, "no tools") || !strings.Contains(systemContent, "Never output secrets") {
			t.Errorf("system prompt lacks schema or safety constraints: %q", systemContent)
		}
		format, _ := body["response_format"].(map[string]any)
		if format["type"] != "json_object" {
			t.Errorf("response_format = %#v", format)
		}
		w.Header().Set("Content-Type", "application/json")
		content := "```json\n{\"schema\":\"frp.cognitive-emission.v1\",\"emission_id\":\"e-1\",\"frame_id\":\"frame-1\",\"observation\":[],\"reasoning\":[],\"claims\":[],\"attention\":[],\"actions\":[],\"frame_ops\":[],\"completion\":null}\n```"
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
	defer server.Close()

	adapter, err := model.NewOpenAIAdapter(model.Config{BaseURL: server.URL + "/v1/", Model: "test-model", Temperature: 0.25, Timeout: time.Second}, model.Credentials{APIKey: key}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	emission, err := adapter.Emit(context.Background(), render.Packet{FrameID: "frame-1"})
	if err != nil {
		t.Fatal(err)
	}
	if emission.EmissionID != "e-1" {
		t.Fatalf("emission = %#v", emission)
	}
	if !strings.Contains(requestBody, `"model":"test-model"`) || !strings.Contains(requestBody, `"temperature":0.25`) {
		t.Fatalf("request = %s", requestBody)
	}
}

func TestOpenAIAdapterTrace(t *testing.T) {
	const responseContent = `{"schema":"frp.cognitive-emission.v1","emission_id":"e","frame_id":"frame-1","observation":[],"reasoning":[],"claims":[],"attention":[],"actions":[],"frame_ops":[],"completion":null}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": responseContent}}}})
	}))
	defer server.Close()
	var traces []model.Trace
	adapter, err := model.NewOpenAIAdapter(model.Config{BaseURL: server.URL + "/v1", Model: "m", Timeout: time.Second, Trace: &model.TraceConfig{MaxBytes: 32, Hook: func(trace model.Trace) { traces = append(traces, trace) }}}, model.Credentials{APIKey: "header-only-secret"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.Emit(context.Background(), render.Packet{FrameID: "frame-1"}); err != nil {
		t.Fatal(err)
	}
	if len(traces) != 2 || traces[0].Direction != "request" || traces[1].Direction != "response" {
		t.Fatalf("traces = %#v", traces)
	}
	for _, trace := range traces {
		if !trace.Truncated || len(trace.Payload) != 32 || trace.OriginalBytes <= 32 {
			t.Fatalf("unbounded trace = %#v", trace)
		}
		if strings.Contains(trace.Payload, "header-only-secret") {
			t.Fatalf("credential leaked in trace: %#v", trace)
		}
	}
}

func TestOpenAIAdapterRejectsMalformedAndInvalidEmission(t *testing.T) {
	tests := []struct{ name, content string }{
		{"malformed JSON", `{not-json}`},
		{"unterminated fence", "```json\n{}"},
		{"wrong schema", `{"schema":"other","emission_id":"e","frame_id":"frame-1"}`},
		{"wrong frame", `{"schema":"frp.cognitive-emission.v1","emission_id":"e","frame_id":"frame-2"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": tc.content}}}})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
			defer server.Close()
			adapter, err := model.NewOpenAIAdapter(model.Config{BaseURL: server.URL + "/v1", Model: "m", Timeout: time.Second}, model.Credentials{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = adapter.Emit(context.Background(), render.Packet{FrameID: "frame-1"}); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestSecretAbsentFromProvenance(t *testing.T) {
	const secret = "do-not-serialize"
	adapter, err := model.NewOpenAIAdapter(model.Config{BaseURL: "http://localhost:11434/v1", Model: "llama3", Timeout: 3 * time.Second}, model.Credentials{APIKey: secret}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{adapter.Provenance(), model.Credentials{APIKey: secret}} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) || strings.Contains(strings.ToLower(string(encoded)), "api_key") {
			t.Fatalf("secret leaked: %s", encoded)
		}
	}
}

func TestRecordedAdapter(t *testing.T) {
	emission := cognition.CognitiveEmission{Schema: cognition.EmissionSchema, EmissionID: "e", FrameID: "f", Observation: []cognition.Observation{}, Reasoning: []cognition.Reasoning{}, Claims: []cognition.EmittedClaim{}, Attention: []cognition.AttentionOperation{}, Actions: []cognition.ActionRequest{}, FrameOps: []cognition.FrameOperation{}}
	adapter := &model.RecordedAdapter{Emissions: []cognition.CognitiveEmission{emission}}
	if got, err := adapter.Emit(context.Background(), render.Packet{FrameID: "f"}); err != nil || got.EmissionID != "e" {
		t.Fatalf("got %#v, %v", got, err)
	}
	if _, err := adapter.Emit(context.Background(), render.Packet{FrameID: "f"}); err == nil {
		t.Fatal("exhausted trace succeeded")
	}
}
