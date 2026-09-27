package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParallelToolCallsDisabledWhenToolsPresent(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		captured = body
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"content": "hi"},
			}},
		})
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", MaxOutputTokens: 64})
	_, err := client.Complete(nil, []Message{{Role: "user", Content: "hi"}}, []ToolDef{{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	json.Unmarshal(captured, &decoded)
	if decoded["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls should be false when tools present, got %v", decoded["parallel_tool_calls"])
	}
}

func TestParallelToolCallsOmittedWhenNoTools(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		captured = body
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"content": "hi"},
			}},
		})
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", MaxOutputTokens: 64})
	_, err := client.Complete(nil, []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	json.Unmarshal(captured, &decoded)
	if _, present := decoded["parallel_tool_calls"]; present {
		t.Fatalf("parallel_tool_calls should be omitted without tools, got %v", decoded["parallel_tool_calls"])
	}
}

func TestMalformedArgumentsSalvage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"content": "done",
					"tool_calls": []any{
						map[string]any{
							"id": "c1", "type": "function",
							"function": map[string]any{"name": "broken", "arguments": "not json{{"},
						},
						map[string]any{
							"id": "c2", "type": "function",
							"function": map[string]any{"name": "good", "arguments": `{"x":1}`},
						},
					},
				},
			}},
		})
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", MaxOutputTokens: 64})
	result, err := client.Complete(nil, []Message{{Role: "user", Content: "go"}}, []ToolDef{{Name: "t"}})
	if err != nil {
		t.Fatalf("malformed call must not fail the completion: %v", err)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected 2 salvaged calls, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].ArgsError == "" {
		t.Fatal("first call should have ArgsError set")
	}
	if result.ToolCalls[0].Name != "broken" {
		t.Fatalf("first call name = %q", result.ToolCalls[0].Name)
	}
	if result.ToolCalls[1].ArgsError != "" {
		t.Fatalf("second call should parse clean, got ArgsError=%q", result.ToolCalls[1].ArgsError)
	}
	if result.ToolCalls[1].Args["x"].(float64) != 1 {
		t.Fatalf("second call args = %v", result.ToolCalls[1].Args)
	}
}
