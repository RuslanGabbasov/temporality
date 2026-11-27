package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The 2026-11-27 incident: an intake run's answer reached the matrix chat as
// the literal text `<tool_call><function=run_status></tool_call>` — the
// provider's serving stack skipped the tool-call parser, the markup rode the
// content field, and the run completed with tags as the user-facing answer.

func TestExtractTextToolCallsIncidentShape(t *testing.T) {
	clean, calls := extractTextToolCalls(`<tool_call><function=run_status></tool_call>`)
	if clean != "" {
		t.Fatalf("clean content = %q, want empty", clean)
	}
	if len(calls) != 1 || calls[0].Name != "run_status" || calls[0].ArgsError != "" {
		t.Fatalf("calls = %+v, want one run_status call without args error", calls)
	}
	if calls[0].ID == "" {
		t.Fatal("extracted call must carry an id")
	}
}

func TestExtractTextToolCallsQwenJSONWithProse(t *testing.T) {
	content := "Проверяю статус.\n<tool_call>\n{\"name\": \"web_search\", \"arguments\": {\"query\": \"penpot\"}}\n</tool_call>\nЧто-то ещё."
	clean, calls := extractTextToolCalls(content)
	if !strings.Contains(clean, "Проверяю статус.") || !strings.Contains(clean, "Что-то ещё.") {
		t.Fatalf("prose lost: %q", clean)
	}
	if strings.Contains(clean, "<tool_call>") || strings.Contains(clean, "arguments") {
		t.Fatalf("markup leaked into clean text: %q", clean)
	}
	if len(calls) != 1 || calls[0].Name != "web_search" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].ArgsRaw != `{"query":"penpot"}` {
		t.Fatalf("args raw = %s", calls[0].ArgsRaw)
	}
	if got := calls[0].Args["query"]; got != "penpot" {
		t.Fatalf("decoded args = %v", calls[0].Args)
	}
}

func TestExtractTextToolCallsLineFormAndBareName(t *testing.T) {
	clean, calls := extractTextToolCalls("<tool_call>run_status\n{\"limit\": 5}</tool_call>")
	if clean != "" || len(calls) != 1 || calls[0].Name != "run_status" {
		t.Fatalf("line form: clean=%q calls=%+v", clean, calls)
	}
	if calls[0].ArgsError != "" || calls[0].Args["limit"] == nil {
		t.Fatalf("line-form args broken: %+v", calls[0])
	}

	clean, calls = extractTextToolCalls("<tool_call>run_status</tool_call>")
	if clean != "" || len(calls) != 1 || calls[0].Name != "run_status" || calls[0].ArgsError != "" {
		t.Fatalf("bare name: clean=%q calls=%+v", clean, calls)
	}
}

func TestExtractTextToolCallsOpenAIWireShape(t *testing.T) {
	_, calls := extractTextToolCalls(`<tool_call>{"function":{"name":"web_search","arguments":"{\"query\":\"x\"}"}}</tool_call>`)
	if len(calls) != 1 || calls[0].Name != "web_search" || calls[0].ArgsError != "" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Args["query"] != "x" {
		t.Fatalf("args = %v", calls[0].Args)
	}
}

func TestExtractTextToolCallsMultipleBlocks(t *testing.T) {
	content := "<tool_call>a_tool</tool_call><tool_call>b_tool</tool_call><tool_call>c_tool</tool_call>"
	clean, calls := extractTextToolCalls(content)
	if clean != "" || len(calls) != 3 || calls[0].Name != "a_tool" || calls[2].Name != "c_tool" {
		t.Fatalf("clean=%q calls=%+v", clean, calls)
	}
	ids := map[string]bool{}
	for _, call := range calls {
		if ids[call.ID] {
			t.Fatalf("duplicate extracted id %s", call.ID)
		}
		ids[call.ID] = true
	}
}

func TestExtractTextToolCallsStandaloneFunctionBlock(t *testing.T) {
	clean, calls := extractTextToolCalls("<function=run_status>{\"limit\":1}</function>")
	if clean != "" || len(calls) != 1 || calls[0].Name != "run_status" || calls[0].Args["limit"] == nil {
		t.Fatalf("clean=%q calls=%+v", clean, calls)
	}
}

func TestExtractTextToolCallsSelfClosingFunction(t *testing.T) {
	clean, calls := extractTextToolCalls("<function=run_status/>")
	if clean != "" || len(calls) != 1 || calls[0].Name != "run_status" || calls[0].ArgsError != "" {
		t.Fatalf("clean=%q calls=%+v", clean, calls)
	}
}

func TestExtractTextToolCallsMalformedArgsCarryError(t *testing.T) {
	clean, calls := extractTextToolCalls("<tool_call>run_status\n{broken</tool_call>")
	if clean != "" || len(calls) != 1 || calls[0].Name != "run_status" {
		t.Fatalf("clean=%q calls=%+v", clean, calls)
	}
	if calls[0].ArgsError == "" || calls[0].ArgsRaw != "{broken" {
		t.Fatalf("malformed args must keep raw with error: %+v", calls[0])
	}
}

func TestExtractTextToolCallsUnparsableKeepsProseDropsTags(t *testing.T) {
	content := "<tool_call>я не понял, что вызывать</tool_call>"
	clean, calls := extractTextToolCalls(content)
	if calls != nil {
		t.Fatalf("calls = %+v, want none", calls)
	}
	if clean != "я не понял, что вызывать" {
		t.Fatalf("clean = %q", clean)
	}
}

func TestExtractTextToolCallsCodeFenceUntouched(t *testing.T) {
	content := "Пример:\n```xml\n<tool_call>{\"name\":\"web_search\"}</tool_call>\n```\nГотово."
	clean, calls := extractTextToolCalls(content)
	if len(calls) != 0 {
		t.Fatalf("calls = %+v, want none inside code fence", calls)
	}
	if !strings.Contains(clean, "<tool_call>") {
		t.Fatalf("fenced markup stripped: %q", clean)
	}
}

func TestExtractTextToolCallsUnclosedTrailingBlock(t *testing.T) {
	clean, calls := extractTextToolCalls("Проверяю: <tool_call><function=run_status>")
	if clean != "Проверяю:" || len(calls) != 1 || calls[0].Name != "run_status" {
		t.Fatalf("clean=%q calls=%+v", clean, calls)
	}
}

func TestExtractTextToolCallsCaseInsensitive(t *testing.T) {
	clean, calls := extractTextToolCalls("<TOOL_CALL><FUNCTION=run_status></TOOL_CALL>")
	if clean != "" || len(calls) != 1 || calls[0].Name != "run_status" {
		t.Fatalf("clean=%q calls=%+v", clean, calls)
	}
}

// Client-level: a blocking provider response whose content carries the
// markup must complete with structured calls and clean content.
func TestCompleteRescuesTextToolCallMarkup(t *testing.T) {
	body := map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"content": "<tool_call><function=run_status></tool_call>"},
			"finish_reason": "stop",
		}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.Contains(completion.Content, "<") {
		t.Fatalf("content still carries markup: %q", completion.Content)
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "run_status" {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
	if completion.Finish != "tool_calls" {
		t.Fatalf("finish = %q, want tool_calls", completion.Finish)
	}
}

// Stream-level: markup accumulated across deltas rescues into calls too.
func TestStreamCompleteRescuesTextToolCallMarkup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"<tool_call>\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"<function=run_status>\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	completion, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "run_status" {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
	if completion.Content != "" {
		t.Fatalf("content = %q, want clean", completion.Content)
	}
	if completion.Finish != "tool_calls" {
		t.Fatalf("finish = %q", completion.Finish)
	}
}

// Guard: a provider that returned structured tool calls must not have its
// content re-scanned — extraction there could execute the same intent twice.
func TestCompleteKeepsTextWhenStructuredCallsPresent(t *testing.T) {
	body := map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"content": "looking into it <tool_call><function=run_status></tool_call>",
				"tool_calls": []any{map[string]any{
					"id":       "call_1",
					"type":     "function",
					"function": map[string]any{"name": "run_status", "arguments": "{}"},
				}},
			},
			"finish_reason": "tool_calls",
		}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
	if !strings.Contains(completion.Content, "looking into it") {
		t.Fatalf("content rewritten despite structured calls: %q", completion.Content)
	}
}

// The rescued call must survive the history replay: an assistant message
// carrying an extracted call marshals back onto the wire as a proper
// tool_call (ArgsRaw is a JSON object string).
func TestExtractedCallReplaysOnTheWire(t *testing.T) {
	_, calls := extractTextToolCalls(`<tool_call>{"name":"run_status","arguments":{"limit":5}}</tool_call>`)
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	messages := []Message{{Role: "assistant", Content: "", ToolCalls: calls}}
	encoded := toRequestMessages(messages)
	if len(encoded) != 1 || len(encoded[0].ToolCalls) != 1 {
		t.Fatalf("wire messages = %+v", encoded)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(encoded[0].ToolCalls[0].Function.Arguments), &probe); err != nil {
		t.Fatalf("replayed arguments are not JSON: %v", err)
	}
	if probe["limit"] == nil {
		t.Fatalf("replayed arguments lost the payload: %v", probe)
	}
}
