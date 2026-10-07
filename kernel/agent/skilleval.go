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
	"github.com/temporality-project/temporality/skills"
)

// Skill evaluation (docs/living-skills.md §24 skill.evaluate, §29). The runner
// executes the stored suite against a concrete skill version: each case is a
// standalone model call grounded in the skill's SKILL.md, and the answer is
// checked against the case's must_contain / must_not_contain patterns. Results
// are recorded as an evaluation run and surfaced through the journal so the
// evolution timeline (§32) can point at them.

const (
	skillEvalMaxCases  = 16
	skillEvalMaxAnswer = 8192
)

// workspaceSkill is the registry projection the evaluator needs.
type workspaceSkill struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Markdown string `json:"markdown"`
}

type workspaceSkillVersion struct {
	Version  string `json:"version"`
	Markdown string `json:"markdown"`
	Status   string `json:"status"`
}

type evaluationCaseResult struct {
	Name       string   `json:"name"`
	Passed     bool     `json:"passed"`
	Answer     string   `json:"answer,omitempty"`
	Missed     []string `json:"missed,omitempty"`
	Unexpected []string `json:"unexpected,omitempty"`
}

// RunSkillEvaluation loads the suite, runs every case against the requested
// skill version and records the run. An empty version evaluates the current
// revision. Returns a compact summary for the caller (tool or HTTP).
func (a *Activities) RunSkillEvaluation(ctx context.Context, skillID, version string) (string, error) {
	if a.Model == nil {
		return "", fmt.Errorf("model client is not configured")
	}
	body, status, err := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/skills/%s", a.WorkspaceURL, skillID), nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", fmt.Errorf("skill %q not found", skillID)
	}
	if status >= 400 {
		return "", fmt.Errorf("skill %q not readable (HTTP %d): %s", skillID, status, truncate(body, 300))
	}
	var skill workspaceSkill
	if err := json.Unmarshal([]byte(body), &skill); err != nil {
		return "", err
	}
	markdown := skill.Markdown
	testedVersion := skill.Version
	if version != "" && version != skill.Version {
		vBody, vStatus, vErr := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/skills/%s/versions", a.WorkspaceURL, skillID), nil)
		if vErr != nil {
			return "", vErr
		}
		if vStatus >= 400 {
			return "", fmt.Errorf("versions not readable (HTTP %d)", vStatus)
		}
		var payload struct {
			Versions []workspaceSkillVersion `json:"versions"`
		}
		if err := json.Unmarshal([]byte(vBody), &payload); err != nil {
			return "", err
		}
		found := false
		for _, v := range payload.Versions {
			if v.Version == version {
				markdown = v.Markdown
				testedVersion = v.Version
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("version %q not found for skill %q", version, skillID)
		}
	}

	suiteBody, sStatus, sErr := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/skills/%s/evaluation-suite", a.WorkspaceURL, skillID), nil)
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
		return "", fmt.Errorf("skill %q has no evaluation cases; add them in the Skills UI before evaluating", skillID)
	}
	if len(suite.Cases) > skillEvalMaxCases {
		suite.Cases = suite.Cases[:skillEvalMaxCases]
	}

	results := make([]evaluationCaseResult, 0, len(suite.Cases))
	passed, failed := 0, 0
	for _, c := range suite.Cases {
		result := a.runEvaluationCase(ctx, skill, markdown, c.Input, c.MustContain, c.MustNotContain)
		result.Name = c.Name
		if result.Passed {
			passed++
		} else {
			failed++
		}
		results = append(results, result)
	}

	recordPayload := map[string]any{
		"skill_version": testedVersion,
		"passed":        passed,
		"failed":        failed,
		"cases":         results,
	}
	recordBody, rStatus, rErr := a.workspaceDo(ctx, http.MethodPost, fmt.Sprintf("%s/v1/workspace/skills/%s/evaluation-runs", a.WorkspaceURL, skillID), recordPayload)
	if rErr != nil || rStatus >= 400 {
		// The run executed; a recording failure degrades observability only.
		recordBody = fmt.Sprintf(`{"recorded":false,"status":%d}`, rStatus)
	}
	a.emitSkillEvaluation(ctx, skillID, skill.Name, testedVersion, passed, failed)
	var summary strings.Builder
	fmt.Fprintf(&summary, "%s %s: %d passed, %d failed\n", skillID, testedVersion, passed, failed)
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

// runEvaluationCase executes one case: a grounded model call plus pattern
// checks. A model failure fails the case (never the whole run) with the error
// as the answer, so suite history stays comparable.
func (a *Activities) runEvaluationCase(ctx context.Context, skill workspaceSkill, markdown, input string, mustContain, mustNotContain []string) evaluationCaseResult {
	system := "You are executing a documented skill. Follow its procedure exactly and answer the task. Reply with the answer only.\n\n# Skill: " + skill.Name + "\n\n" + markdown
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
	if len(answer) > skillEvalMaxAnswer {
		answer = answer[:skillEvalMaxAnswer]
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

// emitSkillEvaluation records skill.evaluation.completed in the journal via
// the durable outbox. Best-effort: the recorded run row is the source of truth.
func (a *Activities) emitSkillEvaluation(ctx context.Context, skillID, skillName, version string, passed, failed int) {
	if a.Events == nil {
		return
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("skill-evaluation/%s/%s/%d", skillID, version, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: a.SourceID, Integration: "agent-kernel", Version: "1"},
		Context:    observation.Context{Actor: observation.Actor{ID: a.SourceID, Type: "agent"}},
		Type:       "skill.evaluation.completed",
		Data: map[string]any{
			"skill_id": skillID, "skill_name": skillName,
			"version": version, "passed": passed, "failed": failed,
		},
	}
	_ = a.Events.Enqueue(ctx, event)
}

// handleSkillEvaluate backs the skill_evaluate agent tool.
func (a *Activities) handleSkillEvaluate(ctx context.Context, request ToolRequest) (ToolResult, error) {
	skillID, _ := request.Arguments["skill_id"].(string)
	if strings.TrimSpace(skillID) == "" {
		return ToolResult{Content: "error: skill_id is required"}, nil
	}
	version, _ := request.Arguments["version"].(string)
	summary, err := a.RunSkillEvaluation(ctx, skillID, version)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	return ToolResult{Content: summary}, nil
}

// handleSkillDiff backs the skill_diff agent tool: manifest + SKILL.md changes
// between two versions (default: current revision → newest draft proposal).
func (a *Activities) handleSkillDiff(ctx context.Context, request ToolRequest) (ToolResult, error) {
	skillID, _ := request.Arguments["skill_id"].(string)
	if strings.TrimSpace(skillID) == "" {
		return ToolResult{Content: "error: skill_id is required"}, nil
	}
	fromVersion, _ := request.Arguments["from_version"].(string)
	toVersion, _ := request.Arguments["to_version"].(string)

	body, status, err := a.workspaceDo(ctx, http.MethodGet, fmt.Sprintf("%s/v1/workspace/skills/%s/versions", a.WorkspaceURL, skillID), nil)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if status == http.StatusNotFound {
		return ToolResult{Content: fmt.Sprintf("error: skill %q not found; use skill_search to list ids", skillID)}, nil
	}
	if status >= 400 {
		return ToolResult{Content: fmt.Sprintf("error: skill_diff failed (HTTP %d): %s", status, truncate(body, 400))}, nil
	}
	var payload struct {
		Versions []struct {
			Version  string          `json:"version"`
			Markdown string          `json:"markdown"`
			Manifest json.RawMessage `json:"manifest"`
			Status   string          `json:"status"`
		} `json:"versions"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	if len(payload.Versions) == 0 {
		return ToolResult{Content: "error: skill has no versions"}, nil
	}
	// versions arrive newest-first; default diff = current revision → newest draft
	pick := func(version string) (int, bool) {
		if version == "" {
			return -1, false
		}
		for i, v := range payload.Versions {
			if v.Version == version {
				return i, true
			}
		}
		return -1, false
	}
	fromIdx, ok := pick(fromVersion)
	if !ok {
		for i, v := range payload.Versions {
			if v.Status == "active" {
				fromIdx = i
				break
			}
		}
		if fromIdx == -1 {
			fromIdx = len(payload.Versions) - 1
		}
	}
	toIdx, ok := pick(toVersion)
	if !ok {
		toIdx = 0
		for i, v := range payload.Versions {
			if v.Status == "draft" {
				toIdx = i
				break
			}
		}
	}
	if fromIdx == toIdx {
		return ToolResult{Content: "error: no different versions to compare (pass from_version/to_version)"}, nil
	}
	from, to := payload.Versions[fromIdx], payload.Versions[toIdx]
	fromManifest, err1 := skills.ParseManifest(string(from.Manifest))
	toManifest, err2 := skills.ParseManifest(string(to.Manifest))
	if err1 != nil || err2 != nil {
		return ToolResult{Content: "error: stored manifest is not parseable"}, nil
	}
	entries := skills.DiffManifests(fromManifest, toManifest)
	added, removed := skills.DiffMarkdown(from.Markdown, to.Markdown)
	return ToolResult{Content: skills.RenderDiff(from.Version, to.Version, entries, added, removed)}, nil
}
