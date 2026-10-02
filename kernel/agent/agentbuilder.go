// agentbuilder turns a natural-language description of a specialist into a
// structured agent definition draft. It is the backend of the agent creation
// wizard (docs/evaluable-agent.md §4): the user describes what the agent is
// for, the model extracts the semantic definition — never a free-form prompt.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

// AgentDraft is the wizard result: a prefilled agent definition plus
// suggestions outside the definition (model, sandbox, network) and clarifying
// questions the model could not resolve on its own.
type AgentDraft struct {
	Name         string                      `json:"name"`
	Description  string                      `json:"description"` // purpose
	Capabilities workspace.AgentCapabilities `json:"capabilities"`
	Constraints  []string                    `json:"constraints,omitempty"`
	Completion   []string                    `json:"completion,omitempty"`
	Suggested    AgentDraftSuggestion        `json:"suggested"`
	Questions    []DraftQuestion             `json:"questions,omitempty"`
}

// AgentDraftSuggestion carries settings that live on the agent entity, not in
// the definition; the user confirms or edits them in the form.
type AgentDraftSuggestion struct {
	Model   string `json:"model,omitempty"`
	Sandbox string `json:"sandbox,omitempty"` // restricted | standard | privileged
	Network *bool  `json:"network,omitempty"`
}

// agentDraftCompletion is the strict JSON shape the model must answer with.
type agentDraftCompletion struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Capabilities struct {
		ReadFiles   *bool `json:"read_files"`
		ModifyFiles *bool `json:"modify_files"`
		RunCommands *bool `json:"run_commands"`
		Network     *bool `json:"network"`
		Skills      *bool `json:"skills"`
		Knowledge   *bool `json:"knowledge"`
	} `json:"capabilities"`
	Constraints []string `json:"constraints"`
	Completion  []string `json:"completion"`
	Suggested   struct {
		Model   string `json:"model"`
		Sandbox string `json:"sandbox"`
		Network *bool  `json:"network"`
	} `json:"suggested"`
	Questions []DraftQuestion `json:"questions"`
}

const agentDraftSystemPrompt = `You are the Temporality Agent Builder.
You turn a natural-language description of a specialist into a structured agent definition.

Answer with a single JSON object and nothing else. No markdown fences, no commentary.
Schema:
{
  "name": "Short agent name, e.g. Coder, Reviewer, DevOps",
  "description": "What this agent is responsible for — one or two sentences of purpose, not style",
  "capabilities": { "read_files": true, "modify_files": true, "run_commands": true,
                    "network": false, "skills": true, "knowledge": true },
  "constraints": ["what the agent must never do, derived from the description"],
  "completion": ["verifiable checks the agent must perform before declaring the work done"],
  "suggested": { "model": "", "sandbox": "restricted|standard|privileged", "network": false },
  "questions": [ { "field": "definition path the question refers to", "question": "a concrete clarifying question" } ]
}

Rules:
- Capabilities express restrictions: include a key only when the description implies it; omit keys you are unsure about. An unrestricted agent has an empty capabilities object.
- "network" defaults to false unless the description clearly needs internet access.
- Constraints are hard prohibitions ("Never ..."), not advice.
- Completion criteria must be objectively verifiable (run tests, build passes, output validated); never inner qualities like "be careful".
- Do not prescribe step-by-step procedures; the agent chooses its own trajectory.
- Do not describe tools or skills — the harness supplies tool schemas and injects skills.
- Suggested model may be empty. Suggested sandbox must be one of restricted, standard, privileged; empty when unclear.
- Ask at most 3 short questions, and only when the answer changes the definition.
- Write description, constraints and completion in the same language as the user description.`

// BuildAgentDraft runs the single model call behind the agent wizard.
func BuildAgentDraft(ctx context.Context, model *llm.Client, description, toolsSummary string) (AgentDraft, error) {
	if strings.TrimSpace(description) == "" {
		return AgentDraft{}, errors.New("description is required")
	}
	if model == nil {
		return AgentDraft{}, errors.New("model client is not configured")
	}
	user := "User description of the agent:\n" + strings.TrimSpace(description)
	if strings.TrimSpace(toolsSummary) != "" {
		user += "\n\nTools available to agents in this workspace:\n" + toolsSummary
	}
	completion, err := model.Complete(ctx, []llm.Message{
		{Role: "system", Content: agentDraftSystemPrompt},
		{Role: "user", Content: user},
	}, nil)
	if err != nil {
		return AgentDraft{}, fmt.Errorf("model call failed: %w", err)
	}
	return parseAgentDraft(completion.Content)
}

// parseAgentDraft tolerates fenced code blocks around the JSON payload and
// keeps unknown/empty capability keys unset (unset = allowed).
func parseAgentDraft(content string) (AgentDraft, error) {
	payload := strings.TrimSpace(content)
	if start := strings.Index(payload, "```"); start >= 0 {
		if firstLine := strings.IndexByte(payload[start:], '\n'); firstLine > 0 {
			rest := payload[start+firstLine+1:]
			if end := strings.LastIndex(rest, "```"); end >= 0 {
				payload = rest[:end]
			}
		}
	}
	var parsed agentDraftCompletion
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return AgentDraft{}, fmt.Errorf("model returned invalid JSON: %w", err)
	}

	trimAll := func(values []string) []string {
		var result []string
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				result = append(result, value)
			}
		}
		return result
	}
	draft := AgentDraft{
		Name:        strings.TrimSpace(parsed.Name),
		Description: strings.TrimSpace(parsed.Description),
		Capabilities: workspace.AgentCapabilities{
			ReadFiles:   parsed.Capabilities.ReadFiles,
			ModifyFiles: parsed.Capabilities.ModifyFiles,
			RunCommands: parsed.Capabilities.RunCommands,
			Network:     parsed.Capabilities.Network,
			Skills:      parsed.Capabilities.Skills,
			Knowledge:   parsed.Capabilities.Knowledge,
		},
		Constraints: trimAll(parsed.Constraints),
		Completion:  trimAll(parsed.Completion),
	}
	switch parsed.Suggested.Sandbox {
	case "restricted", "standard", "privileged":
		draft.Suggested.Sandbox = parsed.Suggested.Sandbox
	}
	draft.Suggested.Model = strings.TrimSpace(parsed.Suggested.Model)
	draft.Suggested.Network = parsed.Suggested.Network
	if draft.Name == "" && draft.Description != "" {
		// Fallback: derive a display name from the purpose's first words.
		firstLine := draft.Description
		if idx := strings.IndexAny(firstLine, ".\n"); idx > 0 {
			firstLine = firstLine[:idx]
		}
		words := strings.Fields(firstLine)
		if len(words) > 3 {
			words = words[:3]
		}
		name := strings.Join(words, " ")
		if name != "" {
			name = strings.ToUpper(name[:1]) + name[1:]
		}
		draft.Name = name
	}
	for _, q := range parsed.Questions {
		if question := strings.TrimSpace(q.Question); question != "" {
			draft.Questions = append(draft.Questions, DraftQuestion{Field: strings.TrimSpace(q.Field), Question: question})
		}
	}
	return draft, nil
}
