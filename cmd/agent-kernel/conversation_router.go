package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	kernelagent "github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/workspace"
)

// ConversationIntentDecision keeps target, intent and execution independent,
// matching the Conversational Runtime routing contract.
type ConversationIntentDecision struct {
	Target                ConversationTarget `json:"target"`
	Intent                string             `json:"intent"`
	Operation             string             `json:"operation"`
	Execution             string             `json:"execution"`
	Confidence            float64            `json:"confidence"`
	RequiresTools         bool               `json:"requires_tools"`
	RequiresClarification bool               `json:"requires_clarification"`
	// RouterLevel records which cascade stage produced the decision
	// (deterministic | fast_model | lead_first | fallback) — the regression
	// corpus and the routing observability both need the provenance.
	RouterLevel string `json:"router_level"`
}

type ConversationTarget struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

var explicitTaskRequest = regexp.MustCompile(`(?i)^(?:(?:please|could you|can you|i need you to)[,!]?\s+)?(?:research|investigate|implement|build|fix|create(?: a file)?|write code|modify|update the code|run tests|deploy|search the web|audit(?: this repository)?|analyze this repository|refactor|migrate)(?:\s|$|[,.:!?])`)
var explicitTaskRequestRU = regexp.MustCompile(`(?i)^(?:(?:пожалуйста|можешь|можете|нужно)[,!]?\s+)?(?:исследуй|изучи|реализуй|разработай|построй|исправь|создай|напиши код|измени код|обнови код|запусти(?:\s+тесты)?|разверни|проведи аудит|проанализируй|рефакторинг|мигрируй)(?:\s|$|[,.:!?])`)

func resolveConversationIntent(conversationID, text string, hasAttachments bool) ConversationIntentDecision {
	decision := ConversationIntentDecision{
		Target: ConversationTarget{Type: "conversation", ID: conversationID},
		Intent: "question", Operation: "respond", Execution: "direct", Confidence: 0.78,
		RouterLevel: "deterministic",
	}
	trimmed := strings.TrimSpace(text)
	if hasAttachments || explicitTaskRequest.MatchString(trimmed) || explicitTaskRequestRU.MatchString(trimmed) {
		decision.Intent = "task_request"
		decision.Operation = "start"
		decision.Execution = "agent_execution"
		decision.Confidence = 0.96
	}
	return decision
}

// ConversationMessageClassifyInput is all the fast-model classifier sees:
// limited context by design (docs/conversational-runtime.md §11.1 level 2 —
// низкая задержка, ограниченный контекст, никакого исполнения инструментов).
type ConversationMessageClassifyInput struct {
	Text           string
	HasAttachments bool
	HasActiveTask  bool
}

// ConversationMessageClassifyOutput is the strict classification schema.
type ConversationMessageClassifyOutput struct {
	Intent                string  `json:"intent"`
	Execution             string  `json:"execution"`
	Confidence            float64 `json:"confidence"`
	RequiresClarification bool    `json:"requires_clarification"`
}

// ConversationMessageClassifier classifies one ambiguous message. Production
// wires a compact model call; tests wire canned answers. An error (timeout,
// provider outage, malformed JSON) degrades to the deterministic decision —
// routing must never fail because its optional stage failed.
type ConversationMessageClassifier func(context.Context, ConversationMessageClassifyInput) (ConversationMessageClassifyOutput, error)

// Cascade tuning (docs/conversational-runtime.md §11.3): the classifier only
// OVERRIDES the deterministic default above the adopt threshold; below the
// clarify threshold the message is too ambiguous to act on safely and the
// runtime asks instead of guessing. In between the safe default stands.
const (
	classifierAdoptConfidence  = 0.75
	classifierClarifyThreshold = 0.55
	classifierTimeout          = 3 * time.Second
)

// resolveConversationIntentCascade runs the routing cascade:
//
//	Level 1 deterministic first — callbacks, commands, explicit task verbs,
//	       attachments. High-confidence outcomes short-circuit.
//	Level 3 lead-first — a message landing in a conversation with active work
//	       is a continuation by default: the lead sees it inside the direct
//	       turn anyway, and a separate classifier call would only add latency
//	       (docs §11.1 level 3).
//	Level 2 fast model — everything ambiguous left by the first two stages,
//	       with the strict schema and a hard timeout. No tools, no side
//	       effects: the decision selects a route, it never executes one.
func resolveConversationIntentCascade(ctx context.Context, classifier ConversationMessageClassifier, conversationID, text string, hasAttachments, hasActiveTask bool) ConversationIntentDecision {
	decision := resolveConversationIntent(conversationID, text, hasAttachments)
	if decision.Execution != "direct" {
		return decision // high-confidence deterministic outcome: no model call
	}
	if hasActiveTask {
		// Lead-first: the discussion continues where the work lives.
		decision.RouterLevel = "lead_first"
		return decision
	}
	if classifier == nil || strings.TrimSpace(text) == "" {
		return decision
	}
	classifyCtx, cancel := context.WithTimeout(ctx, classifierTimeout)
	defer cancel()
	output, err := classifier(classifyCtx, ConversationMessageClassifyInput{
		Text: text, HasAttachments: hasAttachments, HasActiveTask: hasActiveTask,
	})
	if err != nil {
		// Degrade openly: the deterministic default answers, the log-level
		// provenance says the model stage did not participate.
		decision.RouterLevel = "fallback"
		return decision
	}
	if !validIntentClassification(output) {
		decision.RouterLevel = "fallback"
		return decision
	}
	if output.Confidence < classifierClarifyThreshold || output.RequiresClarification {
		decision.Intent = "unknown"
		decision.Operation = "clarify"
		decision.Execution = "clarification"
		decision.Confidence = output.Confidence
		decision.RequiresClarification = true
		decision.RouterLevel = "fast_model"
		return decision
	}
	if output.Confidence >= classifierAdoptConfidence {
		decision.Intent = output.Intent
		switch output.Execution {
		case "agent_execution", "delegated_execution":
			decision.Operation = "start"
			decision.RequiresTools = true
		case "clarification":
			decision.Operation = "clarify"
			decision.RequiresClarification = true
		default:
			decision.Operation = "respond"
		}
		decision.Execution = output.Execution
		decision.Confidence = output.Confidence
	}
	decision.RouterLevel = "fast_model"
	return decision
}

// validIntentClassification rejects schema violations: an unknown enum value
// or a wildly out-of-range confidence means the model did not follow the
// contract, and routing falls back instead of trusting it.
func validIntentClassification(output ConversationMessageClassifyOutput) bool {
	switch output.Intent {
	case "discussion", "question", "information_request", "task_request", "task_amendment",
		"task_status", "cancellation", "approval", "feedback", "instruction", "greeting", "unknown":
	default:
		return false
	}
	switch output.Execution {
	case "direct", "lightweight_tools", "agent_execution", "delegated_execution", "clarification":
	default:
		return false
	}
	return output.Confidence >= 0 && output.Confidence <= 1
}

// classifierSystemPrompt is the complete level-2 contract: structured JSON
// only, no tool execution, no consequential actions.
const classifierSystemPrompt = `You classify one user message for a workspace assistant. Reply with ONLY minified JSON, no prose: {"intent":string,"execution":string,"confidence":number,"requires_clarification":boolean}. intent is one of discussion|question|information_request|task_request|task_amendment|task_status|cancellation|approval|feedback|instruction|greeting|unknown. execution is one of direct|lightweight_tools|agent_execution|delegated_execution|clarification. Use agent_execution only when the message clearly asks for work that needs tools or long-running effort (research, code changes, deployment, analysis); use clarification when the message is too ambiguous to act on safely; otherwise direct. confidence in [0,1] reflects classification certainty. You never execute anything and never decide permissions.`

// modelConversationClassifier wires the level-2 stage to the shared model
// gateway. The lead agent's own model serves the classification: routing must
// not depend on a separately provisioned classifier deployment, and the
// prompt is small enough that latency stays acceptable.
func modelConversationClassifier(activities *kernelagent.Activities, store *workspace.Store, conversation workspace.Conversation, lead workspace.Agent) ConversationMessageClassifier {
	return func(ctx context.Context, input ConversationMessageClassifyInput) (ConversationMessageClassifyOutput, error) {
		var metadata map[string]any
		_ = json.Unmarshal(conversation.Metadata, &metadata)
		projectID, _ := metadata["project_id"].(string)
		model := lead.Model
		if model == "" && projectID != "" {
			if project, err := store.GetProject(ctx, projectID); err == nil {
				model = project.DefaultModel
			}
		}
		if model == "" {
			return ConversationMessageClassifyOutput{}, errors.New("no model available for intent classification")
		}
		prompt := fmt.Sprintf("Active task in this conversation: %v. Message has attachments: %v.\nMessage:\n%s",
			input.HasActiveTask, input.HasAttachments, truncateClassifyText(input.Text))
		completion, err := activities.CallModel(ctx, kernelagent.ModelRequest{
			Model: model, ProviderID: lead.Provider, Project: projectID,
			Messages: []llm.Message{{Role: "system", Content: classifierSystemPrompt}, {Role: "user", Content: prompt}},
		})
		if err != nil {
			return ConversationMessageClassifyOutput{}, err
		}
		var output ConversationMessageClassifyOutput
		if err := json.Unmarshal([]byte(strings.TrimSpace(completion.Content)), &output); err != nil {
			return ConversationMessageClassifyOutput{}, err
		}
		return output, nil
	}
}

func truncateClassifyText(text string) string {
	const max = 2000
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > max {
		return string(runes[:max])
	}
	return string(runes)
}
