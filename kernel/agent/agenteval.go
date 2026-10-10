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

// RunAgentEvaluation loads the suite, runs every case against the requested
// definition version (0 = current) and records the run. Returns a compact
// summary for the caller (HTTP or a future agent tool).
func (a *Activities) RunAgentEvaluation(ctx context.Context, agentID string, version int) (string, error) {
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
	for _, c := range suite.Cases {
		result := a.runAgentEvaluationCase(ctx, prompt, c.Input, c.MustContain, c.MustNotContain)
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
	recordBody, rStatus, rErr := a.workspaceDo(ctx, http.MethodPost, fmt.Sprintf("%s/v1/workspace/agents/%s/evaluation-runs", a.WorkspaceURL, agentID), recordPayload)
	if rErr != nil || rStatus >= 400 {
		// The run executed; a recording failure degrades observability only.
		recordBody = fmt.Sprintf(`{"recorded":false,"status":%d}`, rStatus)
	}
	a.emitAgentEvaluation(ctx, agentID, header.Name, testedVersion, passed, failed)
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
// answer, so suite history stays comparable.
func (a *Activities) runAgentEvaluationCase(ctx context.Context, system, input string, mustContain, mustNotContain []string) evaluationCaseResult {
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
	return result
}

// emitAgentEvaluation records agent.evaluation.completed in the journal via
// the durable outbox. Best-effort: the recorded run row is the source of truth.
func (a *Activities) emitAgentEvaluation(ctx context.Context, agentID, agentName string, version, passed, failed int) {
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
		},
	}
	_ = a.Events.Enqueue(ctx, event)
}
