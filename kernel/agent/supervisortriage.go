package agent

// Supervisor triage (docs/supervisor-model.md §3, wave A1): after a run
// fails, the supervisor model reads a compact summary of the trace and
// produces a structured diagnosis — cause class, plain-language summary,
// recommendation, evidence refs. Pure model call + parsing; journal reads,
// dedup and publishing live at the caller (cmd/agent-kernel triage loop).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// SupervisorCauseClass enumerates the triage buckets. Kept closed: an open
// string invites the model to invent taxonomies per run.
type SupervisorCauseClass string

const (
	CauseModelError         SupervisorCauseClass = "model_error"
	CauseSandboxLimit       SupervisorCauseClass = "sandbox_limit"
	CauseHarnessBug         SupervisorCauseClass = "harness_bug"
	CauseAgentPrompt        SupervisorCauseClass = "agent_prompt"
	CauseUserError          SupervisorCauseClass = "user_error"
	CauseExternalDependency SupervisorCauseClass = "external_dependency"
)

// SupervisorTriage is the model's structured diagnosis of one failed run.
type SupervisorTriage struct {
	CauseClass SupervisorCauseClass
	// Pattern is the model's short stable label for the failure kind (e.g.
	// "cpu range", "tmpfs limit", "provider 429") — the human-facing cluster
	// title when several runs share one root cause (wave A2).
	Pattern        string
	Summary        string
	Recommendation string
	Evidence       []string
	// ClusterKey is the deterministic grouping key (wave A2): cause class
	// plus the normalized error text. The model never invents it — clusters
	// must not depend on the model's phrasing stability.
	ClusterKey string
}

// SupervisorTraceRef is the failed-run digest the caller assembles from
// journal events: what the model sees of the failure.
type SupervisorTraceRef struct {
	RunID     string
	ErrorType string
	Error     string
	Timeline  []string // up to ~12 lines: "type: detail"
}

func supervisorTriagePrompt(trace SupervisorTraceRef) string {
	var b strings.Builder
	b.WriteString(`You are triaging a failed AI-agent run for the platform administrator. Classify WHY it failed and what would fix it.

Cause classes (pick exactly one):
- model_error — the model itself failed (provider errors, empty answers, timeouts)
- sandbox_limit — the sandbox refused or killed the work (memory/CPU/tmpfs limits, timeouts)
- harness_bug — a defect in the agent platform itself (tool crashed, impossible state, 500 from internal API)
- agent_prompt — the agent misbehaved: wrong tool usage, ignored instructions, wrong assumptions in its plan
- user_error — the task/prompt set the agent up to fail (impossible request, missing inputs)
- external_dependency — an outside system (git remote, API endpoint, package registry) failed

Respond with one JSON object:
{"cause_class": "<class>", "pattern": "<2-4 word stable label for this failure kind, e.g. 'cpu range', 'tmpfs limit', 'provider 429'>", "summary": "<1-3 sentences, what happened and why>", "recommendation": "<one concrete next step for the administrator>", "evidence": ["<event id or fact>", "..."]}

Failed run:
`)
	fmt.Fprintf(&b, "run_id: %s\nerror_type: %s\nerror: %s\n", trace.RunID, trace.ErrorType, trace.Error)
	for _, line := range trace.Timeline {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	b.WriteString(`
Rules: ground every claim in the lines above; do not invent events. If the lines are ambiguous, pick the closest class and say what is uncertain in the summary. Keep summary under 60 words, recommendation under 30 words.`)
	return b.String()
}

type supervisorTriageReply struct {
	CauseClass     string   `json:"cause_class"`
	Pattern        string   `json:"pattern"`
	Summary        string   `json:"summary"`
	Recommendation string   `json:"recommendation"`
	Evidence       []string `json:"evidence"`
}

func parseSupervisorTriageReply(content string) (SupervisorTriage, error) {
	payload, ok := extractJSONObject(content)
	if !ok {
		return SupervisorTriage{}, fmt.Errorf("model returned no JSON object")
	}
	var reply struct {
		CauseClass     string   `json:"cause_class"`
		Pattern        string   `json:"pattern"`
		Summary        string   `json:"summary"`
		Recommendation string   `json:"recommendation"`
		Evidence       []string `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(payload), &reply); err != nil {
		return SupervisorTriage{}, fmt.Errorf("model returned invalid JSON: %w", err)
	}
	summary := strings.TrimSpace(reply.Summary)
	if summary == "" {
		return SupervisorTriage{}, fmt.Errorf("model returned an empty summary")
	}
	class := SupervisorCauseClass(strings.ToLower(strings.TrimSpace(reply.CauseClass)))
	switch class {
	case CauseModelError, CauseSandboxLimit, CauseHarnessBug, CauseAgentPrompt, CauseUserError, CauseExternalDependency:
	default:
		// An unknown class degrades to the safest umbrella bucket instead of
		// failing the whole verdict — the summary still carries the nuance.
		class = CauseHarnessBug
	}
	pattern := strings.TrimSpace(reply.Pattern)
	if len(pattern) > 64 {
		pattern = pattern[:64]
	}
	evidence := make([]string, 0, len(reply.Evidence))
	for _, ref := range reply.Evidence {
		if ref = strings.TrimSpace(ref); ref != "" {
			evidence = append(evidence, ref)
		}
	}
	return SupervisorTriage{
		CauseClass:     class,
		Pattern:        pattern,
		Summary:        summary,
		Recommendation: strings.TrimSpace(reply.Recommendation),
		Evidence:       evidence,
	}, nil
}

// SupervisorClusterKey derives the deterministic failure-group key (wave A2):
// cause class + error type + the normalized error text. Normalization —
// lowercase, digits → #, path/quote payloads → ~, whitespace collapsed —
// keeps "range of CPUs is from 0.01 to 1.00" and "range of CPUs is from 0.05
// to 2.00" in one cluster while leaving different failure kinds apart. Runs
// sharing a key collapse into one problem-queue ticket.
func SupervisorClusterKey(trace SupervisorTraceRef) string {
	s := strings.ToLower(strings.TrimSpace(trace.Error))
	if len(s) > 400 {
		s = s[:400]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteByte('#')
		case r == '/' || r == '"' || r == '\'' || r == '`':
			b.WriteByte('~')
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	norm := b.String()
	if len(norm) > 120 {
		norm = norm[:120]
	}
	return trace.ErrorType + "|" + norm
}

// TriageFailedRun asks the supervisor model to diagnose one failed run from a
// compact trace digest. Same model the quality loop uses; the caller owns
// reading the journal and publishing the verdict.
func TriageFailedRun(ctx context.Context, model *llm.Client, trace SupervisorTraceRef) (SupervisorTriage, error) {
	if model == nil {
		return SupervisorTriage{}, fmt.Errorf("model client is not configured")
	}
	return TriageFailedRunFromPrompt(ctx, model, supervisorTriagePrompt(trace))
}

// TriageFailedRunFromPrompt runs the triage against an already-rendered
// prompt (test seam).
func TriageFailedRunFromPrompt(ctx context.Context, model *llm.Client, prompt string) (SupervisorTriage, error) {
	if model == nil {
		return SupervisorTriage{}, fmt.Errorf("model client is not configured")
	}
	reply, err := completeParsed(ctx, model, []llm.Message{
		{Role: "system", Content: "You diagnose failed AI-agent runs for a platform administrator. You answer with JSON only."},
		{Role: "user", Content: prompt},
	}, parseSupervisorTriageReply)
	return reply, err
}

// PublishSupervisorVerdict journals the triage verdict as a supervisor.verdict
// event bound to the failed run (project + run context), so the dashboard's
// problem queue and run traces can surface it. clusterKey groups same-root
// failures into one ticket (wave A2); empty key = unclustered legacy row.
// Best-effort by the caller.
func (a *Activities) PublishSupervisorVerdict(ctx context.Context, model string, v SupervisorTriage, project, runID string) error {
	if a.Events == nil {
		return nil
	}
	data := map[string]any{
		"run_id":         runID,
		"cause_class":    string(v.CauseClass),
		"summary":        v.Summary,
		"recommendation": v.Recommendation,
		"model":          model,
	}
	if v.Pattern != "" {
		data["pattern"] = v.Pattern
	}
	if v.ClusterKey != "" {
		data["cluster_key"] = v.ClusterKey
	}
	if len(v.Evidence) > 0 {
		data["evidence"] = v.Evidence
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("supervisor-verdict/%s/%d", runID, time.Now().UnixNano()),
		OccurredAt: time.Now().UTC(),
		Source:     observation.Source{ID: a.SourceID, Integration: "temporality-agent-kernel", Version: "1"},
		Context:    observation.Context{Project: project, Run: runID, Actor: observation.Actor{ID: "supervisor", Type: "system"}},
		Type:       "supervisor.verdict",
		Data:       data,
	}
	if err := event.Validate(); err != nil {
		return err
	}
	return a.Events.Enqueue(ctx, event)
}
