package agent

// Answer-path presentation publishing (docs/presentation-engine.md §10.1.1):
// an agent may embed a ```presentation fenced block directly in its final
// answer — simple tables, cards and charts must not require a tool call.
// The workflow hands the fenced payloads to one short activity that validates
// and publishes them through the workspace API; the answer text itself stays
// untouched, so the fenced source remains the honest fallback for channels
// without rendering (§2.1). Failures are best-effort: a broken visual must
// never lose the textual answer (§18 acceptance 10).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/temporality-project/temporality/kernel/presentation"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

// PublishPresentationsInput carries the run identity for provenance pinning
// and the raw fenced payloads in document order.
type PublishPresentationsInput struct {
	RunID     string   `json:"run_id"`
	Project   string   `json:"project"`
	TaskID    string   `json:"task_id,omitempty"`
	AgentID   string   `json:"agent_id,omitempty"`
	ActorID   string   `json:"actor_id,omitempty"`
	Documents []string `json:"documents"`
}

// ActivityPublishPresentations is the activity name registered in the
// kernel's activity registry.
const ActivityPublishPresentations = "kernel.publish_presentations"

// publishAnswerPresentations fires the publish activity best-effort: the
// presentation layer may fail (workspace unavailable, invalid IR) and the
// run still completes with its answer. The 15s/one-attempt context matches
// the kernel's other fire-and-forget activities (renewCtx0 shape).
func publishAnswerPresentations(ctx workflow.Context, activityCtx workflow.Context, input *RunInput, answer string) {
	blocks := presentation.ExtractFencedPresentations(answer)
	if len(blocks) == 0 {
		return
	}
	future := workflow.ExecuteActivity(renewCtx0(activityCtx), ActivityPublishPresentations, PublishPresentationsInput{
		RunID:     input.RunID,
		Project:   input.Project,
		TaskID:    input.TaskID,
		AgentID:   input.AgentID,
		ActorID:   input.ActorID,
		Documents: blocks,
	})
	// Best-effort by contract: an error never surfaces as a run failure.
	_ = future.Get(ctx, nil)
}

// PublishPresentations validates and publishes every fenced block of the
// final answer through the workspace API. Storage errors are logged, not
// returned — the answer is already complete without the visuals.
func (a *Activities) PublishPresentations(ctx context.Context, input PublishPresentationsInput) error {
	for i, raw := range input.Documents {
		id := fmt.Sprintf("presentation-%s-%d", sanitizePresentationID(input.RunID), i+1)
		payload := map[string]any{
			"id":       id,
			"document": json.RawMessage(raw),
			"publish":  true,
			"run_id":   input.RunID,
			"task_id":  input.TaskID,
			"actor":    input.ActorID,
		}
		body, status, err := a.workspaceDo(ctx, http.MethodPost,
			fmt.Sprintf("%s/v1/workspace/presentations?project=%s", a.WorkspaceURL, input.Project), payload)
		if err != nil {
			if logger := activity.GetLogger(ctx); logger != nil {
				logger.Warn("publish answer presentation", "run", input.RunID, "index", i, "error", err)
			}
			continue
		}
		if status >= 400 {
			if logger := activity.GetLogger(ctx); logger != nil {
				logger.Warn("publish answer presentation refused", "run", input.RunID, "index", i, "status", status, "body", truncate(body, 300))
			}
		}
	}
	return nil
}

// sanitizePresentationID strips everything an id slug cannot carry.
func sanitizePresentationID(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		out = "answer"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
