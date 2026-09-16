package model_test

import (
	"context"
	"encoding/json"
	"fmt"
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
		if !strings.Contains(systemContent, `"completion":null`) || !strings.Contains(systemContent, `kind "answer"`) || !strings.Contains(systemContent, "same language") || !strings.Contains(systemContent, "no tools") || !strings.Contains(systemContent, "Never output secrets") {
			t.Errorf("system prompt lacks schema or safety constraints: %q", systemContent)
		}
		format, _ := body["response_format"].(map[string]any)
		if format["type"] != "json_object" {
			t.Errorf("response_format = %#v", format)
		}
		if len(body) != 5 || body["model"] != "test-model" || body["temperature"] != 0.25 || body["max_tokens"] != float64(2048) {
			t.Errorf("exact wire body fields = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		content := "```json\n{\"schema\":\"frp.cognitive-emission.v1\",\"emission_id\":\"e-1\",\"frame_id\":\"frame-1\",\"observation\":[],\"reasoning\":[],\"claims\":[],\"attention\":[],\"actions\":[],\"frame_ops\":[],\"completion\":null}\n```"
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}, "usage": map[string]any{"prompt_tokens": 321, "completion_tokens": 123, "total_tokens": 444}})
	}))
	defer server.Close()

	adapter, err := model.NewOpenAIAdapter(model.Config{BaseURL: server.URL + "/v1/", Model: "test-model", Temperature: 0.25, Timeout: time.Second, MaxOutputTokens: 2048}, model.Credentials{APIKey: key}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	emission, usage, err := adapter.Emit(context.Background(), render.Packet{FrameID: "frame-1"})
	if err != nil {
		t.Fatal(err)
	}
	if emission.EmissionID != "e-1" {
		t.Fatalf("emission = %#v", emission)
	}
	if usage != (model.Usage{PromptTokens: 321, CompletionTokens: 123, TotalTokens: 444}) {
		t.Fatalf("usage = %#v", usage)
	}
	if !strings.Contains(requestBody, `"model":"test-model"`) || !strings.Contains(requestBody, `"temperature":0.25`) || !strings.Contains(requestBody, `"max_tokens":2048`) {
		t.Fatalf("request = %s", requestBody)
	}
	if got := adapter.Provenance().MaxOutputTokens; got != 2048 {
		t.Fatalf("max output tokens provenance = %d", got)
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
	if _, _, err = adapter.Emit(context.Background(), render.Packet{FrameID: "frame-1"}); err != nil {
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
			if _, _, err = adapter.Emit(context.Background(), render.Packet{FrameID: "frame-1"}); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestOpenAIAdapterRescuesProseWrappedEmission(t *testing.T) {
	// Reasoning-style models prepend chain-of-thought prose (here starting
	// with 'W', as in the reported 502) before the actual JSON answer.
	const responseContent = `We need to analyze the RenderPacket and produce the emission.
{"schema":"frp.cognitive-emission.v1","emission_id":"e-prose","frame_id":"frame-1","observation":[],"reasoning":[],"claims":[],"attention":[],"actions":[],"frame_ops":[],"completion":null}`
	payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": responseContent}, "finish_reason": "stop"}}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	adapter, err := model.NewOpenAIAdapter(model.Config{BaseURL: server.URL + "/v1", Model: "m", Timeout: time.Second}, model.Credentials{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	emission, _, err := adapter.Emit(context.Background(), render.Packet{FrameID: "frame-1"})
	if err != nil {
		t.Fatal(err)
	}
	if emission.EmissionID != "e-prose" {
		t.Fatalf("emission = %#v", emission)
	}
}

func TestOpenAIAdapterDecodeErrorIsDiagnostic(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		finishReason string
		reasoning    string
		want         []string
	}{
		{
			name:         "pure prose includes snippet",
			content:      "We cannot answer that question.",
			finishReason: "stop",
			want:         []string{"decode cognitive emission", `We cannot answer that question.`},
		},
		{
			name:         "truncated output hints at token limit",
			content:      "Well, {\"schema\":\"frp.cognitive-emission.v1\"",
			finishReason: "length",
			want:         []string{"finish_reason=length", "TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS"},
		},
		{
			name:         "empty content with reasoning points at reasoning budget",
			content:      "",
			finishReason: "length",
			reasoning:    "We need to analyze the RenderPacket step by step",
			want:         []string{"reasoning tokens likely consumed it", "TEMPORALITY_MODEL_REASONING=off", "model reasoning: \"We need to analyze the RenderPacket step by step\""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			message := map[string]any{"content": tc.content}
			if tc.reasoning != "" {
				message["reasoning"] = tc.reasoning
			}
			payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": tc.finishReason}}})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
			defer server.Close()
			adapter, err := model.NewOpenAIAdapter(model.Config{BaseURL: server.URL + "/v1", Model: "m", Timeout: time.Second}, model.Credentials{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = adapter.Emit(context.Background(), render.Packet{FrameID: "frame-1"})
			if err == nil {
				t.Fatal("invalid response accepted")
			}
			for _, fragment := range tc.want {
				if !strings.Contains(err.Error(), fragment) {
					t.Fatalf("error %q lacks %q", err.Error(), fragment)
				}
			}
		})
	}
}

func TestOpenAIAdapterReportsRefusal(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "", "refusal": "I cannot fulfill this request."}}}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	adapter, err := model.NewOpenAIAdapter(model.Config{BaseURL: server.URL + "/v1", Model: "m", Timeout: time.Second}, model.Credentials{}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = adapter.Emit(context.Background(), render.Packet{FrameID: "frame-1"})
	if err == nil || !strings.Contains(err.Error(), "model refused to answer: I cannot fulfill this request.") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAIAdapterReasoningParameter(t *testing.T) {
	tests := []struct {
		preset         string
		wantFieldCount int
		wantReasoning  map[string]any
	}{
		// unset must keep the wire body unchanged (5 fields, no reasoning key)
		{preset: "", wantFieldCount: 5, wantReasoning: nil},
		{preset: "off", wantFieldCount: 6, wantReasoning: map[string]any{"enabled": false}},
		{preset: "exclude", wantFieldCount: 6, wantReasoning: map[string]any{"exclude": true}},
		{preset: "high", wantFieldCount: 6, wantReasoning: map[string]any{"effort": "high"}},
	}
	for _, tc := range tests {
		t.Run("preset="+tc.preset, func(t *testing.T) {
			var reasoning map[string]any
			var fieldCount int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				fieldCount = len(body)
				reasoning, _ = body["reasoning"].(map[string]any)
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "{\"schema\":\"other\"}"}}}})
			}))
			defer server.Close()
			adapter, err := model.NewOpenAIAdapter(model.Config{BaseURL: server.URL + "/v1", Model: "m", Timeout: time.Second, Reasoning: tc.preset}, model.Credentials{}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if got := adapter.Provenance().Reasoning; got != tc.preset {
				t.Fatalf("provenance reasoning = %q", got)
			}
			_, _, _ = adapter.Emit(context.Background(), render.Packet{FrameID: "frame-1"}) // schema mismatch error is fine
			if fieldCount != tc.wantFieldCount {
				t.Fatalf("wire body field count = %d, want %d", fieldCount, tc.wantFieldCount)
			}
			if fmt.Sprint(reasoning) != fmt.Sprint(tc.wantReasoning) {
				t.Fatalf("wire reasoning = %#v, want %#v", reasoning, tc.wantReasoning)
			}
		})
	}
	if _, err := model.NewOpenAIAdapter(model.Config{BaseURL: "http://localhost:9999/v1", Model: "m", Timeout: time.Second, Reasoning: "banana"}, model.Credentials{}, nil); err == nil || !strings.Contains(err.Error(), "reasoning must be one of") {
		t.Fatalf("invalid preset accepted: %v", err)
	}
}

func TestSecretAbsentFromProvenance(t *testing.T) {
	const secret = "do-not-serialize"
	adapter, err := model.NewOpenAIAdapter(model.Config{BaseURL: "http://localhost:11434/v1", Model: "llama3", Timeout: 3 * time.Second}, model.Credentials{APIKey: secret}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Provenance().MaxOutputTokens != 1024 {
		t.Fatalf("default max output tokens = %d", adapter.Provenance().MaxOutputTokens)
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
	if got, _, err := adapter.Emit(context.Background(), render.Packet{FrameID: "f"}); err != nil || got.EmissionID != "e" {
		t.Fatalf("got %#v, %v", got, err)
	}
	if _, _, err := adapter.Emit(context.Background(), render.Packet{FrameID: "f"}); err == nil {
		t.Fatal("exhausted trace succeeded")
	}
}
