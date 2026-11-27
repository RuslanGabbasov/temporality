package main

import (
	"regexp"
	"strings"
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
}

type ConversationTarget struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

var explicitTaskRequest = regexp.MustCompile(`(?i)^(?:(?:please|could you|can you|i need you to)[,!]?\s+)?(?:research|investigate|implement|build|fix|create(?: a file)?|write code|modify|update the code|run tests|deploy|search the web|audit(?: this repository)?|analyze this repository|refactor|migrate)(?:\s|$|[,.:!?])`)
var explicitTaskRequestRU = regexp.MustCompile(`(?i)^(?:(?:пожалуйста|можешь|можете|нужно)[,!]?\s+)?(?:исследуй|изучи|реализуй|разработай|построй|исправь|создай|напиши код|измени код|обнови код|запусти тесты|разверни|проведи аудит|проанализируй|рефакторинг|мигрируй)(?:\s|$|[,.:!?])`)

func resolveConversationIntent(conversationID, text string, hasAttachments bool) ConversationIntentDecision {
	decision := ConversationIntentDecision{
		Target: ConversationTarget{Type: "conversation", ID: conversationID},
		Intent: "question", Operation: "respond", Execution: "direct", Confidence: 0.78,
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
