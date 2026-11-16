package agent

// Organizational Supervisor batch analysis (docs/temporality-organizational-
// operations-spec.md §6). One structured model call per node batch: the input
// is compact metadata (event summaries, active-run snapshot, recent delivery
// keys — never full traces), the output is addressable decisions. The model
// only CHOOSES recipients; the caller validates every decision against the
// batch it actually built — the model can never invent a target or repeat a
// delivered fact the caller did not show it.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/temporality-project/temporality/kernel/llm"
)

// OrgBatchEvent is one projection event in the batch input.
type OrgBatchEvent struct {
	EventID    string         `json:"event_id"`
	Type       string         `json:"type"`
	Summary    string         `json:"summary,omitempty"`
	RunID      string         `json:"run_id,omitempty"`
	ProjectID  string         `json:"project_id,omitempty"`
	TaskID     string         `json:"task_id,omitempty"`
	ActorID    string         `json:"actor_id,omitempty"`
	OccurredAt string         `json:"occurred_at,omitempty"`
	Refs       map[string]any `json:"refs,omitempty"`
}

// OrgBatchRun is one active-run snapshot row in the batch input.
type OrgBatchRun struct {
	RunID       string `json:"run_id"`
	ProjectID   string `json:"project_id,omitempty"`
	Title       string `json:"title,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	ActorID     string `json:"actor_id,omitempty"`
	Status      string `json:"status"`
	Urgent      bool   `json:"urgent,omitempty"`
	LastEventAt string `json:"last_event_at,omitempty"`
}

// OrgBatchDelivery is a recently delivered awareness row — the dedup context:
// the model must not re-notify the same fact to the same run.
type OrgBatchDelivery struct {
	SourceEventID    string `json:"source_event_id,omitempty"`
	TargetRunID      string `json:"target_run_id"`
	Decision         string `json:"decision,omitempty"`
	DeduplicationKey string `json:"deduplication_key,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
}

// OrgBatchInput is the full batch payload (§6.2).
type OrgBatchInput struct {
	NodeID           string            `json:"org_node_id"`
	Now              string            `json:"now"`
	Events           []OrgBatchEvent   `json:"events"`
	ActiveRuns       []OrgBatchRun     `json:"active_runs"`
	RecentDeliveries []OrgBatchDelivery `json:"recent_deliveries,omitempty"`
	MaxRecipientsPerEvent int           `json:"max_recipients_per_event,omitempty"`
	MaxDeliveriesPerRun   int           `json:"max_deliveries_per_run_per_window,omitempty"`
}

// OrgSuggestedLookup points the recipient at the source run (§6.4).
type OrgSuggestedLookup struct {
	RunID       string `json:"run_id,omitempty"`
	DetailLevel string `json:"detail_level,omitempty"`
}

// OrgDecision is one model decision (§6.4 contract).
type OrgDecision struct {
	SourceEventID    string              `json:"source_event_id,omitempty"`
	TargetRunID      string              `json:"target_run_id,omitempty"`
	Decision         string              `json:"decision"`
	Relevance        string              `json:"relevance,omitempty"`
	Reason           string              `json:"reason,omitempty"`
	Message          string              `json:"message,omitempty"`
	SuggestedLookup  *OrgSuggestedLookup `json:"suggested_lookup,omitempty"`
	Urgency          string              `json:"urgency,omitempty"`
	DeduplicationKey string              `json:"deduplication_key,omitempty"`
}

// OrgBatchVerdict is the parsed batch output.
type OrgBatchVerdict struct {
	Decisions []OrgDecision `json:"decisions"`
}

func parseOrgBatchReply(content string) (OrgBatchVerdict, error) {
	payload, ok := extractJSONObject(content)
	if !ok {
		return OrgBatchVerdict{}, fmt.Errorf("model returned no JSON object")
	}
	var reply struct {
		Decisions []OrgDecision `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(payload), &reply); err != nil {
		return OrgBatchVerdict{}, fmt.Errorf("model returned invalid JSON: %w", err)
	}
	verdict := OrgBatchVerdict{Decisions: make([]OrgDecision, 0, len(reply.Decisions))}
	for _, d := range reply.Decisions {
		d.Decision = strings.ToLower(strings.TrimSpace(d.Decision))
		d.SourceEventID = strings.TrimSpace(d.SourceEventID)
		d.TargetRunID = strings.TrimSpace(d.TargetRunID)
		d.Relevance = strings.ToLower(strings.TrimSpace(d.Relevance))
		d.Urgency = strings.ToLower(strings.TrimSpace(d.Urgency))
		d.DeduplicationKey = strings.TrimSpace(d.DeduplicationKey)
		d.Reason = orgCapText(d.Reason, 400)
		d.Message = orgCapText(d.Message, 600)
		if d.SuggestedLookup != nil {
			if d.SuggestedLookup.DetailLevel != "" {
				level := strings.ToLower(strings.TrimSpace(d.SuggestedLookup.DetailLevel))
				switch level {
				case "summary", "findings", "decisions", "artifacts", "trace":
					d.SuggestedLookup.DetailLevel = level
				default:
					d.SuggestedLookup.DetailLevel = "summary"
				}
			} else {
				d.SuggestedLookup.DetailLevel = "summary"
			}
			d.SuggestedLookup.RunID = strings.TrimSpace(d.SuggestedLookup.RunID)
		}
		verdict.Decisions = append(verdict.Decisions, d)
	}
	return verdict, nil
}

// orgCapText trims and hard-caps a model-written string: the values flow into
// steer messages and must never bloat a prompt.
func orgCapText(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) > max {
		runes := []rune(text)
		if len(runes) > max {
			return strings.TrimSpace(string(runes[:max]))
		}
	}
	return text
}

func orgBatchPrompt(input OrgBatchInput) string {
	encoded, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(`Analyze this organizational activity batch and decide which active runs benefit from knowing about it.

You are the Organizational Supervisor of one org unit. Your ONLY job is information routing: choose which ACTIVE runs should receive short context about relevant work by colleagues. You do not manage tasks, do not change plans, and never message completed runs.

Decisions:
- "ignore": the event is not relevant to any active run (most events are).
- "notify": one specific active run gets a short context message.
- "recommend_lookup": tell a run about relevant work AND suggest it request details.
- "urgent_steer": rare — critical information that cannot wait for the next batch (blocked work, conflicting changes in the same component).
- "update_awareness": a substantially changed fact that was already delivered (use a NEW deduplication_key with a revision suffix).

Hard rules:
- Only notify runs listed in active_runs; never a run that is completed, failed or cancelled.
- Every source_event_id must come from the events array of this batch.
- Be selective: no relevance means ignore. Prefer fewer, well-reasoned notifications.
- Respect the recipient limits printed in the input.
- Distinguish hypothesis from confirmed fact; write the reason as a short factual explanation, no chain-of-thought.
- The message is a short neutral statement (1-3 sentences). It must not include secrets, file contents or full tool results — only what the event summaries already state.
- deduplication_key: a short stable key; reuse the exact key from recent_deliveries ONLY for update_awareness, otherwise derive a new one (e.g. "<source_event_id>|<target_run_id>").

Answer with a single JSON object: {"decisions":[{...}]} — possibly empty.`)
	b.WriteString("\n\nBatch input:\n")
	b.Write(encoded)
	return b.String()
}

// RunOrgAwarenessBatch asks the supervisor model to route one node batch.
// The caller owns building the input from the projection and persisting the
// validated decisions.
func RunOrgAwarenessBatch(ctx context.Context, model *llm.Client, input OrgBatchInput) (OrgBatchVerdict, error) {
	if model == nil {
		return OrgBatchVerdict{}, fmt.Errorf("model client is not configured")
	}
	return completeParsed(ctx, model, []llm.Message{
		{Role: "system", Content: "You route organizational awareness inside an agent company. You answer with JSON only."},
		{Role: "user", Content: orgBatchPrompt(input)},
	}, parseOrgBatchReply)
}
