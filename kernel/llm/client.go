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
	"encoding/base64"
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
	"sync/atomic"
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
	Role    string `json:"role"`
	Content string `json:"content"`
	// Images attach vision blocks to a user message (docs/web-research.md §3);
	// on the wire they become image_url content parts next to the text. Tools
	// cannot return images directly — OpenAI-compatible providers only accept
	// them in user messages — so tool results carry blocks that the workflow
	// injects as a follow-up user message.
	Images     []ImageBlock `json:"images,omitempty"`
	ToolCalls  []ToolCall   `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

// ImageBlock is one image payload for a user message: raw bytes plus the
// MIME type. The wire form is a base64 data URI inside an image_url part.
type ImageBlock struct {
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data,omitempty"`
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
	// Stream shape (transport="stream" only): Deltas counts the SSE chunks
	// that carried generated tokens and FirstTokenMs is the wait for the first
	// one. A real stream shows many deltas with an early first token; one
	// late delta means the gateway buffered the whole answer — the call looks
	// streaming-dead even though it succeeded, which is exactly what the
	// operator needs to see to blame the provider, not the kernel.
	Deltas       int   `json:"deltas,omitempty"`
	FirstTokenMs int64 `json:"first_token_ms,omitempty"`
	// Transport records how the completion was delivered: "stream" (SSE) or
	// "blocking" (single JSON response). StreamFailed marks a stream that died
	// mid-flight and was salvaged by the blocking fallback — the wall latency
	// and token usage then already include both generations. StreamMs and
	// StreamError carry how long the dead stream lived and why it died: without
	// them a salvaged completion hides a multi-minute stall behind a healthy-
	// looking latency.
	Transport    string `json:"transport,omitempty"`
	StreamFailed bool   `json:"stream_failed,omitempty"`
	StreamMs     int64  `json:"stream_ms,omitempty"`
	StreamError  string `json:"stream_error,omitempty"`
	// StreamRetried marks a first stream attempt that died of a watchdog and
	// was recovered by the fresh-connection retry: the answer is still a
	// stream, but the wall clock carries a dead attempt the latency number
	// hides. StreamMs holds the wasted first attempt's duration.
	StreamRetried bool `json:"stream_retried,omitempty"`
	// StrippedFields counts the optional envelope fields dropped in-flight
	// because the provider rejected the request as a bad one (400): reasoning,
	// prompt cache key, parallel-tool toggle, stream options. Observability
	// only — the answer itself is complete.
	StrippedFields int `json:"stripped_fields,omitempty"`
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
	BaseURL     string
	Model       string
	APIKey      string
	Temperature float64
	// Timeout is the total per-attempt budget for blocking requests. Streaming
	// requests ignore it: long generations are legal as long as chunks keep
	// arriving, so streams are bounded by StreamIdleTimeout instead plus the
	// caller's context (the Temporal activity deadline in the kernel).
	Timeout time.Duration
	// StreamIdleTimeout is the maximum allowed silence between streamed
	// chunks (and to the first chunk). Only parseable "data:" lines count as
	// traffic: SSE keepalive comments (": ping") hold the connection open but
	// say nothing about generation, so they must not defeat the watchdog — a
	// gateway that parks a queued request behind periodic pings gets canceled
	// here instead of squatting on the activity budget until it drops hours
	// later. A stalled stream is canceled; a slow but steady one may run as
	// long as the caller's context allows (the Temporal activity deadline in
	// the kernel).
	StreamIdleTimeout time.Duration
	// StreamNoProgressTimeout is the maximum allowed silence between chunks
	// that actually carry generated tokens (answer text, reasoning or tool
	// arguments). Some gateways keep data lines flowing with empty payloads
	// while the request sits in a server-side queue; those lines reset the
	// idle timer but not this one. Zero disables the check.
	StreamNoProgressTimeout time.Duration
	// StreamFirstTokenTimeout is the maximum allowed wait for the FIRST chunk
	// that carries generated tokens. It bounds the queueing case end to end:
	// when a provider accepts the request and then sends nothing (or only
	// empty deltas), this fires before the caller's whole retry ladder burns
	// down, and a fresh connection — which often lands on a healthy backend —
	// gets a chance instead. Zero means "unset" and is replaced by the 2m
	// default in New; a negative value disables the check (the env form
	// TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT=0 maps to that disabled state).
	StreamFirstTokenTimeout time.Duration
	MaxOutputTokens         int
	Reasoning               string
}

// ConfigFromEnv reads the shared TEMPORALITY_MODEL_* variables and requires
// the default model endpoint. Callers on the hot path (per model call) want
// this contract; boot-time construction uses OptionalConfigFromEnv so a
// clean install without a default model still comes up.
func ConfigFromEnv() (Config, error) {
	cfg, err := OptionalConfigFromEnv()
	if err != nil {
		return cfg, err
	}
	if cfg.BaseURL == "" || cfg.Model == "" {
		return cfg, errors.New("TEMPORALITY_MODEL_BASE_URL and TEMPORALITY_MODEL_ID are required")
	}
	return cfg, nil
}

// OptionalConfigFromEnv reads the same variables but treats a missing default
// endpoint (TEMPORALITY_MODEL_BASE_URL/TEMPORALITY_MODEL_ID) as legal: models
// routed through workspace providers need no default. Malformed optional
// values still fail.
func OptionalConfigFromEnv() (Config, error) {
	cfg := Config{
		BaseURL:                 strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_BASE_URL")),
		Model:                   strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_ID")),
		APIKey:                  strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_API_KEY")),
		Temperature:             0,
		Timeout:                 180 * time.Second,
		StreamIdleTimeout:       90 * time.Second,
		StreamNoProgressTimeout: 4 * time.Minute,
		StreamFirstTokenTimeout: 2 * time.Minute,
		MaxOutputTokens:         16384,
		Reasoning:               strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_REASONING")),
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
	if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_STREAM_IDLE_TIMEOUT")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			return cfg, fmt.Errorf("invalid TEMPORALITY_MODEL_STREAM_IDLE_TIMEOUT: %s", raw)
		}
		cfg.StreamIdleTimeout = value
	}
	if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_STREAM_NO_PROGRESS_TIMEOUT")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value < 0 {
			return cfg, fmt.Errorf("invalid TEMPORALITY_MODEL_STREAM_NO_PROGRESS_TIMEOUT: %s", raw)
		}
		// 0 is a valid explicit "disable the no-progress watchdog".
		cfg.StreamNoProgressTimeout = value
	}
	if raw := strings.TrimSpace(os.Getenv("TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value < 0 {
			return cfg, fmt.Errorf("invalid TEMPORALITY_MODEL_FIRST_TOKEN_TIMEOUT: %s", raw)
		}
		// 0 is a valid explicit "disable the first-token watchdog"; store it as
		// a negative sentinel so New's zero-means-unset defaulting cannot
		// revive an intentionally disabled check.
		if value == 0 {
			value = -1
		}
		cfg.StreamFirstTokenTimeout = value
	}
	return cfg, nil
}

// Client talks to one OpenAI-compatible endpoint.
type Client struct {
	cfg          Config
	http         *http.Client
	streamHTTP   *http.Client
	jitter       *rand.Rand
	providerHost string
}

// New builds a client.
func New(cfg Config) *Client {
	host := ""
	if parsed, err := url.Parse(cfg.BaseURL); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = 90 * time.Second
	}
	// Zero means "not configured by the caller" rather than "disabled": the
	// watchdog must protect every construction path (env config, provider
	// routing, direct callers) unless it was turned off explicitly via the
	// negative sentinel produced by ConfigFromEnv.
	if cfg.StreamFirstTokenTimeout == 0 {
		cfg.StreamFirstTokenTimeout = 2 * time.Minute
	}
	return &Client{
		cfg:          cfg,
		http:         &http.Client{Timeout: cfg.Timeout},
		streamHTTP:   &http.Client{},
		jitter:       rand.New(rand.NewSource(time.Now().UnixNano())),
		providerHost: host,
	}
}

// anthropicCompatHost reports that the endpoint is Anthropic's OpenAI-
// compatibility layer (api.anthropic.com/v1/chat/completions). The layer is
// deliberately minimal: stream_options and response_format are not part of
// its surface, and its /models catalog authenticates with x-api-key +
// anthropic-version instead of a bearer token. Everything else (messages,
// tools, streaming) speaks the OpenAI wire format as-is.
func anthropicCompatHost(host string) bool {
	return strings.EqualFold(host, "api.anthropic.com")
}

// compatSanitize drops envelope fields the configured provider does not
// speak. Today that is only the Anthropic compatibility layer; OpenAI-
// proper and the gateways tolerate all of these.
// compatSanitize drops envelope fields the configured provider does not
// implement when the host is known-incompatible. Unknown gateways (xiaomi,
// one-api relays, …) surface incompatibilities as 400s instead — the ladder
// below degrades in-flight.
func (c *Client) compatSanitize(req *chatRequest) {
	if !anthropicCompatHost(c.providerHost) {
		return
	}
	req.StreamOptions = nil
	req.ResponseFormat = nil
	req.PromptCacheKey = ""
}

// maxBadRequestStrips bounds the 400-degradation ladder: four optional
// fields, one dropped per round.
const maxBadRequestStrips = 4

// Model identifies the configured model in reports.
func (c *Client) Model() string { return c.cfg.Model }

// badRequestStrip drops the next optional envelope field from a request the
// provider rejected as a bad one (HTTP 400 with a generic body). Priority:
// reasoning effort, prompt cache key, parallel-tool toggle, stream options —
// the fields OpenAI-compatible gateways most often do not implement. Returns
// the dropped field name and true when something was dropped.
func badRequestStrip(req *chatRequest) (string, bool) {
	if req.Reasoning != nil {
		req.Reasoning = nil
		return "reasoning", true
	}
	if req.PromptCacheKey != "" {
		req.PromptCacheKey = ""
		return "prompt_cache_key", true
	}
	if req.ParallelToolCalls != nil {
		req.ParallelToolCalls = nil
		return "parallel_tool_calls", true
	}
	if req.StreamOptions != nil {
		req.StreamOptions = nil
		return "stream_options", true
	}
	return "", false
}

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
	Role    string `json:"role"`
	Content string `json:"content"`
	// Images are folded into content by MarshalJSON (multimodal parts form);
	// never serialized as a separate field.
	Images     []ImageBlock   `json:"-"`
	ToolCalls  []toolCallWire `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// MarshalJSON renders image-bearing messages in the multimodal content form
// OpenAI-compatible providers require — an array of typed parts (text plus
// one image_url data-URI part per image). Messages without images keep the
// plain string form: many gateways only accept that for ordinary turns.
func (rm requestMessage) MarshalJSON() ([]byte, error) {
	if len(rm.Images) == 0 {
		type plain requestMessage
		return json.Marshal(plain(rm))
	}
	blocks := make([]map[string]any, 0, len(rm.Images)+1)
	blocks = append(blocks, map[string]any{"type": "text", "text": rm.Content})
	for _, image := range rm.Images {
		uri := "data:" + image.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)
		blocks = append(blocks, map[string]any{"type": "image_url", "image_url": map[string]any{"url": uri}})
	}
	return json.Marshal(struct {
		Role       string           `json:"role"`
		Content    []map[string]any `json:"content"`
		ToolCalls  []toolCallWire   `json:"tool_calls,omitempty"`
		ToolCallID string           `json:"tool_call_id,omitempty"`
	}{Role: rm.Role, Content: blocks, ToolCalls: rm.ToolCalls, ToolCallID: rm.ToolCallID})
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

// normalizeSystemMessages keeps leading system messages untouched and demotes
// every system message that follows the first non-system message to the user
// role, in place. Strict OpenAI-compatible servers (vLLM with the Qwen chat
// template, among others) reject any system role after the conversation
// started — «System message must be at the beginning» — while the run loop
// legitimately appends late instructions (knowledge hints, final-turn and
// forced-finale reminders). Hoisting them into block 0 instead would mutate
// the one block every call of a run shares and erase prompt-cache prefixes:
// the bare system prompt is the only cross-run shared block, and the history
// prefix must stay byte-identical for the final-turn and finale calls to hit
// the cache. Re-roling late blocks as user turns keeps every byte of every
// prefix where it was.
func normalizeSystemMessages(messages []Message) []Message {
	normalized := make([]Message, len(messages))
	copy(normalized, messages)
	seenNonSystem := false
	for i := range normalized {
		if normalized[i].Role != "system" {
			seenNonSystem = true
			continue
		}
		if seenNonSystem {
			normalized[i].Role = "user"
		}
	}
	return normalized
}

func toRequestMessages(messages []Message) []requestMessage {
	messages = normalizeSystemMessages(messages)
	out := make([]requestMessage, 0, len(messages))
	for _, m := range messages {
		rm := requestMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Images: m.Images}
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
	c.compatSanitize(&req)
	payload, err := json.Marshal(req)
	if err != nil {
		return Completion{}, err
	}

	started := time.Now()
	var lastErr error
	stripped := 0
	for attempt := 1; attempt <= 3 || stripped < maxBadRequestStrips; attempt++ {
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
			completion.Transport = "blocking"
			if stripped > 0 {
				completion.StrippedFields = stripped
			}
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
		// A 400 with a generic body («Invalid request parameters») is usually a
		// provider choking on one of the optional envelope fields it does not
		// implement (reasoning, prompt cache key, parallel-tool toggle, stream
		// options). Strip them one at a time and retry — the same degradation
		// philosophy as the response_format drop above.
		var statusErr *statusError
		if errors.As(err, &statusErr) && statusErr.code == http.StatusBadRequest {
			if stripped < maxBadRequestStrips {
				if _, ok := badRequestStrip(&req); ok {
					stripped++
					payload, err = json.Marshal(req)
					if err != nil {
						return Completion{}, err
					}
					attempt-- // a degradation retry is not a provider retry
					continue
				}
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
		Model:       c.cfg.Model,
		Messages:    toRequestMessages(messages),
		Temperature: c.cfg.Temperature,
		MaxTokens:   c.cfg.MaxOutputTokens,
		Reasoning:   reasoningRequest(c.cfg.Reasoning),
		Stream:      true,
	}
	// The Anthropic compatibility layer keeps its surface minimal: unknown
	// envelope fields (stream_options, response_format, prompt_cache_key) are
	// not part of it — do not send what it did not ask for.
	if !anthropicCompatHost(c.providerHost) {
		req.StreamOptions = &streamOptions{IncludeUsage: true}
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
	c.compatSanitize(&req)
	payload, err := json.Marshal(req)
	if err != nil {
		return Completion{}, err
	}

	started := time.Now()
	// A blanket http.Client.Timeout would cut a long generation mid-flight —
	// for a stream the budget is silence between chunks, not total duration.
	// The stream runs on a dedicated client without a total cap; a watchdog
	// cancels the request when no chunk arrives within StreamIdleTimeout. The
	// caller's context (the Temporal activity deadline) stays the only total
	// limit.
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	request, err := http.NewRequestWithContext(streamCtx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	if c.cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	idle := c.cfg.StreamIdleTimeout
	noProgress := c.cfg.StreamNoProgressTimeout
	firstToken := c.firstTokenBudget(payload)
	var lastTraffic atomic.Int64  // last parseable data line (any payload)
	var lastProgress atomic.Int64 // last chunk that carried generated tokens
	var anyProgress atomic.Bool   // first generated token seen at all
	var cancelReason atomic.Value // string: which watchdog fired, for the error
	start := time.Now().UnixNano()
	lastTraffic.Store(start)
	lastProgress.Store(start)
	stopWatchdog := make(chan struct{})
	defer close(stopWatchdog)
	go func() {
		// Three budgets share one watchdog: idle (no data lines at all — the
		// connection is dead or parked behind keepalive comments), no-progress
		// (data lines flow but carry no generated tokens — the request sits in a
		// server-side queue) and first-token (no generated token ever — the
		// request never left the queue). Tick at the finest granularity.
		tick := idle / 4
		if noProgress > 0 && noProgress/4 < tick {
			tick = noProgress / 4
		}
		if firstToken > 0 && firstToken/4 < tick {
			tick = firstToken / 4
		}
		if tick <= 0 {
			tick = time.Second
		}
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-stopWatchdog:
				return
			case <-ticker.C:
				now := time.Now()
				if now.Sub(time.Unix(0, lastTraffic.Load())) >= idle {
					cancelReason.Store(fmt.Sprintf("idle timeout: no data lines for %s", idle))
					cancelStream()
					return
				}
				if noProgress > 0 && now.Sub(time.Unix(0, lastProgress.Load())) >= noProgress {
					cancelReason.Store(fmt.Sprintf("stalled stream: no generated tokens for %s", noProgress))
					cancelStream()
					return
				}
				if firstToken > 0 && !anyProgress.Load() && now.Sub(time.Unix(0, start)) >= firstToken {
					cancelReason.Store(fmt.Sprintf("first token timeout: no generated tokens for %s", firstToken))
					cancelStream()
					return
				}
			}
		}
	}()
	response, err := c.streamHTTP.Do(request)
	if err != nil {
		// The watchdogs may have canceled the request while Do was still waiting
		// for the response headers — the server-side queue in its purest form
		// (the provider accepted the connection and sends nothing). The cancel
		// must keep its watchdog identity: as a bare transport error it sends
		// callers into the blocking retry ladder, which joins the same queue and
		// adds minutes of silent waiting — exactly what the watchdogs exist to
		// cut short.
		if reason, ok := cancelReason.Load().(string); ok {
			return Completion{}, &watchdogError{reason: reason, cause: err}
		}
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
	deltas := 0
	var firstTokenAt time.Time
	stop := false

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			// Keepalive comments and blank lines are connection liveness, not
			// generation traffic: they must not reset the idle watchdog, or a
			// gateway parking a queued request behind pings defeats it.
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Unparsable data lines are protocol noise, not traffic either.
			continue
		}
		lastTraffic.Store(time.Now().UnixNano())
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
		// Generated tokens (answer text, reasoning or tool arguments) are the
		// only proof the stream is making progress; usage-only and empty-choice
		// chunks keep the connection alive but say nothing about generation.
		if choice.Delta.Content != "" || choice.Delta.ReasoningContent != "" || len(choice.Delta.ToolCalls) > 0 {
			lastProgress.Store(time.Now().UnixNano())
			anyProgress.Store(true)
			deltas++
			if firstTokenAt.IsZero() {
				firstTokenAt = time.Now()
			}
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
		if reason, ok := cancelReason.Load().(string); ok {
			// The watchdog canceled the request context; name the budget that
			// fired so the fallback path can log a diagnosable cause and decide
			// whether a blocking retry can help at all.
			return Completion{}, &watchdogError{reason: reason, cause: err}
		}
		return Completion{}, fmt.Errorf("stream read: %w", err)
	}

	// Build final completion
	completion := Completion{
		Content:      accumulated.String(),
		Reasoning:    reasoning.String(),
		Finish:       finish,
		Provider:     c.providerHost,
		LatencyMs:    time.Since(started).Milliseconds(),
		Usage:        usage,
		RequestRef:   contentRef(payload),
		Transport:    "stream",
		Deltas:       deltas,
		FirstTokenMs: firstTokenAt.Sub(started).Milliseconds(),
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
	// A stream that ends without an answer, tool calls or a finish reason is
	// a provider-side truncation (the connection closed mid-generation —
	// reasoning may have streamed, but there is nothing to act on: no answer,
	// nothing to execute, no "stop" to trust). Returning it as a successful
	// completion completes the run with an empty answer; a transport-flavored
	// error routes it into the caller's retry ladder instead. A consumer-
	// requested stop (stop==true) owns the content it asked to stop at.
	if !stop && completion.Finish == "" && completion.Content == "" && len(completion.ToolCalls) == 0 {
		return Completion{}, &transportError{err: errors.New("stream ended mid-generation: no answer, tool calls or finish reason")}
	}
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

// watchdogError marks a stream canceled by one of the silence budgets (idle,
// no-progress, first-token): the provider accepted the request and then went
// quiet. Callers use StreamTimedOut to distinguish this from protocol
// failures — a blocking retry cannot fix a server-side queue, so the sane
// reaction is a fresh connection, not the same request without streaming.
type watchdogError struct {
	reason string
	cause  error
}

func (e *watchdogError) Error() string {
	return fmt.Sprintf("stream read (%s): %s", e.reason, e.cause)
}

func (e *watchdogError) Unwrap() error { return e.cause }

// StreamTimedOut reports whether the stream failed because the provider went
// silent (any watchdog budget), as opposed to a protocol or status error.
func StreamTimedOut(err error) bool {
	var watchdogErr *watchdogError
	return errors.As(err, &watchdogErr)
}

// firstTokenPrefillBonus scales the first-token watchdog with the prompt
// size that actually costs prefill time. Two corrections over raw byte
// counting: base64 image payloads are discounted to near zero (a screenshot
// contributes megabytes of bytes but a bounded number of vision tokens —
// without this, browser agents with screenshots bought dead providers
// 20-30 minute first-token budgets), and the bonus is capped so no payload
// shape can push the watchdog past ~12 minutes total.
//
// Deliberately modest: a slower-than-this prefill is recovered by the
// fresh-connection retry (which lands on the provider's warm prompt cache
// and is fast), while a generous budget is paid on EVERY stalled call — an
// operator's nudge against a hung provider must stay a near-immediate
// relief, not a fresh 3-minute silent wait.
func firstTokenPrefillBonus(payload []byte) time.Duration {
	bonus := time.Duration(float64(textPayloadBytes(payload)) / 1_000_000 * float64(60*time.Second))
	if bonus > 10*time.Minute {
		bonus = 10 * time.Minute
	}
	return bonus
}

// textPayloadBytes counts the JSON payload bytes excluding base64 data-URI
// bodies ("…;base64,<blob>"): everything up to the closing quote of each
// data URI counts, the blob itself does not.
func textPayloadBytes(payload []byte) int {
	const marker = ";base64,"
	total := 0
	for {
		idx := bytes.Index(payload, []byte(marker))
		if idx < 0 {
			total += len(payload)
			return total
		}
		total += idx + len(marker)
		payload = payload[idx+len(marker):]
		end := bytes.IndexByte(payload, '"')
		if end < 0 {
			// Unterminated data URI: count the rest — better a longer watchdog
			// than an undercount on malformed input.
			return total + len(payload)
		}
		payload = payload[end:]
	}
}

// firstTokenBudget is the watchdog's total patience for the first generated
// token on this payload (0 = disabled by explicit negative config).
func (c *Client) firstTokenBudget(payload []byte) time.Duration {
	if c.cfg.StreamFirstTokenTimeout <= 0 {
		return 0
	}
	return c.cfg.StreamFirstTokenTimeout + firstTokenPrefillBonus(payload)
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
	// An empty completion is a provider failure, not a valid answer: 200 with
	// no content, no tool calls and no reasoning (some providers do this
	// sporadically under json_object mode or long prompts). Treating it as a
	// transient error sends it down the retry ladder with backoff instead of
	// surfacing an empty string to the model-facing caller.
	if strings.TrimSpace(completion.Content) == "" && len(completion.ToolCalls) == 0 && strings.TrimSpace(completion.Reasoning) == "" {
		return Completion{}, &transportError{err: fmt.Errorf("provider returned an empty completion (no content, tool calls or reasoning); body: %s", truncate(string(body), 400))}
	}
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
