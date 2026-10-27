package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const okResponse = `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

func TestNewDerivesProviderHost(t *testing.T) {
	client := New(Config{BaseURL: "https://api.example.com/v1", Model: "m"})
	if got := client.Provider(); got != "api.example.com" {
		t.Fatalf("provider host = %q, want api.example.com", got)
	}
}

func TestNewKeepsProviderEmptyForBadURL(t *testing.T) {
	client := New(Config{BaseURL: "://not-a-url", Model: "m"})
	if got := client.Provider(); got != "" {
		t.Fatalf("provider host = %q, want empty", got)
	}
}

func TestTruncatedDetection(t *testing.T) {
	if !(Completion{Finish: "length"}).Truncated() {
		t.Error("finish_reason=length must report truncated")
	}
	if (Completion{Finish: "stop"}).Truncated() {
		t.Error("finish_reason=stop must not report truncated")
	}
}

func TestTransientClassification(t *testing.T) {
	if !transient(&transportError{err: errors.New("connection reset by peer")}) {
		t.Error("transport errors must be transient")
	}
	if transient(&statusError{code: http.StatusBadRequest, body: "bad request"}) {
		t.Error("400 must not be transient")
	}
	if transient(&statusError{code: http.StatusUnauthorized, body: "bad key"}) {
		t.Error("401 must not be transient")
	}
	if !transient(&statusError{code: http.StatusTooManyRequests, body: "slow down"}) {
		t.Error("429 must be transient")
	}
	if !transient(&statusError{code: http.StatusBadGateway, body: "upstream boom"}) {
		t.Error("5xx must be transient")
	}
	if transient(errors.New("decode response: unexpected EOF")) {
		t.Error("decoding errors must not be transient")
	}
}

func TestCompleteToolChoiceOnlyWithTools(t *testing.T) {
	var lastRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		lastRequest = body
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})

	completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Complete without tools: %v", err)
	}
	if _, present := lastRequest["tool_choice"]; present {
		t.Errorf("tool_choice sent on a tools-less call: %v", lastRequest["tool_choice"])
	}

	tools := []ToolDef{{Name: "echo", Description: "echo", Parameters: map[string]any{"type": "object"}}}
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, tools); err != nil {
		t.Fatalf("Complete with tools: %v", err)
	}
	if got := lastRequest["tool_choice"]; got != "auto" {
		t.Errorf("tool_choice with tools = %v, want auto", got)
	}

	// Observability fields must be populated with hashes, never payloads.
	if completion.Provider != server.Listener.Addr().String() {
		t.Errorf("provider = %q, want %q", completion.Provider, server.Listener.Addr().String())
	}
	if len(completion.RequestRef) != len("sha256:")+64 || completion.RequestRef[:7] != "sha256:" {
		t.Errorf("request_ref = %q, want sha256:<64 hex>", completion.RequestRef)
	}
	if len(completion.ResponseRef) != len("sha256:")+64 || completion.ResponseRef[:7] != "sha256:" {
		t.Errorf("response_ref = %q, want sha256:<64 hex>", completion.ResponseRef)
	}
	if completion.RequestRef == completion.ResponseRef {
		t.Error("request and response payloads hashed to the same ref")
	}
	if completion.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", completion.Attempts)
	}
	if completion.Usage.TotalTokens != 5 || completion.Usage.PromptTokens != 3 || completion.Usage.CompletionTokens != 2 {
		t.Errorf("usage = %+v, want 3/2/5", completion.Usage)
	}
}

func TestCompleteParsesCachedTokens(t *testing.T) {
	responses := []string{
		// OpenAI shape: usage.prompt_tokens_details.cached_tokens
		`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":80}}}`,
		// Anthropic-style gateway shape: usage.cache_read_input_tokens
		`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"cache_read_input_tokens":60}}`,
	}
	for _, body := range responses {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}))
		client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
		completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if completion.Usage.PromptTokens != 100 || completion.Usage.CachedTokens == 0 {
			t.Errorf("usage = %+v, want prompt 100 with cached > 0", completion.Usage)
		}
		server.Close()
	}
}

func TestPromptCacheKeySentWhenOptionGiven(t *testing.T) {
	var sawKey any
	hasKey := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		sawKey, hasKey = body["prompt_cache_key"]
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	if hasKey {
		t.Errorf("prompt_cache_key sent without option: %v", sawKey)
	}
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, WithPromptCacheKey("task-42")); err != nil {
		t.Fatal(err)
	}
	if sawKey != "task-42" {
		t.Errorf("prompt_cache_key = %v, want task-42", sawKey)
	}
}

func TestCompleteRetriesTransientStatus(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"message":"upstream overloaded"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if calls != 2 {
		t.Errorf("provider calls = %d, want 2", calls)
	}
	if completion.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", completion.Attempts)
	}
	if completion.LatencyMs <= 0 {
		t.Errorf("latency_ms = %d, want > 0 (covers the retry backoff)", completion.LatencyMs)
	}
}

func TestParseRetryAfterHeader(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfterHeader("5", now); got != 5*time.Second {
		t.Fatalf("delay-seconds = %s, want 5s", got)
	}
	if got := parseRetryAfterHeader(" 3 ", now); got != 3*time.Second {
		t.Fatalf("padded delay-seconds = %s, want 3s", got)
	}
	for _, value := range []string{"", "0", "-2", "soon", "2.5"} {
		if got := parseRetryAfterHeader(value, now); got != 0 {
			t.Fatalf("header %q = %s, want 0 (keep the own backoff)", value, got)
		}
	}
	// HTTP-date form: in the future → the delta, in the past → 0.
	if got := parseRetryAfterHeader(now.Add(30*time.Second).UTC().Format(http.TimeFormat), now); got != 30*time.Second {
		t.Fatalf("future http-date = %s, want 30s", got)
	}
	if got := parseRetryAfterHeader(now.Add(-time.Hour).UTC().Format(http.TimeFormat), now); got != 0 {
		t.Fatalf("past http-date = %s, want 0", got)
	}
}

// The effective wait is max(backoff, Retry-After), capped: the provider's
// hint wins when it asks for more patience than the own ladder has, but no
// header may park the call for minutes.
func TestRetryDelayHonorsRetryAfterCap(t *testing.T) {
	if got := retryDelayFor(4*time.Second, 0); got != 4*time.Second {
		t.Fatalf("no hint = %s, want the plain backoff", got)
	}
	if got := retryDelayFor(4*time.Second, 2*time.Second); got != 4*time.Second {
		t.Fatalf("small hint = %s, want the longer backoff", got)
	}
	if got := retryDelayFor(4*time.Second, 5*time.Second); got != 5*time.Second {
		t.Fatalf("bigger hint = %s, want 5s", got)
	}
	if got := retryDelayFor(time.Second, 10*time.Minute); got != maxRetryAfterWait {
		t.Fatalf("huge hint = %s, want the %s cap", got, maxRetryAfterWait)
	}
}

// A 429 that names Retry-After: 5 must hold the retry at least those five
// seconds even though the own attempt-2 backoff (4s + jitter) is shorter.
func TestCompleteWaitsForRetryAfter429(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	started := time.Now()
	completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if calls != 2 {
		t.Errorf("provider calls = %d, want 2", calls)
	}
	if elapsed := time.Since(started); elapsed < 5*time.Second {
		t.Errorf("elapsed = %s, want >= the 5s Retry-After", elapsed)
	}
	if completion.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", completion.Attempts)
	}
}

func TestCompleteFailsFastOnAuthError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err == nil {
		t.Fatal("expected auth error")
	}
	if calls != 1 {
		t.Errorf("provider calls = %d, want 1 (no retries on 401)", calls)
	}
}

func TestJSONModeSendsResponseFormat(t *testing.T) {
	var lastRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		lastRequest = body
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("plain Complete: %v", err)
	}
	if _, present := lastRequest["response_format"]; present {
		t.Errorf("response_format sent without JSONMode: %v", lastRequest["response_format"])
	}

	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, JSONMode()); err != nil {
		t.Fatalf("Complete with JSONMode: %v", err)
	}
	format, _ := lastRequest["response_format"].(map[string]any)
	if format["type"] != "json_object" {
		t.Errorf("response_format = %v, want json_object", lastRequest["response_format"])
	}
}

func TestJSONModeFallsBackOnRejection(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests = append(requests, body)
		if _, rejected := body["response_format"]; rejected {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"response_format is not supported"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, JSONMode())
	if err != nil {
		t.Fatalf("Complete after rejection: %v", err)
	}
	if completion.Content != "ok" {
		t.Errorf("content = %q, want ok", completion.Content)
	}
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d, want 2 (rejected with format, retried plain)", len(requests))
	}
	if _, still := requests[1]["response_format"]; still {
		t.Errorf("retry still carries response_format: %v", requests[1]["response_format"])
	}
}

func TestStreamCompleteRequestsAndParsesUsage(t *testing.T) {
	var includeUsage bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if opts, ok := body["stream_options"].(map[string]any); ok {
			includeUsage, _ = opts["include_usage"].(bool)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":5,\"total_tokens\":45}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	completion, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}
	if !includeUsage {
		t.Error("stream_options.include_usage was not requested")
	}
	if completion.Content != "hello" {
		t.Errorf("content = %q, want hello", completion.Content)
	}
	// Usage on the finish_reason chunk (choices non-empty) must be honored.
	if completion.Usage.TotalTokens != 45 {
		t.Errorf("usage = %+v, want total 45", completion.Usage)
	}
}

func TestStreamCompleteParsesUsageOnlyChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	completion, err := client.StreamComplete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("StreamComplete: %v", err)
	}
	if completion.Usage.TotalTokens != 10 || completion.Usage.CachedTokens != 4 {
		t.Errorf("usage = %+v, want total 10 cached 4", completion.Usage)
	}
}

func TestImageMessagesMarshalAsMultimodalParts(t *testing.T) {
	var lastRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		lastRequest = body
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second})
	pngHeader := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	messages := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "look", Images: []ImageBlock{{MIMEType: "image/png", Data: pngHeader}}},
	}
	if _, err := client.Complete(context.Background(), messages, nil); err != nil {
		t.Fatalf("Complete with images: %v", err)
	}
	raw, _ := lastRequest["messages"].([]any)
	if len(raw) != 2 {
		t.Fatalf("messages = %d, want 2", len(raw))
	}
	// Plain messages must stay plain strings.
	if got := raw[0].(map[string]any)["content"]; got != "sys" {
		t.Errorf("system content = %T(%v), want plain string", got, got)
	}
	// Image-bearing messages render as typed content parts.
	parts, ok := raw[1].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatalf("user content is not a parts array: %T", raw[1].(map[string]any)["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want text+image", len(parts))
	}
	if typ := parts[0].(map[string]any)["type"]; typ != "text" {
		t.Errorf("first part type = %v, want text", typ)
	}
	imagePart, ok := parts[1].(map[string]any)["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("second part is not image_url: %v", parts[1])
	}
	uri, _ := imagePart["url"].(string)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngHeader)
	if uri != want {
		t.Errorf("image url = %q, want data URI", uri[:min(30, len(uri))])
	}
}

func TestLateSystemMessagesDemoteToUserInPlace(t *testing.T) {
	// Strict OpenAI-compatible servers (vLLM + Qwen chat template) reject any
	// system role after the conversation started. Late blocks must therefore
	// travel as user turns IN PLACE: re-roling preserves every prompt-cache
	// prefix (the bare block-0 system prompt shared across runs, the stable
	// history prefix the final-turn and finale calls reuse), which hoisting
	// into block 0 would erase.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for i, m := range request.Messages {
			if m.Role == "system" && i != 0 {
				t.Errorf("system message at wire position %d, want only position 0", i)
			}
		}
		if got := request.Messages[0]; got.Role != "system" || got.Content != "base prompt" {
			t.Errorf("block 0 = %s/%q, want the untouched shared system prompt", got.Role, got.Content)
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role != "user" || last.Content != "late hint" {
			t.Errorf("last message = %s/%q, want user/late hint in place", last.Role, last.Content)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, Model: "m", Timeout: 5 * time.Second})
	messages := []Message{
		{Role: "system", Content: "base prompt"},
		{Role: "user", Content: "do things"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "c1", Name: "t", ArgsRaw: "{}"}}},
		{Role: "tool", ToolCallID: "c1", Content: "done"},
		{Role: "system", Content: "late hint"},
	}
	if _, err := client.Complete(context.Background(), messages, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// The caller's slice must keep the original roles: the workflow reuses it
	// as the history base for the next turn.
	if messages[len(messages)-1].Role != "system" {
		t.Fatalf("caller's messages mutated: last role = %s", messages[len(messages)-1].Role)
	}
}

func TestSystemlessConversationUntouchedByNormalization(t *testing.T) {
	messages := []Message{{Role: "user", Content: "hi"}}
	got := normalizeSystemMessages(messages)
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("systemless input changed: %+v", got)
	}
}

// A strict gateway (xiaomi-style) rejects optional envelope fields with a
// generic 400. The completion ladder strips them one at a time and retries —
// the answer survives, and the completion records how many fields were
// dropped so the trace shows the degradation. With tools in play the
// strippable set is reasoning, prompt_cache_key and parallel_tool_calls:
// stream_options never rides a blocking request at all.
func TestCompleteStripsOptionalFieldsOnBadRequest(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if len(bodies) < 4 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "400", "message": "Invalid request parameters", "type": "BadRequestError"}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(okResponse))
	}))
	defer server.Close()

	client := New(Config{
		BaseURL: server.URL, Model: "m", Timeout: 10 * time.Second,
		Reasoning: "high",
	})
	tools := []ToolDef{{Name: "echo", Description: "echo", Parameters: map[string]any{"type": "object"}}}
	completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, tools, WithPromptCacheKey("cache-1"))
	if err != nil {
		t.Fatalf("Complete with the strip ladder: %v", err)
	}
	if completion.StrippedFields != 3 {
		t.Fatalf("stripped_fields = %d, want 3 (reasoning, prompt_cache_key, parallel_tool_calls)", completion.StrippedFields)
	}
	if len(bodies) != 4 {
		t.Fatalf("provider calls = %d, want 4 (three strips, then success)", len(bodies))
	}
	// Requests BEFORE a strip legitimately carry the field; every request
	// AFTER the field was dropped must be clean: reasoning goes after request
	// 1, the cache key after request 2, the parallel toggle after request 3.
	for i, body := range bodies {
		if i >= 1 {
			if _, present := body["reasoning"]; present {
				t.Fatalf("reasoning present in request #%d after being stripped", i)
			}
		}
		if i >= 2 {
			if _, present := body["prompt_cache_key"]; present {
				t.Fatalf("prompt_cache_key present in request #%d after being stripped", i)
			}
		}
		if i >= 3 {
			if _, present := body["parallel_tool_calls"]; present {
				t.Fatalf("parallel_tool_calls present in request #%d after being stripped", i)
			}
		}
	}
	if _, present := bodies[0]["stream_options"]; present {
		t.Fatal("stream_options must not ride a blocking request at all")
	}
}
