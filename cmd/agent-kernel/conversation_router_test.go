package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	kernelagent "github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// TestResolveConversationIntentDeterministicCorpus pins the level-1 routing
// on a bilingual corpus (docs/conversational-runtime.md §20): the regression
// corpus starts here, every future router change re-runs it.
func TestResolveConversationIntentDeterministicCorpus(t *testing.T) {
	cases := []struct {
		name          string
		text          string
		attachments   bool
		wantIntent    string
		wantExecution string
	}{
		{"explicit en task", "please implement a new export endpoint", false, "task_request", "agent_execution"},
		{"explicit en research", "could you research the flaky test?", false, "task_request", "agent_execution"},
		{"explicit ru task", "Пожалуйста, реализуй новый endpoint", false, "task_request", "agent_execution"},
		{"explicit ru fix", "Исправь падающий тест в раннере", false, "task_request", "agent_execution"},
		{"attachments mean work", "", true, "task_request", "agent_execution"},
		{"en question", "What does this error mean?", false, "question", "direct"},
		{"ru question", "Почему сборка падает только ночью?", false, "question", "direct"},
		{"ru smalltalk", "Привет! Как дела?", false, "question", "direct"},
		{"not a task verb mid-sentence", "I read about the way they implement deploys", false, "question", "direct"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision := resolveConversationIntent("c-1", tc.text, tc.attachments)
			require.Equal(t, tc.wantIntent, decision.Intent)
			require.Equal(t, tc.wantExecution, decision.Execution)
			require.Equal(t, "deterministic", decision.RouterLevel)
			require.Equal(t, ConversationTarget{Type: "conversation", ID: "c-1"}, decision.Target)
		})
	}
}

// classifyOnce builds a canned level-2 classifier.
func classifyOnce(output ConversationMessageClassifyOutput, err error) ConversationMessageClassifier {
	return func(context.Context, ConversationMessageClassifyInput) (ConversationMessageClassifyOutput, error) {
		return output, err
	}
}

func TestIntentCascadeAdoptsConfidentClassifier(t *testing.T) {
	calls := 0
	classifier := func(context.Context, ConversationMessageClassifyInput) (ConversationMessageClassifyOutput, error) {
		calls++
		return ConversationMessageClassifyOutput{Intent: "task_request", Execution: "agent_execution", Confidence: 0.9}, nil
	}
	decision := resolveConversationIntentCascade(context.Background(), classifier, "c-1", "подготовь отчёт по инциденту", false, false)
	require.Equal(t, "task_request", decision.Intent)
	require.Equal(t, "agent_execution", decision.Execution)
	require.Equal(t, true, decision.RequiresTools)
	require.Equal(t, 0.9, decision.Confidence)
	require.Equal(t, "fast_model", decision.RouterLevel)
	require.Equal(t, 1, calls)
}

func TestIntentCascadeKeepsDeterministicWhenConfidenceMidBand(t *testing.T) {
	decision := resolveConversationIntentCascade(context.Background(),
		classifyOnce(ConversationMessageClassifyOutput{Intent: "question", Execution: "direct", Confidence: 0.65}, nil),
		"c-1", "what about the deploy?", false, false)
	require.Equal(t, "question", decision.Intent, "mid-band must not override the default")
	require.Equal(t, "direct", decision.Execution)
	require.Equal(t, "fast_model", decision.RouterLevel)
	require.False(t, decision.RequiresClarification)
}

func TestIntentCascadeAsksClarificationOnLowConfidence(t *testing.T) {
	decision := resolveConversationIntentCascade(context.Background(),
		classifyOnce(ConversationMessageClassifyOutput{Intent: "unknown", Execution: "clarification", Confidence: 0.4}, nil),
		"c-1", "ну это самое", false, false)
	require.Equal(t, "unknown", decision.Intent)
	require.Equal(t, "clarification", decision.Execution)
	require.True(t, decision.RequiresClarification)
	require.Equal(t, "fast_model", decision.RouterLevel)
}

func TestIntentCascadeAsksClarificationWhenModelRequestsIt(t *testing.T) {
	decision := resolveConversationIntentCascade(context.Background(),
		classifyOnce(ConversationMessageClassifyOutput{Intent: "task_request", Execution: "agent_execution", Confidence: 0.95, RequiresClarification: true}, nil),
		"c-1", "сделай как обычно", false, false)
	require.Equal(t, "clarification", decision.Execution, "requires_clarification wins even over a confident task verdict")
	require.True(t, decision.RequiresClarification)
}

func TestIntentCascadeDegradesOnClassifierFailure(t *testing.T) {
	decision := resolveConversationIntentCascade(context.Background(),
		classifyOnce(ConversationMessageClassifyOutput{}, errors.New("timeout")),
		"c-1", "что там по задаче?", false, false)
	require.Equal(t, "question", decision.Intent, "deterministic default stands")
	require.Equal(t, "direct", decision.Execution)
	require.Equal(t, "fallback", decision.RouterLevel)
}

func TestIntentCascadeIgnoresMalformedClassifierOutput(t *testing.T) {
	decision := resolveConversationIntentCascade(context.Background(),
		classifyOnce(ConversationMessageClassifyOutput{Intent: "hallucinated", Execution: "direct", Confidence: 0.99}, nil),
		"c-1", "any message", false, false)
	require.Equal(t, "fallback", decision.RouterLevel)
	require.Equal(t, "direct", decision.Execution)

	badConfidence := resolveConversationIntentCascade(context.Background(),
		classifyOnce(ConversationMessageClassifyOutput{Intent: "question", Execution: "direct", Confidence: 1.7}, nil),
		"c-1", "any message", false, false)
	require.Equal(t, "fallback", badConfidence.RouterLevel)
}

func TestIntentCascadeLeadFirstSkipsClassifier(t *testing.T) {
	calls := 0
	classifier := func(context.Context, ConversationMessageClassifyInput) (ConversationMessageClassifyOutput, error) {
		calls++
		return ConversationMessageClassifyOutput{Intent: "task_request", Execution: "agent_execution", Confidence: 0.99}, nil
	}
	decision := resolveConversationIntentCascade(context.Background(), classifier, "c-1", "а теперь ещё вот это", false, true)
	require.Equal(t, "lead_first", decision.RouterLevel)
	require.Equal(t, "direct", decision.Execution)
	require.Equal(t, 0, calls, "active work in the talk must not pay for a classification round")
}

func TestIntentCascadeDeterministicTaskSkipsClassifier(t *testing.T) {
	calls := 0
	classifier := func(context.Context, ConversationMessageClassifyInput) (ConversationMessageClassifyOutput, error) {
		calls++
		return ConversationMessageClassifyOutput{Intent: "question", Execution: "direct", Confidence: 0.99}, nil
	}
	decision := resolveConversationIntentCascade(context.Background(), classifier, "c-1", "реализуй фичу X", false, false)
	require.Equal(t, "agent_execution", decision.Execution)
	require.Equal(t, "deterministic", decision.RouterLevel)
	require.Equal(t, 0, calls)
}

func TestIntentCascadeClassifierTimeoutDoesNotRoute(t *testing.T) {
	slow := func(ctx context.Context, _ ConversationMessageClassifyInput) (ConversationMessageClassifyOutput, error) {
		select {
		case <-ctx.Done():
			return ConversationMessageClassifyOutput{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return ConversationMessageClassifyOutput{Intent: "question", Execution: "direct", Confidence: 0.99}, nil
		}
	}
	started := time.Now()
	decision := resolveConversationIntentCascade(context.Background(), slow, "c-1", "опрос", false, false)
	require.Less(t, time.Since(started), classifierTimeout+time.Second, "the cascade must not wait past its own timeout")
	require.Equal(t, "fallback", decision.RouterLevel)
}

func TestValidIntentClassificationPinsEnums(t *testing.T) {
	require.True(t, validIntentClassification(ConversationMessageClassifyOutput{Intent: "task_request", Execution: "agent_execution", Confidence: 0.5}))
	require.False(t, validIntentClassification(ConversationMessageClassifyOutput{Intent: "hocus", Execution: "direct", Confidence: 0.5}))
	require.False(t, validIntentClassification(ConversationMessageClassifyOutput{Intent: "question", Execution: "teleport", Confidence: 0.5}))
	require.False(t, validIntentClassification(ConversationMessageClassifyOutput{Intent: "question", Execution: "direct", Confidence: -0.1}))
	require.False(t, validIntentClassification(ConversationMessageClassifyOutput{Intent: "question", Execution: "direct", Confidence: 1.1}))
}

// TestRunConversationTurnWrapsPermanentFailure: the no-lead failure carries
// the permanent sentinel the recovery scan relies on (park as 'failed',
// never redrive).
func TestRunConversationTurnWrapsPermanentFailure(t *testing.T) {
	_, err := runConversationTurn(context.Background(), &kernelagent.Activities{}, nil, workspace.Conversation{ID: "c"}, nil, "", conversationClarifyDirective)
	require.Error(t, err)
	require.ErrorIs(t, err, errConversationTurnPermanent)
}
