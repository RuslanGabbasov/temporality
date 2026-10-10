// Package llm provides a minimal OpenAI-compatible chat client with tool
// calling, used by the classic agent harness in the adaptive-memory vertical
// slice. It intentionally shares the TEMPORALITY_MODEL_* environment contract
// with the rest of the repository so benchmarks run against the same provider
// configuration.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ToolDef describes one callable tool in the OpenAI function-calling format.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON schema object
}

// Message is one chat message. ToolCallID links assistant tool calls to the
// tool-role replies.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a model-requested tool invocation. Args are already decoded
// from the provider's JSON string. ArgsError marks calls whose arguments
// never parsed: the call is preserved as conversation record but must not
// execute.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Args      map[string]any `json:"args"`
	ArgsRaw   string         `json:"args_raw"`
	ArgsError string         `json:"-"`
}

// Completion is one assistant turn.
type Completion struct {
	Content   string
	Reasoning string // model's reasoning/thinking text (reasoning_content)
	ToolCalls []ToolCall
	Usage     Usage
	Finish    string
	// Observability fields (pivot §13): provider identity, wall-clock
	// latency and content-addressed references for the exact request and
	// response payloads. Hashes only — payload contents never enter events.
	Provider    string `json:"provider,omitempty"`
	LatencyMs   int64  `json:"latency_ms,omitempty"`
	RequestRef  string `json:"request_ref,omitempty"`
	ResponseRef string `json:"response_ref,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
}

// Truncated reports whether the provider cut the completion off at the token
// limit (finish_reason=length). Callers must surface this: a truncated answer
// is not a normal completion.
func (c Completion) Truncated() bool { return c.Finish == "length" }

// Usage carries provider token accounting. CachedTokens is the part of
// PromptTokens served from the provider's prompt cache (OpenAI reports it in
// usage.prompt_tokens_details.cached_tokens, Anthropic-compatible gateways in
// usage.cache_read_input_tokens); 0 when the provider does not report it.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
}

// Config is resolved from the environment.
type Config struct {
	BaseURL         string
	Model           string
	APIKey          string
	Temperature     float64
	Timeout         time.Duration
	MaxOutputTokens int
	Reasoning       string
}

// ConfigFromEnv reads the shared TEMPORALITY_MODEL_* variables.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		BaseURL:         strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_BASE_URL")),
		Model:           strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_ID")),
		APIKey:          strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_API_KEY")),
		Temperature:     0,
		Timeout:         180 * time.Second,
		MaxOutputTokens: 16384,
		Reasoning:       strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_REASONING")),
	}
	if cfg.BaseURL == "" || cfg.Model == "" {
		return cfg, errors.New("TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID are required")
	}
	if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_TEMPERATURE")); raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 || value > 2 {
			return cfg, fmt.Errorf("invalid TEMPORALITY_MODEL_TEMPERATURE: %s", raw)
		}
		cfg.Temperature = value
	}
	if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_TIMEOUT")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			return cfg, fmt.Errorf("invalid TEMPORALITY_MODEL_TIMEOUT: %s", raw)
		}
		cfg.Timeout = value
	}
	if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 64 || value > 1000000 {
			return cfg, fmt.Errorf("invalid TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS: %s", raw)
		}
		cfg.MaxOutputTokens = value
	}
	return cfg, nil
}

// Client talks to one OpenAI-compatible endpoint.
type Client struct {
	cfg          Config
	http         *http.Client
	jitter       *rand.Rand
	providerHost string
}

// New builds a client.
func New(cfg Config) *Client {
	host := ""
	if parsed, err := url.Parse(cfg.BaseURL); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}, jitter: rand.New(rand.NewSource(time.Now().UnixNano())), providerHost: host}
}

// Model identifies the configured model in reports.
func (c *Client) Model() string { return c.cfg.Model }

// Provider identifies the configured endpoint host in reports.
func (c *Client) Provider() string { return c.providerHost }

type chatTool struct {
	Type     string  `json:"type"`
	Function ToolDef `json:"function"`
}

type toolCallWire struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type requestMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []toolCallWire `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []requestMessage `json:"messages"`
	Tools    []chatTool       `json:"tools,omitempty"`
	// tool_choice is only meaningful when tools exist.
	ToolChoice string `json:"tool_choice,omitempty"`
	// ParallelToolCalls=false asks the provider for at most one tool call per
	// response: batches of near-identical calls were the main waste source.
	ParallelToolCalls *bool          `json:"parallel_tool_calls,omitempty"`
	Temperature       float64        `json:"temperature"`
	MaxTokens         int            `json:"max_tokens"`
	Reasoning         map[string]any `json:"reasoning,omitempty"`
	Stream            bool           `json:"stream,omitempty"`
	// StreamOptions asks OpenAI-compatible providers to include token usage
	// in the final streaming chunk. Without it many gateways omit usage from
	// SSE responses entirely, and token accounting silently reads zero.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	// ResponseFormat asks OpenAI-compatible providers for structured output
	// (json_object). Only set by explicit option: free-form chat turns must
	// keep plain text.
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	// PromptCacheKey asks the provider to route requests with the same key to
	// one prompt cache entry. It is stable across the turns of a run (task id)
	// so the append-only conversation prefix stays cacheable.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

// Option tunes a single completion call.
type Option func(*chatRequest)

// JSONMode asks the provider to answer with a JSON object only. Providers
// without structured-output support are handled gracefully: a rejection of
// the field falls back to a plain call instead of failing the wizard.
func JSONMode() Option {
	return func(req *chatRequest) { req.ResponseFormat = map[string]any{"type": "json_object"} }
}

// WithPromptCacheKey routes same-key requests to one provider prompt cache
// entry. Unsupported providers ignore unknown body fields.
func WithPromptCacheKey(key string) Option {
	return func(req *chatRequest) { req.PromptCacheKey = key }
}

// streamOptions is the OpenAI streaming options object.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			ToolCalls        []toolCallWire `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// chatStreamChunk is a single SSE chunk in a streaming response.
type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string              `json:"content"`
			ReasoningContent string              `json:"reasoning_content"`
			ToolCalls        []toolCallWireDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *wireUsage `json:"usage"`
}

// wireUsage is the OpenAI-compatible usage object. Cached input tokens live
// in provider-specific fields: prompt_tokens_details.cached_tokens (OpenAI)
// and cache_read_input_tokens (Anthropic-style gateways).
type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptDetails    *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
}

// toUsage folds the wire usage into the client-side accounting, picking the
// cached-token figure from whichever field the provider populated.
func (u *wireUsage) toUsage() Usage {
	if u == nil {
		return Usage{}
	}
	cached := u.CacheReadInputTokens
	if u.PromptDetails != nil && u.PromptDetails.CachedTokens > cached {
		cached = u.PromptDetails.CachedTokens
	}
	return Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     cached,
	}
}

// toolCallWireDelta is a partial tool call in a streaming chunk.
type toolCallWireDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// reasoningRequest maps the reasoning preset onto the provider field the same
// way the FRP adapter does, so both stacks share semantics.
func reasoningRequest(preset string) map[string]any {
	switch preset {
	case "", "off":
		return map[string]any{"enabled": false}
	case "exclude":
		return map[string]any{"exclude": true}
	case "low", "medium", "high":
		return map[string]any{"effort": preset}
	default:
		return map[string]any{"enabled": false}
	}
}

func toRequestMessages(messages []Message) []requestMessage {
	out := make([]requestMessage, 0, len(messages))
	for _, m := range messages {
		rm := requestMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, call := range m.ToolCalls {
			var tc toolCallWire
			tc.ID = call.ID
			tc.Type = "function"
			tc.Function.Name = call.Name
			tc.Function.Arguments = call.ArgsRaw
			rm.ToolCalls = append(rm.ToolCalls, tc)
		}
		out = append(out, rm)
	}
	return out
}

// Complete performs one chat completion. It retries transient provider
// failures (429/5xx/network) with bounded backoff; the returned completion
// carries observability fields (provider, latency, payload references,
// attempt count).
func (c *Client) Complete(ctx context.Context, messages []Message, tools []ToolDef, options ...Option) (Completion, error) {
	req := chatRequest{
		Model:       c.cfg.Model,
		Messages:    toRequestMessages(messages),
		Temperature: c.cfg.Temperature,
		MaxTokens:   c.cfg.MaxOutputTokens,
		Reasoning:   reasoningRequest(c.cfg.Reasoning),
	}
	if len(tools) > 0 {
		// tool_choice is only meaningful when tools exist; sending it on a
		// tools-less forced finale is contradictory wire traffic.
		req.ToolChoice = "auto"
		parallel := false
		req.ParallelToolCalls = &parallel
	}
	for _, tool := range tools {
		req.Tools = append(req.Tools, chatTool{Type: "function", Function: tool})
	}
	for _, option := range options {
		option(&req)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Completion{}, err
	}

	started := time.Now()
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			delay := time.Duration(1<<uint(attempt))*time.Second + time.Duration(c.jitter.Int63n(1500))*time.Millisecond
			select {
			case <-ctx.Done():
				return Completion{}, ctx.Err()
			case <-time.After(delay):
			}
		}
		completion, err := c.once(ctx, payload)
		if err == nil {
			completion.LatencyMs = time.Since(started).Milliseconds()
			completion.Attempts = attempt
			return completion, nil
		}
		lastErr = err
		// Providers without structured-output support reject response_format
		// as a bad request. Drop the field and retry once as plain chat: a
		// missing JSON mode degrades extraction robustness, not availability.
		if req.ResponseFormat != nil {
			var statusErr *statusError
			if errors.As(err, &statusErr) && (statusErr.code == http.StatusBadRequest || statusErr.code == http.StatusUnprocessableEntity) {
				req.ResponseFormat = nil
				payload, err = json.Marshal(req)
				if err != nil {
					return Completion{}, err
				}
				continue
			}
		}
		if ctx.Err() != nil || !transient(err) {
			return Completion{}, err
		}
	}
	return Completion{}, fmt.Errorf("giving up after retries: %w", lastErr)
}

// StreamDelta is one streamed chunk: answer text, reasoning text and/or a
// partial tool call, exactly as the provider emitted it.
type StreamDelta struct {
	Text      string
	Reasoning string
	ToolCalls []ToolCallDelta
}

// TokenCallback is called for each streamed token/chunk.
// Return true to stop streaming early.
type TokenCallback func(delta StreamDelta) bool

// ToolCallDelta is a partial tool call in a streaming chunk.
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	ArgsDelta string
}

// StreamComplete sends a streaming request and calls onToken for each chunk.
// The final Completion is returned with accumulated content and tool calls.
func (c *Client) StreamComplete(ctx context.Context, messages []Message, tools []ToolDef, onToken TokenCallback, options ...Option) (Completion, error) {
	req := chatRequest{
		Model:         c.cfg.Model,
		Messages:      toRequestMessages(messages),
		Temperature:   c.cfg.Temperature,
		MaxTokens:     c.cfg.MaxOutputTokens,
		Reasoning:     reasoningRequest(c.cfg.Reasoning),
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	if len(tools) > 0 {
		req.ToolChoice = "auto"
		parallel := false
		req.ParallelToolCalls = &parallel
	}
	for _, tool := range tools {
		req.Tools = append(req.Tools, chatTool{Type: "function", Function: tool})
	}
	for _, option := range options {
		option(&req)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return Completion{}, err
	}

	started := time.Now()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	if c.cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Completion{}, &transportError{err: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		return Completion{}, &statusError{code: response.StatusCode, body: truncate(string(body), 400)}
	}

	// Parse SSE stream
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	var accumulated strings.Builder
	var reasoning strings.Builder
	toolCallAccum := make(map[int]*ToolCall)
	var finish string
	var usage Usage
	stop := false

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		// Usage may ride either a usage-only chunk (empty choices) or the final
		// finish_reason chunk depending on the gateway — accept both.
		if chunk.Usage != nil {
			usage = chunk.Usage.toUsage()
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finish = choice.FinishReason
		}
		// Accumulate content
		if choice.Delta.Content != "" {
			accumulated.WriteString(choice.Delta.Content)
			if onToken != nil && onToken(StreamDelta{Text: choice.Delta.Content}) {
				stop = true
			}
		}
		// Accumulate reasoning content
		if choice.Delta.ReasoningContent != "" {
			reasoning.WriteString(choice.Delta.ReasoningContent)
			if onToken != nil && onToken(StreamDelta{Reasoning: choice.Delta.ReasoningContent}) {
				stop = true
			}
		}
		// Accumulate tool calls
		for _, tc := range choice.Delta.ToolCalls {
			existing, ok := toolCallAccum[tc.Index]
			if !ok {
				existing = &ToolCall{ID: tc.ID, Name: tc.Function.Name}
				toolCallAccum[tc.Index] = existing
			}
			if tc.ID != "" {
				existing.ID = tc.ID
			}
			if tc.Function.Name != "" {
				existing.Name = tc.Function.Name
			}
			existing.ArgsRaw += tc.Function.Arguments
			if onToken != nil {
				onToken(StreamDelta{ToolCalls: []ToolCallDelta{{Index: tc.Index, ID: tc.ID, Name: tc.Function.Name, ArgsDelta: tc.Function.Arguments}}})
			}
		}
		if stop {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return Completion{}, fmt.Errorf("stream read: %w", err)
	}

	// Build final completion
	completion := Completion{
		Content:    accumulated.String(),
		Reasoning:  reasoning.String(),
		Finish:     finish,
		Provider:   c.providerHost,
		LatencyMs:  time.Since(started).Milliseconds(),
		Usage:      usage,
		RequestRef: contentRef(payload),
	}
	for _, tc := range toolCallAccum {
		args := map[string]any{}
		argsError := ""
		if strings.TrimSpace(tc.ArgsRaw) != "" {
			if err := json.Unmarshal([]byte(tc.ArgsRaw), &args); err != nil {
				args = nil
				argsError = err.Error()
			}
		}
		completion.ToolCalls = append(completion.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Name,
			Args:      args,
			ArgsRaw:   tc.ArgsRaw,
			ArgsError: argsError,
		})
	}
	completion.ResponseRef = contentRef([]byte(completion.Content))
	return completion, nil
}

func transient(err error) bool {
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		return statusErr.code == http.StatusTooManyRequests || statusErr.code >= 500
	}
	// Transport failures (connection refused, EOF mid-body, DNS) are transient
	// provider-side conditions; only request-context cancellation is final.
	var transportErr *transportError
	return errors.As(err, &transportErr)
}

type transportError struct {
	err error
}

func (e *transportError) Error() string { return "provider transport: " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("provider status %d: %s", e.code, e.body)
}

func (c *Client) once(ctx context.Context, payload []byte) (Completion, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Completion{}, &transportError{err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return Completion{}, err
	}
	if response.StatusCode != http.StatusOK {
		return Completion{}, &statusError{code: response.StatusCode, body: truncate(string(body), 400)}
	}
	var decoded chatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Completion{}, fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return Completion{}, &statusError{code: decoded.Error.Code, body: decoded.Error.Message}
	}
	if len(decoded.Choices) == 0 {
		return Completion{}, errors.New("provider returned no choices")
	}
	choice := decoded.Choices[0]
	completion := Completion{Content: choice.Message.Content, Reasoning: choice.Message.ReasoningContent, Finish: choice.FinishReason}
	for _, call := range choice.Message.ToolCalls {
		args := map[string]any{}
		argsError := ""
		if strings.TrimSpace(call.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				// Salvage the call instead of failing the whole completion: one
				// malformed arguments string must not lose the neighboring
				// well-formed calls of the same response.
				args = nil
				argsError = err.Error()
			}
		}
		completion.ToolCalls = append(completion.ToolCalls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Args:      args,
			ArgsRaw:   call.Function.Arguments,
			ArgsError: argsError,
		})
	}
	if decoded.Usage != nil {
		completion.Usage = decoded.Usage.toUsage()
	}
	completion.Provider = c.providerHost
	completion.RequestRef = contentRef(payload)
	completion.ResponseRef = contentRef(body)
	return completion, nil
}

// contentRef derives the sha256 content reference operators can use to
// correlate the exact payload behind an event without storing the payload.
func contentRef(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
