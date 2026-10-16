// teambuilder turns a natural-language description of a collaboration into a
// structured team manifest draft. It is the backend of the team creation
// wizard (docs/agent-teams.md §6): the user describes what the team is for,
// the model extracts the formal definition — protocol, role slots with
// requirements and contracts, defaults — grounded in the actual roster.
// Like the skill builder it never creates the team: the draft only prefills
// the form, creation goes through the regular team API.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

// TeamDraft is the wizard result: a prefilled team definition plus clarifying
// questions the model could not resolve on its own.
type TeamDraft struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Manifest    json.RawMessage `json:"manifest"`
	Questions   []DraftQuestion `json:"questions,omitempty"`
}

// teamDraftCompletion is the strict JSON shape the model must answer with.
type teamDraftCompletion struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Manifest    workspace.TeamManifest `json:"manifest"`
	Questions   []DraftQuestion        `json:"questions"`
}

const teamDraftSystemPrompt = `You are the Temporality Team Builder.
You turn a natural-language description of a collaboration into a structured team definition draft.

Answer with a single JSON object and nothing else. No markdown fences, no commentary.
Schema:
{
  "name": "Short team name, e.g. Code Delivery",
  "description": "What this team is for — one or two sentences",
  "manifest": {
    "protocol": { "kind": "pipeline|lead_workers|fan_out|review_gate|dag",
                  "max_rework": 2,
                  "steps": [ { "id": "step-id", "slot_id": "slot-id", "depends_on": ["step-id"], "review_of": ["step-id"] } ] },
    "slots": [ { "id": "kebab-case-slot-id",
                 "title": "Role title, e.g. Developer, Reviewer, Researcher",
                 "responsibility": "What this role is accountable for",
                 "requirements": { "capabilities": ["read_files","modify_files","run_commands","network","skills","knowledge","delegation"],
                                   "skills": ["skill ids from the roster"] },
                 "contract": "The expected output the next stage relies on",
                 "binding": { "mode": "fixed|role", "agent_id": "roster agent id", "preferred_agent_id": "roster agent id" } } ],
    "defaults": { "max_turns": 40, "budget_usd": 5, "parallel_limit": 4 }
  },
  "questions": [ { "field": "manifest path the question refers to", "question": "a concrete clarifying question for the user" } ]
}

Rules:
- protocol.kind must be one of: pipeline (slots run sequentially in order), lead_workers (first slot decomposes and delegates to the rest), fan_out (all slots but the last run in parallel, the last reduces), review_gate (the last slot reviews the others and can send work back), dag (explicit steps with dependencies).
- "steps" only apply to the dag protocol; omit them otherwise.
- Slot order matters for every protocol except dag: it defines the pipeline, the lead-first roster, the fan-out set plus reducer, and the executor/reviewer split.
- capabilities may only use the seven canonical names; list only ones the role genuinely needs.
- Use binding.mode "fixed" only when the description explicitly names an agent that exists in the roster; otherwise use "role" with requirements. preferred_agent_id is an optional tie-breaker from the roster.
- Skill requirements must reference skill ids that appear in the roster.
- Defaults: set max_turns, budget_usd and parallel_limit only when the description implies a constraint; use 0 to omit.
- The contract of a slot must describe a concrete, checkable output, not a wish.
- Ask at most 3 short questions, and only when the answer changes the definition.
- Write name, description, responsibilities and contracts in the same language as the user description.`

func teamDraftPrompt(final bool) string {
	if final {
		return teamDraftSystemPrompt + finalRoundInstruction
	}
	return teamDraftSystemPrompt
}

// BuildTeamDraft runs the single model call behind the team wizard. final
// marks a follow-up round where the user already answered the builder's
// questions: no new questions are asked or returned.
func BuildTeamDraft(ctx context.Context, model *llm.Client, description, rosterSummary string, final bool) (TeamDraft, error) {
	if model == nil {
		return TeamDraft{}, errors.New("model client is not configured")
	}
	if strings.TrimSpace(description) == "" {
		return TeamDraft{}, errors.New("description is required")
	}
	user := "User description of the team:\n" + strings.TrimSpace(description)
	if strings.TrimSpace(rosterSummary) != "" {
		user += "\n\nAgents available in this workspace (roster):\n" + strings.TrimSpace(rosterSummary)
	}
	draft, err := completeParsed(ctx, model, []llm.Message{
		{Role: "system", Content: teamDraftPrompt(final)},
		{Role: "user", Content: user},
	}, parseTeamDraft)
	if err != nil {
		return TeamDraft{}, err
	}
	if final {
		// Hard guarantee: one clarification round, ever.
		draft.Questions = nil
	}
	return draft, nil
}

// FormatTeamRoster renders the roster for the builder prompt: one line per
// agent with id, name, purpose, skills and hard capability denials, so the
// model grounds fixed bindings and skill requirements in reality.
func FormatTeamRoster(agents []workspace.Agent) string {
	var b strings.Builder
	for i := range agents {
		a := &agents[i]
		fmt.Fprintf(&b, "- %s (%s)", a.ID, a.Name)
		if purpose := strings.TrimSpace(a.Description); purpose != "" {
			if len(purpose) > 140 {
				purpose = purpose[:140] + "…"
			}
			b.WriteString(" — " + purpose)
		}
		if denied := deniedCapabilities(a); len(denied) > 0 {
			fmt.Fprintf(&b, "; denied: %s", strings.Join(denied, ", "))
		}
		if len(a.Skills) > 0 {
			fmt.Fprintf(&b, "; skills: %s", strings.Join(a.Skills, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// deniedCapabilities lists the hard denials of a defined agent (nil or true
// means allowed and is not worth the prompt tokens).
func deniedCapabilities(a *workspace.Agent) []string {
	if a.Definition == nil {
		return nil
	}
	caps := a.Definition.Capabilities
	var denied []string
	flag := func(set *bool, name string) {
		if set != nil && !*set {
			denied = append(denied, name)
		}
	}
	flag(caps.ReadFiles, "read_files")
	flag(caps.ModifyFiles, "modify_files")
	flag(caps.RunCommands, "run_commands")
	flag(caps.Network, "network")
	flag(caps.Skills, "skills")
	flag(caps.Knowledge, "knowledge")
	flag(caps.Delegation, "delegation")
	return denied
}

// parseTeamDraft tolerates reasoning blocks, fenced code blocks and stray
// prose around the JSON payload, normalizes the manifest onto its canonical
// form and rejects drafts that would not validate as a team definition.
func parseTeamDraft(content string) (TeamDraft, error) {
	payload, _ := extractJSONObject(content)
	var parsed teamDraftCompletion
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return TeamDraft{}, fmt.Errorf("model returned invalid JSON: %w (model output: %.200s)", err, strings.TrimSpace(content))
	}
	manifest := normalizeTeamManifest(&parsed.Manifest)
	raw, err := json.Marshal(manifest)
	if err != nil {
		return TeamDraft{}, err
	}
	if err := workspace.ValidateTeamManifest(raw); err != nil {
		return TeamDraft{}, fmt.Errorf("draft manifest is not valid: %w", err)
	}
	draft := TeamDraft{
		Name:        strings.TrimSpace(parsed.Name),
		Description: strings.TrimSpace(parsed.Description),
		Manifest:    raw,
	}
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
			// Uppercase the first rune, not byte: descriptions are often Cyrillic.
			runes := []rune(name)
			runes[0] = unicode.ToUpper(runes[0])
			name = string(runes)
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

// normalizeTeamManifest maps the model's output onto the canonical manifest:
// lowercased enum synonyms, canonical capability names, trimmed ids and
// contracts, a fixed binding without an agent downgraded to a role binding.
func normalizeTeamManifest(m *workspace.TeamManifest) workspace.TeamManifest {
	normalized := workspace.TeamManifest{
		Protocol: workspace.TeamProtocol{
			Kind:      teamProtocolKind(m.Protocol.Kind),
			MaxRework: nonNegative(m.Protocol.MaxRework),
		},
		Defaults: workspace.TeamDefaults{
			MaxTurns:      nonNegative(m.Defaults.MaxTurns),
			BudgetUSD:     nonNegativeFloat(m.Defaults.BudgetUSD),
			ParallelLimit: nonNegative(m.Defaults.ParallelLimit),
		},
	}
	steps := make([]workspace.TeamStep, 0, len(m.Protocol.Steps))
	for _, step := range m.Protocol.Steps {
		step.ID = strings.TrimSpace(step.ID)
		step.SlotID = strings.TrimSpace(step.SlotID)
		step.DependsOn = trimList(step.DependsOn)
		step.ReviewOf = trimList(step.ReviewOf)
		if step.ID != "" {
			steps = append(steps, step)
		}
	}
	normalized.Protocol.Steps = steps

	normalized.Slots = make([]workspace.TeamSlot, 0, len(m.Slots))
	for _, slot := range m.Slots {
		slot.ID = strings.TrimSpace(slot.ID)
		if slot.ID == "" {
			continue
		}
		slot.Title = strings.TrimSpace(slot.Title)
		if slot.Title == "" {
			slot.Title = slot.ID
		}
		slot.Responsibility = strings.TrimSpace(slot.Responsibility)
		slot.Contract = strings.TrimSpace(slot.Contract)
		slot.Binding.Mode = teamBindingMode(slot.Binding.Mode)
		slot.Binding.AgentID = strings.TrimSpace(slot.Binding.AgentID)
		slot.Binding.PreferredAgentID = strings.TrimSpace(slot.Binding.PreferredAgentID)
		// A fixed binding without an agent would not validate; the honest
		// downgrade is a role slot, keeping the mention as a preference.
		if slot.Binding.Mode == workspace.TeamBindingFixed && slot.Binding.AgentID == "" {
			slot.Binding.Mode = workspace.TeamBindingRole
		}
		if slot.Binding.Mode == workspace.TeamBindingRole {
			slot.Binding.AgentID = ""
			capabilities := make([]string, 0, len(slot.Requirements.Capabilities))
			seen := map[string]bool{}
			for _, capability := range slot.Requirements.Capabilities {
				canonical, ok := workspace.NormalizeAgentCapability(normalizeEnum(capability))
				if !ok || seen[canonical] {
					continue
				}
				seen[canonical] = true
				capabilities = append(capabilities, canonical)
			}
			slot.Requirements.Capabilities = capabilities
			slot.Requirements.Skills = trimList(slot.Requirements.Skills)
		}
		normalized.Slots = append(normalized.Slots, slot)
	}
	return normalized
}

// teamProtocolKind maps a loosely written protocol name onto the canonical
// dictionary (case, spaces and dashes are forgiven, plus common synonyms).
func teamProtocolKind(kind string) string {
	normalized := normalizeEnum(kind)
	switch normalized {
	case "pipelines", "conveyor":
		return workspace.TeamProtocolPipeline
	case "lead_workers", "lead_worker", "leadandworkers":
		return workspace.TeamProtocolLeadWorkers
	case "fan_out", "fanout", "fanouts":
		return workspace.TeamProtocolFanOut
	case "review_gate", "reviewgate", "reviewgates", "qa_gate":
		return workspace.TeamProtocolReviewGate
	case "dag", "graph":
		return workspace.TeamProtocolDAG
	default:
		return normalized
	}
}

// teamBindingMode normalizes a binding mode; anything unrecognized stays
// empty so validation reports it instead of silently guessing.
func teamBindingMode(mode string) string {
	switch normalized := normalizeEnum(mode); {
	case strings.Contains(normalized, "fixed"):
		return workspace.TeamBindingFixed
	case normalized == "role" || normalized == "by_role" || normalized == "match":
		return workspace.TeamBindingRole
	default:
		return ""
	}
}

// normalizeEnum lowercases and folds separators to underscores so enum
// synonyms from the model collapse onto one spelling.
func normalizeEnum(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func trimList(values []string) []string {
	trimmed := values[:0]
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	return trimmed
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func nonNegativeFloat(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}
