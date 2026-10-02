// skillbuilder turns a natural-language description of a capability into a
// structured skill draft. It is the backend of the skill creation wizard:
// the user describes what the skill should do, the model extracts the formal
// contract (docs/living-skills.md), matching tools against what is actually
// available in the workspace (builtins + MCP servers).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
)

// SkillDraft is the wizard result: a prefilled skill plus the provenance of
// every populated field and clarifying questions the model could not resolve
// on its own.
type SkillDraft struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Markdown    string          `json:"markdown"`
	Manifest    json.RawMessage `json:"manifest"`
	Questions   []DraftQuestion `json:"questions,omitempty"`
}

// DraftQuestion is one clarification the model wants from the user before the
// draft can be considered complete.
type DraftQuestion struct {
	Field    string `json:"field"`
	Question string `json:"question"`
}

// draftCompletion is the strict JSON shape the model must answer with.
type draftCompletion struct {
	Name         string                `json:"name"`
	Description  string                `json:"description"`
	Markdown     string                `json:"markdown"`
	ID           string                `json:"id"`
	Version      string                `json:"version"`
	Inputs       map[string]draftField `json:"inputs"`
	Outputs      map[string]draftField `json:"outputs"`
	Capabilities []string              `json:"capabilities"`
	Tools        []string              `json:"tools"`
	Runtime      struct {
		Sandbox    string `json:"sandbox"`
		Network    string `json:"network"`
		Filesystem struct {
			Read  []string `json:"read"`
			Write []string `json:"write"`
		} `json:"filesystem"`
		Credentials []string `json:"credentials"`
		MCP         []string `json:"mcp"`
	} `json:"runtime"`
	Preconditions  []string `json:"preconditions"`
	Postconditions []string `json:"postconditions"`
	Evidence       struct {
		Required []string `json:"required"`
	} `json:"evidence"`
	Evaluation struct {
		Suite string `json:"suite"`
	} `json:"evaluation"`
	Questions []DraftQuestion `json:"questions"`
}

type draftField struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

const draftSystemPrompt = `You are the Temporality Skill Builder.
You turn a natural-language description of a capability into a structured skill draft.

Answer with a single JSON object and nothing else. No markdown fences, no commentary.
Schema:
{
  "id": "kebab-case-identifier",
  "version": "1.0.0",
  "name": "Human readable name",
  "description": "One line describing what the skill does",
  "markdown": "SKILL.md body in markdown with ## Purpose, ## When to use, ## Procedure, ## Constraints sections",
  "inputs":  { "field-name": { "type": "string|number|boolean|object|artifact", "description": "...", "required": true } },
  "outputs": { "field-name": { "type": "artifact", "description": "..." } },
  "capabilities": ["verb phrases this skill covers"],
  "tools": ["only tool names from the available tools list"],
  "runtime": { "sandbox": "required|optional|none", "network": "restricted|open|none",
               "filesystem": { "read": ["paths"], "write": ["paths"] },
               "credentials": ["credential names"], "mcp": ["server names from the available list"] },
  "preconditions":  ["states that must hold before execution"],
  "postconditions": ["states that must hold after execution"],
  "evidence": { "required": ["artifacts or observations that must be recorded as proof"] },
  "evaluation": { "suite": "skill-id/default" },
  "questions": [ { "field": "manifest path the question refers to", "question": "a concrete clarifying question for the user" } ]
}

Rules:
- Omit sections you cannot infer rather than inventing values: use empty objects/arrays.
- "tools" and "runtime.mcp" must only reference names from the available tools and servers list.
- Every tool you list must plausibly be needed by the procedure.
- Ask at most 3 short questions, and only when the answer changes the contract.
- Write the markdown instructions in the same language as the user description.`

func draftPrompt(final bool) string {
	if final {
		return draftSystemPrompt + finalRoundInstruction
	}
	return draftSystemPrompt
}

// BuildSkillDraft runs the single model call behind the wizard. final marks a
// follow-up round where the user already answered the builder's questions:
// no new questions are asked or returned.
func BuildSkillDraft(ctx context.Context, model *llm.Client, description, toolsSummary string, final bool) (SkillDraft, error) {
	if model == nil {
		return SkillDraft{}, errors.New("model client is not configured")
	}
	if strings.TrimSpace(description) == "" {
		return SkillDraft{}, errors.New("description is required")
	}
	user := "User description of the skill:\n" + strings.TrimSpace(description)
	if strings.TrimSpace(toolsSummary) != "" {
		user += "\n\nAvailable tools and MCP servers in this workspace:\n" + toolsSummary
	}
	completion, err := model.Complete(ctx, []llm.Message{
		{Role: "system", Content: draftPrompt(final)},
		{Role: "user", Content: user},
	}, nil)
	if err != nil {
		return SkillDraft{}, fmt.Errorf("model call failed: %w", err)
	}
	draft, err := parseDraftCompletion(completion.Content)
	if err != nil {
		return SkillDraft{}, err
	}
	if final {
		// Hard guarantee: one clarification round, ever.
		draft.Questions = nil
	}
	return draft, nil
}

// parseDraftCompletion tolerates reasoning blocks, fenced code blocks and
// stray prose around the JSON payload.
func parseDraftCompletion(content string) (SkillDraft, error) {
	payload, _ := extractJSONObject(content)
	var parsed draftCompletion
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return SkillDraft{}, fmt.Errorf("model returned invalid JSON: %w (model output: %.200s)", err, strings.TrimSpace(content))
	}

	manifest := map[string]any{}
	setIf := func(key string, value any) {
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				manifest[key] = v
			}
		case []string:
			if len(v) > 0 {
				manifest[key] = v
			}
		case map[string]draftField:
			if len(v) > 0 {
				converted := map[string]any{}
				for name, field := range v {
					entry := map[string]any{}
					if strings.TrimSpace(field.Type) != "" {
						entry["type"] = field.Type
					}
					if strings.TrimSpace(field.Description) != "" {
						entry["description"] = field.Description
					}
					if field.Required {
						entry["required"] = true
					}
					if len(entry) > 0 {
						converted[name] = entry
					}
				}
				if len(converted) > 0 {
					manifest[key] = converted
				}
			}
		}
	}
	setIf("id", parsed.ID)
	setIf("version", parsed.Version)
	setIf("name", parsed.Name)
	setIf("description", parsed.Description)
	if strings.TrimSpace(parsed.Evaluation.Suite) != "" {
		manifest["evaluation"] = map[string]any{"suite": parsed.Evaluation.Suite}
	}
	if len(parsed.Evidence.Required) > 0 {
		manifest["evidence"] = map[string]any{"required": parsed.Evidence.Required}
	}
	setIf("inputs", parsed.Inputs)
	setIf("outputs", parsed.Outputs)
	setIf("capabilities", parsed.Capabilities)
	setIf("tools", parsed.Tools)
	runtime := map[string]any{}
	if strings.TrimSpace(parsed.Runtime.Sandbox) != "" {
		runtime["sandbox"] = parsed.Runtime.Sandbox
	}
	if strings.TrimSpace(parsed.Runtime.Network) != "" {
		runtime["network"] = parsed.Runtime.Network
	}
	if len(parsed.Runtime.Filesystem.Read) > 0 || len(parsed.Runtime.Filesystem.Write) > 0 {
		fs := map[string]any{}
		if len(parsed.Runtime.Filesystem.Read) > 0 {
			fs["read"] = parsed.Runtime.Filesystem.Read
		}
		if len(parsed.Runtime.Filesystem.Write) > 0 {
			fs["write"] = parsed.Runtime.Filesystem.Write
		}
		runtime["filesystem"] = fs
	}
	if len(parsed.Runtime.Credentials) > 0 {
		runtime["credentials"] = parsed.Runtime.Credentials
	}
	if len(parsed.Runtime.MCP) > 0 {
		runtime["mcp"] = parsed.Runtime.MCP
	}
	if len(runtime) > 0 {
		manifest["runtime"] = runtime
	}
	if len(parsed.Preconditions) > 0 {
		manifest["preconditions"] = parsed.Preconditions
	}
	if len(parsed.Postconditions) > 0 {
		manifest["postconditions"] = parsed.Postconditions
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return SkillDraft{}, err
	}

	draft := SkillDraft{
		Name:        strings.TrimSpace(parsed.Name),
		Description: strings.TrimSpace(parsed.Description),
		Markdown:    strings.TrimSpace(parsed.Markdown),
		Manifest:    raw,
	}
	for _, q := range parsed.Questions {
		if strings.TrimSpace(q.Question) != "" {
			draft.Questions = append(draft.Questions, DraftQuestion{Field: strings.TrimSpace(q.Field), Question: strings.TrimSpace(q.Question)})
		}
	}
	return draft, nil
}
