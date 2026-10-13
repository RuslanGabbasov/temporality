package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// Agent evaluation (docs/plan-evaluable-agent.md §2.2 stage 8). The runner
// executes the stored suite against a pinned definition version: each case is
// a standalone model call grounded in the system prompt the agent would run
// with, and the answer is checked against the case's must_contain /
// must_not_contain patterns. Runs are recorded per definition version so the
// evolution view compares definitions, not the drifting current prompt.

const (
	agentEvalMaxCases  = 16
	agentEvalMaxAnswer = 8192
)

// workspaceAgentHeader is the registry projection the evaluator needs to
// resolve the agent and its current definition version.
type workspaceAgentHeader struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	DefinitionVersion int    `json:"definition_version"`
}

// workspaceAgentVersion is the immutable snapshot row; the evaluator only
// needs the compiled prompt pinned to the version.
type workspaceAgentVersion struct {
	Version        int    `json:"version"`
	CompiledPrompt string `json:"compiled_prompt"`
}

// EvalRunMeta pins an evaluation-run row the caller already created (M3 eval
// gate, specs/quality-loop.md §10.4): the gate pre-creates the row so its 202
// answer can name a concrete evaluation_run_id, and the runner finalises that
// exact row instead of inserting a new one. The zero value means "record a new
// run", keeping the plain RunAgentEvaluation contract unchanged.
type EvalRunMeta struct {
	RunID      int64
	ProposalID int64
	Trigger    string
}

// EvalTriggerGate is the trigger string of gate-started runs (§5.1),
// shared with the apply endpoint that pre-creates the run row.
const EvalTriggerGate = "gate"

// RunAgentEvaluation loads the suite, runs every case against the requested
// definition version (0 = current) and records the run. Returns a compact
// summary for the caller (HTTP or a future agent tool).
func (a *Activities) RunAgentEvaluation(ctx context.Context, agentID string, version int) (string, error) {
	return a.RunAgentEvaluationMeta(ctx, agentID, version, EvalRunMeta{})
}

// RunAgentEvaluationMeta is RunAgentEvaluation against a pre-created run row:
// held-out cases are excluded when the run decides the gate (§4.5), the
// record payload carries the row identity, and a failed record of a gate run
// is an error — the caller fails the row instead of leaving it 'unknown'
// forever (§10.4).
func (a *Activities) RunAgentEvaluationMeta(ctx context.Context, agentID string, version int, meta EvalRunMeta) (string, error) {
	if a.Model == nil {
		return "", fmt.Errorf("model client is not configured")
	}
	body, status, err := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/agents/%s", a.WorkspaceURL, agentID), nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", fmt.Errorf("agent %q not found", agentID)
	}
	if status >= 400 {
		return "", fmt.Errorf("agent %q not readable (HTTP %d): %s", agentID, status, truncate(body, 300))
	}
	var header workspaceAgentHeader
	if err := json.Unmarshal([]byte(body), &header); err != nil {
		return "", err
	}

	// Resolve the system prompt under test. A pinned version must evaluate the
	// compiled prompt snapshot of that exact version; version 0 evaluates what
	// a run started now would use (override > compiled > legacy).
	prompt := ""
	testedVersion := version
	if version > 0 && version != header.DefinitionVersion {
		vBody, vStatus, vErr := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/agents/%s/versions", a.WorkspaceURL, agentID), nil)
		if vErr != nil {
			return "", vErr
		}
		if vStatus >= 400 {
			return "", fmt.Errorf("agent versions not readable (HTTP %d)", vStatus)
		}
		var payload struct {
			Versions []workspaceAgentVersion `json:"versions"`
		}
		if err := json.Unmarshal([]byte(vBody), &payload); err != nil {
			return "", err
		}
		found := false
		for _, v := range payload.Versions {
			if v.Version == version {
				prompt = v.CompiledPrompt
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("version %d not found for agent %q", version, agentID)
		}
		if strings.TrimSpace(prompt) == "" {
			return "", fmt.Errorf("version %d of agent %q has no compiled prompt snapshot", version, agentID)
		}
	} else {
		pBody, pStatus, pErr := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/agents/%s/prompt", a.WorkspaceURL, agentID), nil)
		if pErr != nil {
			return "", pErr
		}
		if pStatus >= 400 {
			return "", fmt.Errorf("agent prompt not readable (HTTP %d)", pStatus)
		}
		var payload struct {
			Prompt  string `json:"prompt"`
			Version int    `json:"version"`
		}
		if err := json.Unmarshal([]byte(pBody), &payload); err != nil {
			return "", err
		}
		prompt = payload.Prompt
		testedVersion = payload.Version
		if testedVersion == 0 {
			testedVersion = header.DefinitionVersion
		}
	}

	suiteBody, sStatus, sErr := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/agents/%s/evaluation-suite", a.WorkspaceURL, agentID), nil)
	if sErr != nil {
		return "", sErr
	}
	if sStatus >= 400 {
		return "", fmt.Errorf("evaluation suite not readable (HTTP %d)", sStatus)
	}
	var suite struct {
		Cases []struct {
			Name           string   `json:"name"`
			Input          string   `json:"input"`
			MustContain    []string `json:"must_contain"`
			MustNotContain []string `json:"must_not_contain"`
			HeldOut        bool     `json:"held_out"`
		} `json:"cases"`
	}
	if err := json.Unmarshal([]byte(suiteBody), &suite); err != nil {
		return "", err
	}
	if len(suite.Cases) == 0 {
		return "", fmt.Errorf("agent %q has no evaluation cases; add them in the agent's Evolution tab before evaluating", agentID)
	}
	if len(suite.Cases) > agentEvalMaxCases {
		suite.Cases = suite.Cases[:agentEvalMaxCases]
	}

	results := make([]evaluationCaseResult, 0, len(suite.Cases))
	passed, failed := 0, 0
	// §10.5: per-run token accounting rides agent.evaluation.completed as
	// data.tokens_in / data.tokens_out — the unit economics of the loop.
	tokensIn, tokensOut := 0, 0
	for _, c := range suite.Cases {
		if meta.Trigger == EvalTriggerGate && c.HeldOut {
			continue // §4.5: held-out cases never decide the gate
		}
		result, usage := a.runAgentEvaluationCase(ctx, prompt, c.Input, c.MustContain, c.MustNotContain)
		tokensIn += usage.PromptTokens
		tokensOut += usage.CompletionTokens
		result.Name = c.Name
		if result.Passed {
			passed++
		} else {
			failed++
		}
		results = append(results, result)
	}

	recordPayload := map[string]any{
		"agent_version": testedVersion,
		"passed":        passed,
		"failed":        failed,
		"cases":         results,
	}
	if meta.RunID > 0 {
		// Gate runs finalise the pre-created row (§10.4): identity rides along.
		recordPayload["id"] = meta.RunID
		recordPayload["proposal_id"] = meta.ProposalID
		if meta.Trigger != "" {
			recordPayload["trigger"] = meta.Trigger
		}
	}
	recordBody, rStatus, rErr := a.workspaceDo(ctx, http.MethodPost, fmt.Sprintf("%s/v1/workspace/agents/%s/evaluation-runs", a.WorkspaceURL, agentID), recordPayload)
	recordFailed := rErr != nil || rStatus >= 400
	if recordFailed {
		// The run executed; a recording failure degrades observability only.
		recordBody = fmt.Sprintf(`{"recorded":false,"status":%d}`, rStatus)
	}
	a.emitAgentEvaluation(ctx, agentID, header.Name, testedVersion, passed, failed, tokensIn, tokensOut)
	if meta.RunID > 0 && recordFailed {
		// A gate run must never stay 'unknown': surface the failure so the
		// apply endpoint can fail the row and let a retry start anew (§10.4).
		return "", fmt.Errorf("recording gate evaluation run %d failed: %s", meta.RunID, truncate(recordBody, 300))
	}
	var summary strings.Builder
	fmt.Fprintf(&summary, "%s v%d: %d passed, %d failed\n", agentID, testedVersion, passed, failed)
	for _, r := range results {
		mark := "✓"
		if !r.Passed {
			mark = "✗"
		}
		fmt.Fprintf(&summary, "%s %s", mark, r.Name)
		if len(r.Missed) > 0 || len(r.Unexpected) > 0 {
			fmt.Fprintf(&summary, " (missed: %s; unexpected: %s)", strings.Join(r.Missed, ", "), strings.Join(r.Unexpected, ", "))
		}
		summary.WriteByte('\n')
	}
	summary.WriteString(strings.TrimSpace(string(recordBody)))
	return summary.String(), nil
}

// runAgentEvaluationCase executes one case: a model call grounded in the
// agent's system prompt plus pattern checks. The compiled prompt is used
// as-is — the evaluation measures whether the definition produces the
// expected behaviour, not whether the model follows extra instructions. A
// model failure fails the case (never the whole run) with the error as the
// answer, so suite history stays comparable. The returned usage feeds the
// run's token accounting (§10.5); a failed call reports zero usage.
func (a *Activities) runAgentEvaluationCase(ctx context.Context, system, input string, mustContain, mustNotContain []string) (evaluationCaseResult, llm.Usage) {
	messages := []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: input},
	}
	callCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	completion, err := a.Model.Complete(callCtx, messages, nil)
	answer := completion.Content
	if err != nil {
		answer = "evaluation error: " + err.Error()
		return evaluationCaseResult{Answer: answer, Passed: false}, llm.Usage{}
	}
	if len(answer) > agentEvalMaxAnswer {
		answer = answer[:agentEvalMaxAnswer]
	}
	lower := strings.ToLower(answer)
	result := evaluationCaseResult{Answer: answer, Passed: true}
	for _, needle := range mustContain {
		if needle != "" && !strings.Contains(lower, strings.ToLower(needle)) {
			result.Passed = false
			result.Missed = append(result.Missed, needle)
		}
	}
	for _, forbidden := range mustNotContain {
		if forbidden != "" && strings.Contains(lower, strings.ToLower(forbidden)) {
			result.Passed = false
			result.Unexpected = append(result.Unexpected, forbidden)
		}
	}
	return result, completion.Usage
}

// emitAgentEvaluation records agent.evaluation.completed in the journal via
// the durable outbox. Best-effort: the recorded run row is the source of truth.
// tokens_in/tokens_out carry the run's token accounting (§10.5).
func (a *Activities) emitAgentEvaluation(ctx context.Context, agentID, agentName string, version, passed, failed, tokensIn, tokensOut int) {
	if a.Events == nil {
		return
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("agent-evaluation/%s/%d/%d", agentID, version, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: a.SourceID, Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: a.SourceID, Type: "agent"}},
		Type:       "agent.evaluation.completed",
		Data: map[string]any{
			"agent_id": agentID, "agent_name": agentName,
			"version": version, "passed": passed, "failed": failed,
			"tokens_in": tokensIn, "tokens_out": tokensOut,
		},
	}
	_ = a.Events.Enqueue(ctx, event)
}
