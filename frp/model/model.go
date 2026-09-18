// Package model connects rendered FRP packets to cognitive model adapters.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/temporality-project/temporality/frp/cognition"
	"github.com/temporality-project/temporality/frp/render"
)

// Usage is the provider-reported token accounting of one Emit call. It is
// zero when the provider did not report usage (e.g. recorded adapters).
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Adapter converts a rendered FRP packet into a validated cognitive emission.
type Adapter interface {
	Emit(context.Context, render.Packet) (cognition.CognitiveEmission, Usage, error)
}

// RejectedError marks a model response that ARRIVED but could not be decoded
// or validated. The raw output is retained so a repair pass can show the
// model exactly what it sent and why it was rejected — a blind retry re-pays
// the whole prompt for the same mistake.
type RejectedError struct {
	Reason string
	Raw    string
}

func (e *RejectedError) Error() string { return e.Reason }

// RepairAdapter is the optional capability behind repair retries: re-emit
// with the rejected attempt quoted back to the model. Adapters without it
// (recorded traces) simply never repair.
type RepairAdapter interface {
	EmitRepair(context.Context, render.Packet, *RejectedError) (cognition.CognitiveEmission, Usage, error)
}

// Add returns the summed cost of several emit attempts.
func (u Usage) Add(other Usage) Usage {
	return Usage{PromptTokens: u.PromptTokens + other.PromptTokens, CompletionTokens: u.CompletionTokens + other.CompletionTokens, TotalTokens: u.TotalTokens + other.TotalTokens}
}

// Config is non-secret model configuration and may safely be retained as provenance.
// BaseURL is the OpenAI-compatible API root, normally ending in /v1.
// Reasoning controls provider reasoning parameters (OpenRouter-style):
// "" sends nothing, "off" disables reasoning, "exclude" hides it from the
// response, and "low"/"medium"/"high" set the reasoning effort.
type Config struct {
	BaseURL         string        `json:"base_url"`
	Model           string        `json:"model"`
	Temperature     float64       `json:"temperature"`
	Timeout         time.Duration `json:"timeout"`
	MaxOutputTokens int           `json:"max_output_tokens"`
	Reasoning       string        `json:"reasoning,omitempty"`
	Trace           *TraceConfig  `json:"-"`
}

// TraceConfig enables bounded payload tracing for one adapter instance.
type TraceConfig struct {
	MaxBytes int
	Hook     func(Trace)
}

// Trace describes one bounded model payload. It never contains HTTP headers.
type Trace struct {
	Direction     string
	Payload       string
	Truncated     bool
	OriginalBytes int
}

// Credentials are deliberately separate from serializable model provenance.
type Credentials struct {
	APIKey string `json:"-"`
}

// Provenance describes the public configuration used for a model call.
type Provenance struct {
	Adapter         string  `json:"adapter"`
	BaseURL         string  `json:"base_url"`
	Model           string  `json:"model"`
	Temperature     float64 `json:"temperature"`
	TimeoutMS       int64   `json:"timeout_ms"`
	MaxOutputTokens int     `json:"max_output_tokens"`
	Reasoning       string  `json:"reasoning,omitempty"`
}

// OpenAIAdapter calls an OpenAI-compatible chat-completions endpoint.
// Its runtime configuration is copied at construction and cannot be mutated by callers.
type OpenAIAdapter struct {
	endpoint        string
	model           string
	temperature     float64
	timeout         time.Duration
	maxOutputTokens int
	reasoning       map[string]any
	apiKey          string
	client          *http.Client
	provenance      Provenance
	trace           TraceConfig
}

func NewOpenAIAdapter(config Config, credentials Credentials, client *http.Client) (*OpenAIAdapter, error) {
	if config.MaxOutputTokens == 0 {
		config.MaxOutputTokens = 1024
	}
	reasoning, err := reasoningRequest(config.Reasoning)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("model base_url must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("model base_url must use HTTP or HTTPS")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("model is required")
	}
	if config.Temperature < 0 || config.Temperature > 2 {
		return nil, errors.New("temperature must be between 0 and 2")
	}
	if config.Timeout <= 0 {
		return nil, errors.New("timeout must be positive")
	}
	if config.MaxOutputTokens < 64 || config.MaxOutputTokens > 65536 {
		return nil, errors.New("max_output_tokens must be between 64 and 65536")
	}
	if config.Trace != nil && (config.Trace.MaxBytes <= 0 || config.Trace.Hook == nil) {
		return nil, errors.New("model trace requires a positive max_bytes and hook")
	}
	if client == nil {
		client = http.DefaultClient
	}
	var trace TraceConfig
	if config.Trace != nil {
		trace = *config.Trace
	}
	return &OpenAIAdapter{
		endpoint:        base + "/chat/completions",
		model:           config.Model,
		temperature:     config.Temperature,
		timeout:         config.Timeout,
		maxOutputTokens: config.MaxOutputTokens,
		reasoning:       reasoning,
		apiKey:          credentials.APIKey,
		client:          client,
		trace:           trace,
		provenance: Provenance{
			Adapter:         "openai-chat-completions",
			BaseURL:         base,
			Model:           config.Model,
			Temperature:     config.Temperature,
			TimeoutMS:       config.Timeout.Milliseconds(),
			MaxOutputTokens: config.MaxOutputTokens,
			Reasoning:       config.Reasoning,
		},
	}, nil
}

// reasoningRequest maps a Config.Reasoning preset to the provider request
// object. OpenRouter accepts {"enabled":bool}, {"effort":string} and
// {"exclude":bool}; other OpenAI-compatible servers ignore the field.
func reasoningRequest(preset string) (map[string]any, error) {
	switch preset {
	case "":
		return nil, nil
	case "off":
		return map[string]any{"enabled": false}, nil
	case "exclude":
		return map[string]any{"exclude": true}, nil
	case "low", "medium", "high":
		return map[string]any{"effort": preset}, nil
	default:
		return nil, fmt.Errorf("reasoning must be one of off, exclude, low, medium, high")
	}
}

func (a *OpenAIAdapter) Provenance() Provenance { return a.provenance }

func (a *OpenAIAdapter) emitTrace(direction string, payload []byte) {
	if a.trace.Hook == nil {
		return
	}
	originalBytes := len(payload)
	truncated := originalBytes > a.trace.MaxBytes
	if truncated {
		payload = payload[:a.trace.MaxBytes]
	}
	a.trace.Hook(Trace{Direction: direction, Payload: string(payload), Truncated: truncated, OriginalBytes: originalBytes})
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    float64        `json:"temperature"`
	MaxTokens      int            `json:"max_tokens"`
	Reasoning      map[string]any `json:"reasoning,omitempty"`
	ResponseFormat responseFormat `json:"response_format"`
}
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type responseFormat struct {
	Type string `json:"type"`
}

const systemPrompt = `You convert one FRP RenderPacket into exactly one CognitiveEmission JSON object.
Return JSON only (no Markdown or commentary), conforming to this required skeleton:
{"schema":"frp.cognitive-emission.v1","emission_id":"<new non-empty id>","frame_id":"<exact frame_id from RenderPacket>","observation":[],"reasoning":[],"claims":[],"attention":[],"actions":[],"frame_ops":[],"claim_ops":[],"completion":null}
All listed fields are required. observation items require interpretation and a canonical STRING ref; prefer durable refs such as "event:UUID" or "claim:UUID", and use "query:text" only when referring to the visible focus query (never return a ref object; omit the observation instead of sending an empty ref). Always include one reasoning item with kind "answer" whose text is a direct, helpful user-facing answer to the Objective in the same language as the Objective; other reasoning items may describe inference or constraints. Do not merely describe what the answer should say. completion is a plain string (or null) with the final answer, only for a genuinely finished Objective; never an object. claims require proposition, confidence (0..1), and status "candidate" (hypothesis) or "supported" — "supported" only for a fact directly extracted from events visible in the packet, and then it must cite them as "evidence":["event:UUID",...] copied exactly; a hypothesis may also cite motivating events in evidence; a claim that replaces a wrong or outdated claim you saw must also carry "supersedes":"claim:UUID" naming the old one. attention supports {"op":"attend","target":{"type":"query","text":"..."}} or a typed target id; frame_ops items look like {"op":"pin","ref":"claim:UUID"} or {"op":"unpin","ref":"event:UUID"} where ref is always a canonical string, never an object. claim_ops reconcile memory: retire a claim that is wrong, outdated, or an exact duplicate with {"op":"refute","claim":"claim:UUID"} — prefer superseding with a corrected claim over bare refutation, refute duplicates you do not need — and promote a hypothesis you have now verified (for example, an execution result in the packet proved it) with {"op":"confirm","claim":"claim:UUID"}; set claim_ops to [] when memory holds nothing to reconcile. The identity section anchors you in the episode: steps is your depth and commitments lists your own live positions (claims you emitted, newest first) — stay consistent with them or reconcile them explicitly via claim_ops; identity_health (when present) reports self-contradictions among your commitments and retracted commitments, so re-anchor on the objective instead of silently drifting. The world_memory section (when present) is knowledge earlier episodes built in this world, delivered by state: items with "state":"confirmed" are cited facts (evidence refs included) you may build on; "state":"refuted" are hypotheses a prior episode already investigated and rejected — do NOT re-investigate them without genuinely new evidence; "state":"investigated" names targets a prior episode already studied; "state":"hypothesis" are unverified guesses from before — treat them as leads to verify, not as facts, and confirm or refute them once you have evidence. Every id you reference (observation refs, attention targets, frame_ops refs, claim_ops claims, supersedes and evidence targets) must be copied EXACTLY from the RenderPacket — never invent, guess, or pattern an id. The affordances section (when present) lists exactly the affordance ids available in this step, each with a signature line like "path*(string) ...; find*(string) ..." where a trailing * marks required parameters and the parentheses hold the JSON type: emit each action as {"affordance":"<one listed id>","args":{...}} with exactly those parameter names — never tool-call style with id/arguments keys. Set actions to [] when the RenderPacket lists no affordances; never invent an affordance or physical tool. Work economically: reuse what the packet already shows instead of re-observing, and when run_command is listed prefer locating code with grep (run_command grep [-rn] PATTERN) over reading many files whole.
Use only information present in the RenderPacket. You have no tools, external access, credentials, secrets, or permission to infer or request them. Never output secrets. Every emission must make progress: either request at least one action, or set completion to a non-null string when the objective is genuinely finished. An emission that neither requests actions nor completes the objective wastes a cognitive step — when you are not done, request your next action(s) in the same emission together with any claims and frame_ops you want to record.`

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			Refusal          string `json:"refusal"`
			Reasoning        string `json:"reasoning"`         // OpenRouter-normalized reasoning field
			ReasoningContent string `json:"reasoning_content"` // DeepSeek-style native field
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (a *OpenAIAdapter) Emit(ctx context.Context, packet render.Packet) (cognition.CognitiveEmission, Usage, error) {
	packetJSON, err := render.MarshalPacket(packet)
	if err != nil {
		return cognition.CognitiveEmission{}, Usage{}, fmt.Errorf("marshal render packet: %w", err)
	}
	return a.emitMessages(ctx, packet, []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: string(packetJSON)},
	})
}

const repairInstruction = "Your previous response (quoted above as your last message) was rejected by the runtime: %s\nReturn the corrected complete JSON object only — fix exactly what was rejected (invalid or invented ids, wrong types, malformed JSON, mismatched frame_id), keep everything else identical to your previous attempt. All refs must be copied exactly from the RenderPacket in the first user message."

// EmitRepair re-asks with the rejected attempt in the conversation: the model
// sees its own output and the precise reason, instead of gambling a fresh
// full-price retry on the same latent mistake. The packet is unchanged, so
// the stable prefix (system + packet) stays provider-cacheable.
func (a *OpenAIAdapter) EmitRepair(ctx context.Context, packet render.Packet, rejection *RejectedError) (cognition.CognitiveEmission, Usage, error) {
	packetJSON, err := render.MarshalPacket(packet)
	if err != nil {
		return cognition.CognitiveEmission{}, Usage{}, fmt.Errorf("marshal render packet: %w", err)
	}
	return a.emitMessages(ctx, packet, []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: string(packetJSON)},
		{Role: "assistant", Content: rejection.Raw},
		{Role: "user", Content: fmt.Sprintf(repairInstruction, rejection.Reason)},
	})
}

func (a *OpenAIAdapter) emitMessages(ctx context.Context, packet render.Packet, messages []chatMessage) (cognition.CognitiveEmission, Usage, error) {
	body, err := json.Marshal(chatRequest{
		Model:          a.model,
		Temperature:    a.temperature,
		MaxTokens:      a.maxOutputTokens,
		Reasoning:      a.reasoning,
		Messages:       messages,
		ResponseFormat: responseFormat{Type: "json_object"},
	})
	if err != nil {
		return cognition.CognitiveEmission{}, Usage{}, fmt.Errorf("marshal model request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return cognition.CognitiveEmission{}, Usage{}, fmt.Errorf("create model request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	a.emitTrace("request", body)
	response, err := a.client.Do(req)
	if err != nil {
		return cognition.CognitiveEmission{}, Usage{}, fmt.Errorf("model request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return cognition.CognitiveEmission{}, Usage{}, fmt.Errorf("model request returned HTTP %d", response.StatusCode)
	}
	var decoded chatResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<20))
	if err := decoder.Decode(&decoded); err != nil {
		return cognition.CognitiveEmission{}, Usage{}, fmt.Errorf("decode model response: %w", err)
	}
	usage := Usage{}
	if decoded.Usage != nil {
		usage = Usage{PromptTokens: decoded.Usage.PromptTokens, CompletionTokens: decoded.Usage.CompletionTokens, TotalTokens: decoded.Usage.TotalTokens}
	}
	if len(decoded.Choices) == 0 {
		return cognition.CognitiveEmission{}, usage, errors.New("model response has no choices")
	}
	choice := decoded.Choices[0]
	if strings.TrimSpace(choice.Message.Content) == "" && choice.Message.Refusal != "" {
		return cognition.CognitiveEmission{}, usage, fmt.Errorf("model refused to answer: %s", boundedSnippet(choice.Message.Refusal))
	}
	a.emitTrace("response", []byte(choice.Message.Content))
	content, err := stripJSONFence(choice.Message.Content)
	if err != nil {
		return cognition.CognitiveEmission{}, usage, &RejectedError{Reason: err.Error(), Raw: choice.Message.Content}
	}
	var emission cognition.CognitiveEmission
	err = json.Unmarshal([]byte(content), &emission)
	if err != nil {
		// Reasoning-style models prepend chain-of-thought prose to the answer
		// (and some backends ignore response_format). Rescue the outermost JSON
		// object when present; validation below stays strict either way.
		if extracted := outermostJSON(content); extracted != "" {
			var rescued cognition.CognitiveEmission
			if rescueErr := json.Unmarshal([]byte(extracted), &rescued); rescueErr == nil {
				emission, err = rescued, nil
			}
		}
	}
	if err != nil {
		message := fmt.Sprintf("decode cognitive emission: %v", err)
		if choice.FinishReason == "length" {
			if strings.TrimSpace(choice.Message.Content) == "" {
				// Empty content with finish_reason=length: reasoning tokens consumed
				// the whole budget before the answer began. Not repairable by
				// re-asking: the budget, not the wording, is the constraint.
				message += fmt.Sprintf("; model produced no content within the token budget (finish_reason=length) — reasoning tokens likely consumed it: raise TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS above %d, set TEMPORALITY_MODEL_REASONING=off for toggleable models, or use a non-reasoning model", a.maxOutputTokens)
				reasoning := choice.Message.Reasoning
				if reasoning == "" {
					reasoning = choice.Message.ReasoningContent
				}
				if reasoning != "" {
					message += fmt.Sprintf("; model reasoning: %q", boundedSnippet(reasoning))
				}
			} else {
				message += fmt.Sprintf("; model output was truncated (finish_reason=length), raise TEMPORALITY_MODEL_MAX_OUTPUT_TOKENS above %d", a.maxOutputTokens)
			}
			return cognition.CognitiveEmission{}, usage, fmt.Errorf("%s; model content: %q", message, boundedSnippet(choice.Message.Content))
		}
		// A delivered-but-malformed answer is worth repairing with feedback,
		// not blindly retrying at full prompt price.
		return cognition.CognitiveEmission{}, usage, &RejectedError{Reason: fmt.Sprintf("%s; model content: %q", message, boundedSnippet(choice.Message.Content)), Raw: choice.Message.Content}
	}
	if err := emission.Validate(); err != nil {
		return cognition.CognitiveEmission{}, usage, &RejectedError{Reason: fmt.Sprintf("validate cognitive emission: %v", err), Raw: choice.Message.Content}
	}
	if emission.FrameID != packet.FrameID {
		return cognition.CognitiveEmission{}, usage, &RejectedError{Reason: fmt.Sprintf("emission frame_id %q does not match packet frame_id %q — copy the frame_id exactly", emission.FrameID, packet.FrameID), Raw: choice.Message.Content}
	}
	return emission, usage, nil
}

func stripJSONFence(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed, nil
	}
	lineEnd := strings.IndexByte(trimmed, '\n')
	if lineEnd < 0 {
		return "", errors.New("malformed fenced model JSON")
	}
	language := strings.TrimSpace(strings.TrimPrefix(trimmed[:lineEnd], "```"))
	if language != "" && !strings.EqualFold(language, "json") {
		return "", fmt.Errorf("unsupported model content fence %q", language)
	}
	rest := trimmed[lineEnd+1:]
	closing := strings.LastIndex(rest, "```")
	if closing < 0 || strings.TrimSpace(rest[closing+3:]) != "" {
		return "", errors.New("malformed fenced model JSON")
	}
	return strings.TrimSpace(rest[:closing]), nil
}

// outermostJSON returns the substring from the first '{' to the last '}' so a
// JSON object wrapped in model commentary can still be decoded.
func outermostJSON(content string) string {
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end <= start {
		return ""
	}
	return content[start : end+1]
}

// boundedSnippet returns a rune-safe diagnostic fragment of model output.
func boundedSnippet(value string) string {
	const limit = 256
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "...(truncated)"
}

// RecordedAdapter replays emissions without network access.
type RecordedAdapter struct {
	Emissions []cognition.CognitiveEmission
	index     int
}

func (a *RecordedAdapter) Emit(_ context.Context, packet render.Packet) (cognition.CognitiveEmission, Usage, error) {
	if a.index >= len(a.Emissions) {
		return cognition.CognitiveEmission{}, Usage{}, errors.New("recorded model trace exhausted")
	}
	emission := a.Emissions[a.index]
	a.index++
	if err := emission.Validate(); err != nil {
		return cognition.CognitiveEmission{}, Usage{}, fmt.Errorf("validate recorded cognitive emission: %w", err)
	}
	if emission.FrameID != packet.FrameID {
		return cognition.CognitiveEmission{}, Usage{}, errors.New("recorded emission frame_id does not match packet frame_id")
	}
	return emission, Usage{}, nil
}
