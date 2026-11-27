package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	kernelagent "github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/kernel/llm"
)

// markupCompletion mimics what the transport rescue produces for a
// markup-only answer: the tags move into ToolCalls and Content arrives empty.
func markupCompletion(names ...string) llm.Completion {
	calls := make([]llm.ToolCall, 0, len(names))
	for _, name := range names {
		calls = append(calls, llm.ToolCall{ID: "textcall_" + name, Name: name})
	}
	return llm.Completion{Content: "   ", ToolCalls: calls, Finish: "tool_calls"}
}

// TestResolveConversationTurnAnswerNudgesMarkupOnly: a direct turn has no
// tool executor, so markup-only output gets one corrective retry — the retry
// must carry the nudge on top of the original messages — and its prose is
// delivered as the answer.
func TestResolveConversationTurnAnswerNudgesMarkupOnly(t *testing.T) {
	var requests []kernelagent.ModelRequest
	call := func(_ context.Context, request kernelagent.ModelRequest) (llm.Completion, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return markupCompletion("run_status"), nil
		}
		return llm.Completion{Content: "Сейчас активных запусков нет."}, nil
	}
	answer, err := resolveConversationTurnAnswer(context.Background(), call, kernelagent.ModelRequest{
		Model: "m", Messages: []llm.Message{{Role: "system", Content: "s"}, {Role: "user", Content: "что с задачей?"}},
	})
	require.NoError(t, err)
	require.Equal(t, "Сейчас активных запусков нет.", answer)
	require.Len(t, requests, 2)
	require.Len(t, requests[1].Messages, 4)
	require.Equal(t, "system", requests[1].Messages[0].Role)
	require.Equal(t, "assistant", requests[1].Messages[2].Role)
	require.Contains(t, requests[1].Messages[2].Content, "attempted tool call: run_status")
	require.Equal(t, "user", requests[1].Messages[3].Role)
	require.Equal(t, conversationToolMarkupNudge, requests[1].Messages[3].Content)
}

// TestResolveConversationTurnAnswerProseWins: prose mixed with markup is
// already a user-visible answer — no retry, delivered verbatim.
func TestResolveConversationTurnAnswerProseWins(t *testing.T) {
	calls := 0
	call := func(_ context.Context, _ kernelagent.ModelRequest) (llm.Completion, error) {
		calls++
		return llm.Completion{Content: "Готово. <tool_call><function=run_status></tool_call>"}, nil
	}
	answer, err := resolveConversationTurnAnswer(context.Background(), call, kernelagent.ModelRequest{})
	require.NoError(t, err)
	require.Equal(t, "Готово. <tool_call><function=run_status></tool_call>", answer)
	require.Equal(t, 1, calls)
}

// TestResolveConversationTurnAnswerEmptyWithoutTools: a bare empty answer
// (no rescued tool calls) keeps the former honest failure — redrive owns it.
func TestResolveConversationTurnAnswerEmptyWithoutTools(t *testing.T) {
	calls := 0
	call := func(_ context.Context, _ kernelagent.ModelRequest) (llm.Completion, error) {
		calls++
		return llm.Completion{Content: "  "}, nil
	}
	_, err := resolveConversationTurnAnswer(context.Background(), call, kernelagent.ModelRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty response")
	require.Equal(t, 1, calls)
}

// TestResolveConversationTurnAnswerFallbackStopsRedrive: even after the
// nudge the model insists on markup — the graceful fallback completes the
// turn so the user still receives a reply instead of silence.
func TestResolveConversationTurnAnswerFallbackStopsRedrive(t *testing.T) {
	calls := 0
	call := func(_ context.Context, _ kernelagent.ModelRequest) (llm.Completion, error) {
		calls++
		return markupCompletion("run_status", "list_tasks"), nil
	}
	answer, err := resolveConversationTurnAnswer(context.Background(), call, kernelagent.ModelRequest{})
	require.NoError(t, err)
	require.Equal(t, conversationToolMarkupFallback, answer)
	require.Equal(t, 2, calls)
}

// TestResolveConversationTurnAnswerTransportErrorsStayHonest: transport
// failures (first call or nudge retry) surface as errors — redrive may still
// get a real answer through on a later attempt.
func TestResolveConversationTurnAnswerTransportErrorsStayHonest(t *testing.T) {
	boom := errors.New("provider down")
	first := func(_ context.Context, _ kernelagent.ModelRequest) (llm.Completion, error) {
		return llm.Completion{}, boom
	}
	_, err := resolveConversationTurnAnswer(context.Background(), first, kernelagent.ModelRequest{})
	require.ErrorIs(t, err, boom)

	calls := 0
	second := func(_ context.Context, _ kernelagent.ModelRequest) (llm.Completion, error) {
		calls++
		if calls == 1 {
			return markupCompletion("run_status"), nil
		}
		return llm.Completion{}, boom
	}
	_, err = resolveConversationTurnAnswer(context.Background(), second, kernelagent.ModelRequest{})
	require.ErrorIs(t, err, boom)
	require.Equal(t, 2, calls)
}
