package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The subscription preset ships base URLs with the /v1 segment, so every test
// mounts its fake behind server.URL + "/v1" and asserts the suffix.

func newMessagesClient(url string) *Client {
	return New(Config{
		BaseURL:        url + "/v1",
		Model:          "claude-sonnet-4-5",
		APIKey:         "sk-ant-oat01-example000000",
		AnthropicOAuth: true,
		Timeout:        10 * time.Second,
	})
}

// TestMessagesWireCompleteToolCall pins the blocking round trip: URL, OAuth
// headers, the translated envelope (system hoisted, tool_choice with the
// parallel toggle, tool_result replay) and the block-array answer mapping.
func TestMessagesWireCompleteToolCall(t *testing.T) {
	var gotBody map[string]any
	var gotAuth, gotVersion, gotBeta, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("Anthropic-Version")
		gotBeta = r.Header.Get("Anthropic-Beta")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "msg_1", "type": "message", "role": "assistant",
			"content": [
				{"type": "text", "text": "Checking."},
				{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "SPb"}}
			],
			"stop_reason": "tool_use",
			"usage": {"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 42}
		}`))
	}))
	defer server.Close()

	completion, err := newMessagesClient(server.URL).Complete(context.Background(),
		[]Message{
			{Role: "system", Content: "You are terse."},
			{Role: "user", Content: "Weather?"},
			{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "toolu_1", Name: "get_weather", ArgsRaw: `{"city":"SPb"}`}}},
			{Role: "tool", ToolCallID: "toolu_1", Content: "rain, 7°C"},
			{Role: "tool", ToolCallID: "toolu_1", Content: "wind, 5 m/s"},
		},
		[]ToolDef{{Name: "get_weather", Description: "current weather", Parameters: map[string]any{"type": "object"}}},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if !strings.HasSuffix(gotPath, "/v1/messages") {
		t.Fatalf("path = %s, want /v1/messages", gotPath)
	}
	if gotAuth != "Bearer sk-ant-oat01-example000000" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotVersion != anthropicVersionHeader {
		t.Fatalf("Anthropic-Version = %q", gotVersion)
	}
	if gotBeta != anthropicOAuthBetaValue {
		t.Fatalf("Anthropic-Beta = %q, want the oauth beta", gotBeta)
	}

	if gotBody["system"] != "You are terse." {
		t.Fatalf("system = %v, want the hoisted system prompt", gotBody["system"])
	}
	if gotBody["max_tokens"].(float64) != 16384 {
		t.Fatalf("max_tokens = %v, want the kernel default backstop", gotBody["max_tokens"])
	}
	choice := gotBody["tool_choice"].(map[string]any)
	if choice["type"] != "auto" || choice["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice = %v, want auto with the parallel toggle", choice)
	}
	tools := gotBody["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["name"] != "get_weather" || tool["input_schema"] == nil {
		t.Fatalf("tools[0] = %v, want input_schema form", tool)
	}

	messages := gotBody["messages"].([]any)
	if len(messages) != 3 {
		// user text | assistant tool_use | user (two tool_results merged)
		t.Fatalf("messages = %d turns, want 3: %s", len(messages), mustJSON(gotBody))
	}
	replay := messages[2].(map[string]any)
	if replay["role"] != "user" {
		t.Fatalf("tool replay role = %v, want user", replay["role"])
	}
	blocks := replay["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("tool replay = %d blocks, want the two merged tool_results", len(blocks))
	}
	first := blocks[0].(map[string]any)
	if first["type"] != "tool_result" || first["tool_use_id"] != "toolu_1" {
		t.Fatalf("first replay block = %v, want tool_result for toolu_1", first)
	}

	if completion.Content != "Checking." {
		t.Fatalf("Content = %q", completion.Content)
	}
	if completion.Finish != "tool_calls" {
		t.Fatalf("Finish = %q, want the tool_use mapping", completion.Finish)
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].ID != "toolu_1" || completion.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("ToolCalls = %+v", completion.ToolCalls)
	}
	if completion.ToolCalls[0].Args["city"] != "SPb" {
		t.Fatalf("Args = %v", completion.ToolCalls[0].Args)
	}
	usage := completion.Usage
	if usage.PromptTokens != 100 || usage.CompletionTokens != 20 || usage.TotalTokens != 120 || usage.CachedTokens != 42 {
		t.Fatalf("Usage = %+v", usage)
	}
	if completion.Provider == "" || completion.RequestRef == "" || completion.Transport != "blocking" {
		t.Fatalf("observability fields missing: %+v", completion)
	}
}

// The streaming twin: the Anthropic SSE grammar (event/data pairs) must
// assemble into the same Completion shape the chat wire produces, with text
// and tool-argument deltas reaching the callback in order.
func TestMessagesWireStreamComplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"usage":{"input_tokens":25,"cache_read_input_tokens":11}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_9","name":"read_file"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"a.txt\"}"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	var deltas []StreamDelta
	completion, err := newMessagesClient(server.URL).StreamComplete(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, nil,
		func(delta StreamDelta) bool {
			deltas = append(deltas, delta)
			return false
		})
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}

	if completion.Content != "Hello" {
		t.Fatalf("Content = %q, want the streamed text", completion.Content)
	}
	if completion.Finish != "tool_calls" {
		t.Fatalf("Finish = %q", completion.Finish)
	}
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v", completion.ToolCalls)
	}
	call := completion.ToolCalls[0]
	if call.ID != "toolu_9" || call.Name != "read_file" || call.Args["path"] != "a.txt" {
		t.Fatalf("assembled call = %+v", call)
	}
	if completion.Usage.PromptTokens != 25 || completion.Usage.CachedTokens != 11 || completion.Usage.CompletionTokens != 7 || completion.Usage.TotalTokens != 32 {
		t.Fatalf("Usage = %+v", completion.Usage)
	}
	if completion.Transport != "stream" || completion.Deltas != 4 {
		t.Fatalf("stream shape = transport %q deltas %d", completion.Transport, completion.Deltas)
	}

	// Delta order: two text tokens, then two tool-argument deltas with the
	// block identity attached.
	if len(deltas) != 4 || deltas[0].Text != "Hel" || deltas[1].Text != "Hello"[3:] {
		t.Fatalf("text deltas = %+v", deltas)
	}
	for _, delta := range deltas[2:] {
		if len(delta.ToolCalls) != 1 || delta.ToolCalls[0].ID != "toolu_9" || delta.ToolCalls[0].Name != "read_file" {
			t.Fatalf("tool delta = %+v", delta)
		}
	}
	if deltas[2].ToolCalls[0].ArgsDelta != `{"path":` || deltas[3].ToolCalls[0].ArgsDelta != `"a.txt"}` {
		t.Fatalf("arg deltas = %+v", deltas[2:])
	}
}

// A mid-stream error event must fail the call (transient-flavored), never
// complete the run with a silent truncation.
func TestMessagesWireStreamErrorEventFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"))
	}))
	defer server.Close()

	_, err := newMessagesClient(server.URL).StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	var transportErr *transportError
	if !errors.As(err, &transportErr) || !strings.Contains(err.Error(), "overloaded_error") {
		t.Fatalf("err = %v, want a transport error naming the event", err)
	}
}

// A plain Anthropic API-key provider (no OAuth) keeps the chat-completions
// compat path: the messages translation must not leak into it.
func TestAnthropicCompatStaysOnChatWire(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL + "/v1", Model: "claude-sonnet-4-5", APIKey: "sk-ant-api-key", Timeout: 10 * time.Second})
	if client.messagesWire() {
		t.Fatal("a plain API-key client must stay on the chat wire")
	}
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/chat/completions") {
		t.Fatalf("path = %s, want the compat chat endpoint", gotPath)
	}
}

// A bare api.anthropic.com base (no /v1) is forgiven on the Messages wire —
// the spelling every Anthropic example uses.
func TestMessagesWireURLForgivenV1(t *testing.T) {
	client := New(Config{BaseURL: "https://api.anthropic.com", Model: "m", AnthropicOAuth: true})
	if got, want := client.completionsURL(), "https://api.anthropic.com/v1/messages"; got != want {
		t.Fatalf("url = %s, want %s", got, want)
	}
	client = New(Config{BaseURL: "https://api.anthropic.com/v1", Model: "m", AnthropicOAuth: true})
	if got, want := client.completionsURL(), "https://api.anthropic.com/v1/messages"; got != want {
		t.Fatalf("url = %s, want %s", got, want)
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
