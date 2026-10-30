package agent

// Supervisor reconciliation (docs/failure-reconciliation.md, wave S1): a long
// run piles up dozens of uncertain operations whose side effects nobody can
// confirm by hand. The supervisor model reads the whole wall and returns ONE
// integrated conclusion per run — needs_attention plus causes and concrete
// remedies — together with a best-effort per-operation effect verdict
// (occurred / none / unknown). The kernel records those effects as
// supervisor-actor reconciliations, so the wall closes without a human, and
// the integrated conclusion is what the human actually reads.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/llm"
	"github.com/temporality-project/temporality/observation"
)

// SupervisorReconcileVerdict is the model's integrated conclusion for one
// run's wall of uncertain operations.
type SupervisorReconcileVerdict struct {
	NeedsAttention bool     `json:"needs_attention"`
	Summary        string   `json:"summary"`
	Causes         []string `json:"causes"`
	Remedies       []string `json:"remedies"`
	// Per-operation effects: the model's best answer to «did the side effect
	// happen» for every operation it was shown. Recorded as supervisor-actor
	// reconciliations; ops the model is unsure about stay «unknown» — the
	// honest answer.
	Verdicts []SupervisorOpVerdict `json:"verdicts"`
}

type SupervisorOpVerdict struct {
	OperationID string `json:"operation_id"`
	// occurred / none / unknown
	Effect string `json:"effect"`
	Why    string `json:"why,omitempty"`
}

// SupervisorWallDigest is what the model sees: one line per uncertain
// operation, plus the run's own error context when the run failed.
type SupervisorWallDigest struct {
	RunID string
	Lines []string
	Error string
}

func supervisorReconcilePrompt(digest SupervisorWallDigest) string {
	var b strings.Builder
	b.WriteString(`A long AI-agent run left a pile of tool operations whose side effects could not be confirmed (worker crash windows, in-flight timeouts, boundary failures). Checking each one by hand is not viable — you produce ONE integrated conclusion for the administrator.

For every operation decide the most plausible effect:
- occurred — the call most plausibly landed (e.g. the command ran, the file was written before the crash)
- none — the call most plausibly did not land (the failure happened before the call, or the tool is internal and cannot affect outside systems)
- unknown — genuinely unobservable

Then the integrated conclusion:
- needs_attention: true only when something in this pile requires action in an EXTERNAL system (cleanup, rollback, notification) or reveals a repeating defect worth fixing now.
- causes: the distinct root causes behind the pile (1-3 items, plain language).
- remedies: concrete next steps for the administrator (0-3 items). Empty when needs_attention is false.

Respond with one JSON object:
{"needs_attention": <bool>, "summary": "<2-4 sentences>", "causes": ["..."], "remedies": ["..."], "verdicts": [{"operation_id": "<id>", "effect": "occurred|none|unknown", "why": "<short>"}]}

Rules: ground every claim in the lines below; cover EVERY operation_id exactly once; if the run itself failed with the same root cause, say so in the summary — an operator reading it must understand the pile in one glance.`)
	fmt.Fprintf(&b, "\nrun_id: %s\n", digest.RunID)
	if digest.Error != "" {
		fmt.Fprintf(&b, "run error: %s\n", digest.Error)
	}
	for _, line := range digest.Lines {
		b.WriteString("- " + line + "\n")
	}
	return b.String()
}

type supervisorReconcileReply struct {
	NeedsAttention bool `json:"needs_attention"`
	Summary        string
	Causes         []string
	Remedies       []string
	Verdicts       []struct {
		OperationID string `json:"operation_id"`
		Effect      string `json:"effect"`
		Why         string `json:"why"`
	} `json:"verdicts"`
}

func cleanVerdictStrings(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func parseSupervisorReconcileReply(content string, knownOps map[string]bool) (SupervisorReconcileVerdict, error) {
	payload, ok := extractJSONObject(content)
	if !ok {
		return SupervisorReconcileVerdict{}, fmt.Errorf("model returned no JSON object")
	}
	var reply supervisorReconcileReply
	if err := json.Unmarshal([]byte(payload), &reply); err != nil {
		return SupervisorReconcileVerdict{}, fmt.Errorf("model returned invalid JSON: %w", err)
	}
	summary := strings.TrimSpace(reply.Summary)
	if summary == "" {
		return SupervisorReconcileVerdict{}, fmt.Errorf("model returned an empty summary")
	}
	verdict := SupervisorReconcileVerdict{
		NeedsAttention: reply.NeedsAttention,
		Summary:        summary,
		Causes:         cleanVerdictStrings(reply.Causes),
		Remedies:       cleanVerdictStrings(reply.Remedies),
	}
	// Every known op must be covered exactly once; unknown effects are the
	// honest default for ops the model skipped or hallucinated ids for.
	seen := map[string]bool{}
	for _, v := range reply.Verdicts {
		id := strings.TrimSpace(v.OperationID)
		if !knownOps[id] || seen[id] {
			continue
		}
		effect := strings.ToLower(strings.TrimSpace(v.Effect))
		switch effect {
		case "occurred", "none":
			verdict.Verdicts = append(verdict.Verdicts, SupervisorOpVerdict{OperationID: id, Effect: effect, Why: strings.TrimSpace(v.Why)})
		default:
			verdict.Verdicts = append(verdict.Verdicts, SupervisorOpVerdict{OperationID: id, Effect: "unknown", Why: strings.TrimSpace(v.Why)})
		}
		seen[id] = true
	}
	for id := range knownOps {
		if !seen[id] {
			verdict.Verdicts = append(verdict.Verdicts, SupervisorOpVerdict{OperationID: id, Effect: "unknown", Why: "not covered by the model"})
		}
	}
	return verdict, nil
}

// ReconcileFailedRunWall asks the supervisor model for the integrated
// reconciliation of one run's uncertain operations. knownOps lists the
// operation ids the verdict must cover.
func ReconcileFailedRunWall(ctx context.Context, model *llm.Client, digest SupervisorWallDigest, knownOps []string) (SupervisorReconcileVerdict, error) {
	if model == nil {
		return SupervisorReconcileVerdict{}, fmt.Errorf("model client is not configured")
	}
	known := map[string]bool{}
	for _, id := range knownOps {
		known[id] = true
	}
	reply, err := completeParsed(ctx, model, []llm.Message{
		{Role: "system", Content: "You reconcile piles of unverifiable AI-agent tool operations for a platform administrator. You answer with JSON only."},
		{Role: "user", Content: supervisorReconcilePrompt(digest)},
	}, func(content string) (SupervisorReconcileVerdict, error) {
		return parseSupervisorReconcileReply(content, known)
	})
	return reply, err
}

// BuildWallDigest renders the model-facing digest: one line per operation
// with the tool, the uncertainty reason, the turn and the first ~160 chars of
// the error detail or arguments.
func BuildWallDigest(runID string, ops []UncertainOperation, runError string) SupervisorWallDigest {
	digest := SupervisorWallDigest{RunID: runID, Error: runError}
	for _, op := range ops {
		turn := ""
		if idx := strings.Index(op.OperationID, "/turn/"); idx >= 0 {
			if rest := op.OperationID[idx+6:]; len(rest) >= 2 {
				turn = rest[:2]
			}
		}
		detail := op.ErrorDetail
		if detail == "" {
			detail = op.Arguments
		}
		if len(detail) > 160 {
			detail = detail[:160] + "…"
		}
		line := fmt.Sprintf("operation_id: %s | tool: %s | reason: %s", op.OperationID, op.Tool, op.Reason)
		if turn != "" {
			line += " | turn: " + turn
		}
		if op.AgentID != "" {
			line += " | agent: " + op.AgentID
		}
		if detail != "" {
			line += " | detail: " + detail
		}
		digest.Lines = append(digest.Lines, line)
	}
	return digest
}

// PublishSupervisorReconciliation journals the integrated reconciliation as a
// supervisor.verdict event (kind=reconciliation) bound to the run: the run's
// overview renders this card instead of the raw wall of per-op questions.
func (a *Activities) PublishSupervisorReconciliation(ctx context.Context, runID, project string, verdict SupervisorReconcileVerdict, operations, applied int) error {
	if a.Events == nil {
		return nil
	}
	data := map[string]any{
		"run_id":          runID,
		"kind":            "reconciliation",
		"needs_attention": verdict.NeedsAttention,
		"summary":         verdict.Summary,
		"operations":      operations,
		"applied":         applied,
		"model":           "supervisor",
	}
	if len(verdict.Causes) > 0 {
		data["causes"] = verdict.Causes
	}
	if len(verdict.Remedies) > 0 {
		data["remedies"] = verdict.Remedies
	}
	event := observation.Event{
		Schema:     observation.Schema,
		EventID:    fmt.Sprintf("supervisor-reconcile/%s/%d", runID, time.Now().UnixNano()),
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
