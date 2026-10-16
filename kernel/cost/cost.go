// Package cost provides model-call cost accounting. Prices are configured
// via KERNEL_MODEL_PRICES as "model:prompt_per_1k:completion_per_1k" comma
// entries. Costs are derived from token counts in model.completed events;
// no new persistent storage is needed.
package cost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	neturl "net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// Price is the cost per 1000 tokens for a model.
type Price struct {
	Model       string  `json:"model"`
	PromptPer1k float64 `json:"prompt_per_1k"`
	CompPer1k   float64 `json:"completion_per_1k"`
}

// Config maps model names to their prices.
type Config struct {
	prices map[string]Price
}

// ParsePrices parses KERNEL_MODEL_PRICES format: "model:p1k:c1k,model2:p1k:c1k"
func ParsePrices(raw string) (Config, error) {
	cfg := Config{prices: map[string]Price{}}
	if raw == "" {
		return cfg, nil
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 3 {
			return cfg, fmt.Errorf("price entry %q must be model:prompt_per_1k:completion_per_1k", entry)
		}
		prompt, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			return cfg, fmt.Errorf("invalid prompt price in %q: %w", entry, err)
		}
		comp, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		if err != nil {
			return cfg, fmt.Errorf("invalid completion price in %q: %w", entry, err)
		}
		model := strings.TrimSpace(parts[0])
		cfg.prices[model] = Price{Model: model, PromptPer1k: prompt, CompPer1k: comp}
	}
	return cfg, nil
}

// ModelCost returns the cost in dollars for a model call. Cached prompt tokens
// are billed at half the prompt price (see kernel/agent cachedPriceFactor for
// the same rule in budget accounting).
func (c Config) ModelCost(model string, promptTokens, cachedTokens, completionTokens int) float64 {
	price, ok := c.prices[model]
	if !ok {
		return 0
	}
	cached := min(cachedTokens, promptTokens)
	fresh := promptTokens - cached
	return (float64(fresh)/1000)*price.PromptPer1k +
		(float64(cached)/1000)*price.PromptPer1k*0.5 +
		(float64(completionTokens)/1000)*price.CompPer1k
}

// Price returns the configured price for a model and whether one exists.
func (c Config) Price(model string) (Price, bool) {
	price, ok := c.prices[model]
	return price, ok
}

// Prices returns all configured prices.
func (c Config) Prices() []Price {
	result := make([]Price, 0, len(c.prices))
	for _, p := range c.prices {
		result = append(result, p)
	}
	return result
}

// CallCost represents the cost of a single model call.
type CallCost struct {
	RunID            string    `json:"run_id"`
	OperationID      string    `json:"operation_id,omitempty"`
	Model            string    `json:"model"`
	Provider         string    `json:"provider"`
	PromptTokens     int       `json:"prompt_tokens"`
	CachedTokens     int       `json:"cached_tokens,omitempty"`
	CompletionTokens int       `json:"completion_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	At               time.Time `json:"at"`
}

// TaskRunBreakdown is one run of a task: identity, timing and the tokens the
// run's own model calls consumed (delegated children are separate rows).
type TaskRunBreakdown struct {
	RunID            string     `json:"run_id"`
	ParentRunID      string     `json:"parent_run_id,omitempty"`
	AgentID          string     `json:"agent_id,omitempty"`
	Status           string     `json:"status"` // running | completed | failed | cancelled
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	DurationMS       int64      `json:"duration_ms"`
	ModelCalls       int        `json:"model_calls"`
	PromptTokens     int        `json:"prompt_tokens"`
	CachedTokens     int        `json:"cached_tokens,omitempty"`
	CompletionTokens int        `json:"completion_tokens"`
	TotalTokens      int        `json:"total_tokens"`
	CostUSD          float64    `json:"cost_usd"`
}

// TaskAgentRollup aggregates all runs one agent executed inside the task.
type TaskAgentRollup struct {
	AgentID          string  `json:"agent_id"`
	Runs             int     `json:"runs"`
	ModelCalls       int     `json:"model_calls"`
	ActiveMS         int64   `json:"active_ms"`
	PromptTokens     int     `json:"prompt_tokens"`
	CachedTokens     int     `json:"cached_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// ApprovalStats aggregates the human-approval budget of a scope: how many
// decisions were asked of people, how they resolved and how long the saga
// waited on them. Timed-out waits count too: the pipeline was blocked either
// way. Auto-granted approvals are policy decisions, not human ones, and are
// reported separately.
type ApprovalStats struct {
	Requested   int   `json:"requested"`
	Granted     int   `json:"granted"`
	Rejected    int   `json:"rejected"`
	TimedOut    int   `json:"timed_out"`
	AutoGranted int   `json:"auto_granted,omitempty"`
	Pending     int   `json:"pending,omitempty"`
	WaitMS      int64 `json:"wait_ms"`               // sum of resolved waits
	MaxWaitMS   int64 `json:"max_wait_ms,omitempty"` // slowest single decision
}

// TaskCost is the aggregated time/token budget of a whole task: every run it
// spans (conversation follow-ups and the full delegation tree) with the sum
// of what was spent.
type TaskCost struct {
	Project               string             `json:"project"`
	TaskID                string             `json:"task_id"`
	RunCount              int                `json:"run_count"`
	ModelCalls            int                `json:"model_calls"`
	FirstStartedAt        *time.Time         `json:"first_started_at,omitempty"`
	LastEndedAt           *time.Time         `json:"last_ended_at,omitempty"`
	ElapsedMS             int64              `json:"elapsed_ms"` // calendar span first start → last end
	ActiveMS              int64              `json:"active_ms"`  // sum of run durations; parallel children count fully
	TotalPromptTokens     int                `json:"total_prompt_tokens"`
	TotalCachedTokens     int                `json:"total_cached_tokens,omitempty"`
	TotalCompletionTokens int                `json:"total_completion_tokens"`
	TotalTokens           int                `json:"total_tokens"`
	TotalCostUSD          float64            `json:"total_cost_usd"`
	Approvals             ApprovalStats      `json:"approvals"`
	Runs                  []TaskRunBreakdown `json:"runs"`
	Agents                []TaskAgentRollup  `json:"agents"`
}

// SagaRollup is one explicitly registered saga (docs/sagas.md): a
// long-running effort that unites one or more tasks — each with its runs,
// agents and delegation stages — with summed tokens, time, cost and the
// human-approval budget.
type SagaRollup struct {
	SagaID                string        `json:"saga_id"`
	Title                 string        `json:"title,omitempty"`
	Goal                  string        `json:"goal,omitempty"`
	MergedInto            string        `json:"merged_into,omitempty"` // set when the saga was merged into another; its bindings were re-projected
	TaskIDs               []string      `json:"task_ids"`
	RootRunID             string        `json:"root_run_id,omitempty"` // earliest bound task's root run
	RegisteredByRun       string        `json:"registered_by_run,omitempty"`
	RegisteredAt          *time.Time    `json:"registered_at,omitempty"`
	RunCount              int           `json:"run_count"`
	Running               int           `json:"running"`
	ModelCalls            int           `json:"model_calls"`
	AgentCount            int           `json:"agent_count"`
	FirstStartedAt        *time.Time    `json:"first_started_at,omitempty"`
	LastEndedAt           *time.Time    `json:"last_ended_at,omitempty"`
	ElapsedMS             int64         `json:"elapsed_ms"`
	ActiveMS              int64         `json:"active_ms"`
	TotalPromptTokens     int           `json:"total_prompt_tokens"`
	TotalCachedTokens     int           `json:"total_cached_tokens,omitempty"`
	TotalCompletionTokens int           `json:"total_completion_tokens"`
	TotalTokens           int           `json:"total_tokens"`
	TotalCostUSD          float64       `json:"total_cost_usd"`
	Approvals             ApprovalStats `json:"approvals"`
}

// TaskRollup is one task inside a project: everything spent on it across
// follow-up runs and the delegation tree, plus the saga it is bound to (empty
// when the task has no explicit saga — those are the "unbound" rows of the
// project page).
type TaskRollup struct {
	TaskID                string        `json:"task_id"`
	SagaID                string        `json:"saga_id,omitempty"`
	Title                 string        `json:"title,omitempty"`
	RootRunID             string        `json:"root_run_id,omitempty"`
	RunCount              int           `json:"run_count"`
	Running               int           `json:"running"`
	ModelCalls            int           `json:"model_calls"`
	AgentCount            int           `json:"agent_count"`
	FirstStartedAt        *time.Time    `json:"first_started_at,omitempty"`
	LastEndedAt           *time.Time    `json:"last_ended_at,omitempty"`
	ElapsedMS             int64         `json:"elapsed_ms"`
	ActiveMS              int64         `json:"active_ms"`
	TotalPromptTokens     int           `json:"total_prompt_tokens"`
	TotalCachedTokens     int           `json:"total_cached_tokens,omitempty"`
	TotalCompletionTokens int           `json:"total_completion_tokens"`
	TotalTokens           int           `json:"total_tokens"`
	TotalCostUSD          float64       `json:"total_cost_usd"`
	Approvals             ApprovalStats `json:"approvals"`
}

// ProjectBudget is the project-level rollup: totals across every task plus a
// row per explicit saga and per task, so long-running cross-agent efforts and
// unbound tasks are both visible (docs/sagas.md).
type ProjectBudget struct {
	Project               string        `json:"project"`
	SagaCount             int           `json:"saga_count"`
	TaskCount             int           `json:"task_count"`
	RunCount              int           `json:"run_count"`
	ModelCalls            int           `json:"model_calls"`
	FirstStartedAt        *time.Time    `json:"first_started_at,omitempty"`
	LastEndedAt           *time.Time    `json:"last_ended_at,omitempty"`
	ElapsedMS             int64         `json:"elapsed_ms"`
	ActiveMS              int64         `json:"active_ms"`
	TotalPromptTokens     int           `json:"total_prompt_tokens"`
	TotalCachedTokens     int           `json:"total_cached_tokens,omitempty"`
	TotalCompletionTokens int           `json:"total_completion_tokens"`
	TotalTokens           int           `json:"total_tokens"`
	TotalCostUSD          float64       `json:"total_cost_usd"`
	Approvals             ApprovalStats `json:"approvals"`
	Sagas                 []SagaRollup  `json:"sagas"`
	Tasks                 []TaskRollup  `json:"tasks"`
}

// RunCost is the aggregated cost for a run.
type RunCost struct {
	RunID                 string     `json:"run_id"`
	Project               string     `json:"project"`
	TotalCostUSD          float64    `json:"total_cost_usd"`
	TotalPromptTokens     int        `json:"total_prompt_tokens"`
	TotalCachedTokens     int        `json:"total_cached_tokens,omitempty"`
	TotalCompletionTokens int        `json:"total_completion_tokens"`
	CallCount             int        `json:"call_count"`
	Calls                 []CallCost `json:"calls,omitempty"`
}

// ProjectCost is the aggregated cost for a project.
type ProjectCost struct {
	Project               string    `json:"project"`
	TotalCostUSD          float64   `json:"total_cost_usd"`
	TotalPromptTokens     int       `json:"total_prompt_tokens"`
	TotalCachedTokens     int       `json:"total_cached_tokens,omitempty"`
	TotalCompletionTokens int       `json:"total_completion_tokens"`
	RunCount              int       `json:"run_count"`
	Runs                  []RunCost `json:"runs,omitempty"`
}

// CostAPI serves cost endpoints.
type CostAPI struct {
	config     Config
	http       *http.Client
	journalURL string
	token      string
	now        func() time.Time
}

// NewCostAPI creates a cost API handler.
func NewCostAPI(config Config, journalURL string, token string) *CostAPI {
	return &CostAPI{
		config:     config,
		http:       &http.Client{Timeout: 10 * time.Second},
		journalURL: journalURL,
		token:      token,
		now:        time.Now,
	}
}

// unknownAgentID labels runs whose start event carried no agent identity
// (legacy or externally started runs).
const unknownAgentID = "-"

// PricesHandler returns configured model prices.
func (a *CostAPI) PricesHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"prices": a.config.Prices()})
}

// RunCostHandler returns cost for a specific run.
func (a *CostAPI) RunCostHandler(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	runID := r.URL.Query().Get("run")
	if project == "" || runID == "" {
		writeError(w, 400, "project and run are required")
		return
	}
	events, err := a.queryEvents(r, project, runID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	cost := a.aggregateRun(project, runID, events)
	writeJSON(w, 200, cost)
}

// ProjectCostHandler returns cost for all runs in a project.
func (a *CostAPI) ProjectCostHandler(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		writeError(w, 400, "project is required")
		return
	}
	events, err := a.queryEvents(r, project, "")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	cost := a.aggregateProject(project, events)
	writeJSON(w, 200, cost)
}

// TaskCostHandler returns the time/token budget of a whole task: every run it
// spans — conversation follow-ups and the full delegation tree — with summed
// tokens, cost, durations and the human-approval budget. Delegated children
// carry the task of their root (kernel ResolveAgent copies TaskID), so a
// task-filtered journal query sees the entire tree without walking parent
// links.
func (a *CostAPI) TaskCostHandler(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	taskID := r.URL.Query().Get("task")
	if project == "" || taskID == "" {
		writeError(w, 400, "project and task are required")
		return
	}
	events, err := a.queryEventsFiltered(r, project, "", taskID,
		"run.started", "run.completed", "run.failed", "run.cancelled", "model.completed",
		"approval.requested", "approval.granted", "approval.rejected", "approval.timed_out", "approval.auto_granted")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, a.aggregateTask(project, taskID, events))
}

// ProjectBudgetHandler returns the project rollup: totals across every task
// plus a row per explicit saga and per task (docs/sagas.md) with tokens, time,
// cost and approval budget.
func (a *CostAPI) ProjectBudgetHandler(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		writeError(w, 400, "project is required")
		return
	}
	events, err := a.queryEventsFiltered(r, project, "", "",
		"run.started", "run.completed", "run.failed", "run.cancelled", "model.completed",
		"approval.requested", "approval.granted", "approval.rejected", "approval.timed_out", "approval.auto_granted",
		"saga.registered", "saga.attached", "saga.detached", "saga.renamed", "saga.merged")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, a.aggregateProjectBudget(project, events))
}

// TeamRunCost is the honest rollup of one team program run: every event of
// the run tree — the root and ids prefixed "<root>/" — folded into totals, a
// per-run breakdown and the team lifecycle snapshot (docs/agent-teams.md §10).
type TeamRunCost struct {
	Project               string             `json:"project"`
	RunID                 string             `json:"run_id"`
	TeamID                string             `json:"team_id,omitempty"`
	Version               string             `json:"version,omitempty"`
	Status                string             `json:"status,omitempty"`
	ReworkRounds          int                `json:"rework_rounds,omitempty"`
	RunCount              int                `json:"run_count"`
	ModelCalls            int                `json:"model_calls"`
	FirstStartedAt        *time.Time         `json:"first_started_at,omitempty"`
	LastEndedAt           *time.Time         `json:"last_ended_at,omitempty"`
	ElapsedMS             int64              `json:"elapsed_ms"`
	ActiveMS              int64              `json:"active_ms"`
	TotalPromptTokens     int                `json:"total_prompt_tokens"`
	TotalCachedTokens     int                `json:"total_cached_tokens"`
	TotalCompletionTokens int                `json:"total_completion_tokens"`
	TotalTokens           int                `json:"total_tokens"`
	TotalCostUSD          float64            `json:"total_cost_usd"`
	Approvals             ApprovalStats      `json:"approvals"`
	Runs                  []TaskRunBreakdown `json:"runs"`
}

// TeamRunCostHandler returns the token/time/cost rollup of a team run tree.
// The root run makes no model calls itself; the value is in the aggregation
// over its slot children, their delegates and the reviewers between them.
func (a *CostAPI) TeamRunCostHandler(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	runID := r.URL.Query().Get("run_id")
	if project == "" || runID == "" {
		writeError(w, 400, "project and run_id are required")
		return
	}
	events, err := a.queryEventsFiltered(r, project, "", "",
		"run.started", "run.completed", "run.failed", "run.cancelled", "model.completed",
		"approval.requested", "approval.granted", "approval.rejected", "approval.timed_out", "approval.auto_granted",
		"team.started", "team.completed", "team.failed")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, a.aggregateTeamRunCost(project, runID, events))
}

// aggregateTeamRunCost folds the events of one run tree (run id == root or
// prefixed "<root>/") into a TeamRunCost. Events of other runs in the project
// are ignored; a tree with no events yet still answers with empty totals.
func (a *CostAPI) aggregateTeamRunCost(project, runID string, events []observation.Event) TeamRunCost {
	out := TeamRunCost{Project: project, RunID: runID}
	saga := newSagaAccumulator()
	inTree := func(run string) bool { return run == runID || strings.HasPrefix(run, runID+"/") }
	var teamStarted, teamEnded *observation.Event
	for i := range events {
		ev := events[i]
		if !inTree(ev.Context.Run) {
			continue
		}
		switch ev.Type {
		case "team.started":
			if ev.Context.Run == runID {
				teamStarted = &events[i]
			}
		case "team.completed", "team.failed":
			if ev.Context.Run == runID {
				teamEnded = &events[i]
			}
		default:
			a.ingestSagaEvent(saga, ev)
		}
	}
	if teamStarted != nil {
		out.TeamID, _ = teamStarted.Data["team_id"].(string)
		out.Version, _ = teamStarted.Data["version"].(string)
	}
	if teamEnded != nil {
		if teamEnded.Type == "team.failed" {
			out.Status = "failed"
		} else {
			out.Status = "completed"
			out.ReworkRounds = toInt(teamEnded.Data["rework_rounds"])
		}
	}
	totals := a.finalizeSaga(saga, a.now())
	out.RunCount = totals.runCount
	out.ModelCalls = totals.modelCalls
	out.FirstStartedAt = totals.firstStartedAt
	out.LastEndedAt = totals.lastEndedAt
	out.ElapsedMS = totals.elapsedMS
	out.ActiveMS = totals.activeMS
	out.TotalPromptTokens = totals.promptTokens
	out.TotalCachedTokens = totals.cachedTokens
	out.TotalCompletionTokens = totals.completionTokens
	out.TotalTokens = totals.totalTokens
	out.TotalCostUSD = totals.costUSD
	out.Approvals = saga.approvals.stats()
	out.Runs = totals.runs
	return out
}

// TeamSlotDispute counts reviewer rejections of one executor slot inside one
// team's runs (docs/agent-teams.md §11): a pair rejected again and again is
// the strongest evolution signal a team emits.
type TeamSlotDispute struct {
	SlotID       string   `json:"slot_id"`
	RejectedBy   string   `json:"rejected_by"`
	Rejections   int      `json:"rejections"`
	Runs         int      `json:"runs"`
	LastRound    int      `json:"last_round,omitempty"`
	LastFeedback string   `json:"last_feedback,omitempty"`
	SourceRuns   []string `json:"source_runs,omitempty"`
}

// TeamVersionMetrics folds one version's team runs into the numbers the
// Evolution tab shows: volume, outcomes, first-try acceptance, rework,
// spend and disputes.
type TeamVersionMetrics struct {
	Version           string            `json:"version"`
	Runs              int               `json:"runs"`
	Completed         int               `json:"completed"`
	Failed            int               `json:"failed"`
	AcceptFirstTry    int               `json:"accept_first_try"`
	TotalReworkRounds int               `json:"total_rework_rounds"`
	AvgReworkRounds   float64           `json:"avg_rework_rounds"`
	TotalTokens       int               `json:"total_tokens"`
	TotalCostUSD      float64           `json:"total_cost_usd"`
	DurationMS        int64             `json:"duration_ms"` // sum of per-run wall time
	Disputes          []TeamSlotDispute `json:"disputes,omitempty"`
}

// TeamMetrics is the per-version metric set of one team.
type TeamMetrics struct {
	TeamID   string               `json:"team_id"`
	Project  string               `json:"project"`
	Versions []TeamVersionMetrics `json:"versions"`
}

// TeamMetricsHandler returns per-version metrics of one team's runs in a
// project: volume, accept-first-try, rework, spend and slot disputes.
func (a *CostAPI) TeamMetricsHandler(w http.ResponseWriter, r *http.Request, teamID string) {
	metrics, err := a.TeamMetrics(r.Context(), r.URL.Query().Get("project"), teamID)
	if err != nil {
		if errors.Is(err, errProjectRequired) {
			writeError(w, 400, "project is required")
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, metrics)
}

// errProjectRequired separates validation from transport in the ctx-based
// team metrics entry point.
var errProjectRequired = errors.New("project is required")

// TeamMetrics loads a team's lifecycle events from the journal and folds them
// into per-version metrics (docs/agent-teams.md §10).
func (a *CostAPI) TeamMetrics(ctx context.Context, project, teamID string) (TeamMetrics, error) {
	if project == "" {
		return TeamMetrics{}, errProjectRequired
	}
	events, err := a.queryEventsCtx(ctx, project,
		"team.started", "team.completed", "team.failed", "plan.task.rejected")
	if err != nil {
		return TeamMetrics{}, err
	}
	return a.aggregateTeamMetrics(teamID, project, events), nil
}

// DisputesOf lists the slot disputes recorded against one version.
func DisputesOf(metrics TeamMetrics, version string) []TeamSlotDispute {
	for _, entry := range metrics.Versions {
		if entry.Version == version {
			return entry.Disputes
		}
	}
	return nil
}

// aggregateTeamMetrics folds a team's lifecycle events into per-version
// metrics. team.started/team.completed pair up by run id; plan.task.rejected
// events of those runs (context.run "<root>/program") count slot disputes.
func (a *CostAPI) aggregateTeamMetrics(teamID, project string, events []observation.Event) TeamMetrics {
	metrics := TeamMetrics{TeamID: teamID, Project: project}
	type runInfo struct {
		version string
		started time.Time
		ended   time.Time
		status  string
		rework  int
		tokens  int
		costUSD float64
		seenEnd bool
	}
	runs := map[string]*runInfo{}
	order := []string{}
	for _, ev := range events {
		switch ev.Type {
		case "team.started":
			if id, _ := ev.Data["team_id"].(string); id != teamID {
				continue
			}
			info := runs[ev.Context.Run]
			if info == nil {
				info = &runInfo{}
				runs[ev.Context.Run] = info
				order = append(order, ev.Context.Run)
			}
			info.version, _ = ev.Data["version"].(string)
			info.started = ev.OccurredAt
		case "team.completed", "team.failed":
			info := runs[ev.Context.Run]
			if info == nil {
				continue
			}
			if info.seenEnd && !ev.OccurredAt.After(info.ended) {
				continue
			}
			info.seenEnd = true
			info.ended = ev.OccurredAt
			if ev.Type == "team.failed" {
				info.status = "failed"
			} else {
				info.status = "completed"
				info.rework = toInt(ev.Data["rework_rounds"])
			}
			info.tokens = toInt(ev.Data["total_tokens"])
			info.costUSD, _ = ev.Data["cost_usd"].(float64)
		}
	}
	// Disputes: rejections land on the root's "/program" frame; map them onto
	// the owning team run by prefix.
	type disputeKey struct{ slot, reviewer string }
	disputes := map[disputeKey]*TeamSlotDispute{}
	disputeRuns := map[disputeKey]map[string]bool{}
	for _, ev := range events {
		if ev.Type != "plan.task.rejected" {
			continue
		}
		var root string
		for _, candidate := range order {
			if ev.Context.Run == candidate || strings.HasPrefix(ev.Context.Run, candidate+"/") {
				root = candidate
				break
			}
		}
		if root == "" {
			continue
		}
		slot, _ := ev.Data["task_id"].(string)
		reviewer, _ := ev.Data["rejected_by"].(string)
		if slot == "" || reviewer == "" {
			continue
		}
		key := disputeKey{slot, reviewer}
		dispute := disputes[key]
		if dispute == nil {
			dispute = &TeamSlotDispute{SlotID: slot, RejectedBy: reviewer}
			disputes[key] = dispute
			disputeRuns[key] = map[string]bool{}
		}
		dispute.Rejections++
		dispute.LastRound = toInt(ev.Data["round"])
		if feedback, _ := ev.Data["feedback"].(string); feedback != "" {
			dispute.LastFeedback = feedback
		}
		disputeRuns[key][root] = true
	}
	byVersion := map[string]*TeamVersionMetrics{}
	for _, root := range order {
		info := runs[root]
		entry := byVersion[info.version]
		if entry == nil {
			entry = &TeamVersionMetrics{Version: info.version}
			byVersion[info.version] = entry
		}
		entry.Runs++
		if info.status == "completed" {
			entry.Completed++
			entry.TotalReworkRounds += info.rework
			if info.rework == 0 {
				entry.AcceptFirstTry++
			}
		} else if info.status == "failed" {
			entry.Failed++
		}
		if info.seenEnd {
			entry.DurationMS += info.ended.Sub(info.started).Milliseconds()
		}
		entry.TotalTokens += info.tokens
		entry.TotalCostUSD += info.costUSD
	}
	for key, dispute := range disputes {
		for root := range disputeRuns[key] {
			dispute.SourceRuns = append(dispute.SourceRuns, root)
		}
		sort.Strings(dispute.SourceRuns)
		dispute.Runs = len(dispute.SourceRuns)
		// A dispute belongs to the version of its latest source run — teams
		// evolve to fix it, so it must follow the most recent evidence.
		if len(dispute.SourceRuns) > 0 {
			latest := dispute.SourceRuns[len(dispute.SourceRuns)-1]
			if info := runs[latest]; info != nil {
				if entry := byVersion[info.version]; entry != nil {
					entry.Disputes = append(entry.Disputes, *dispute)
				}
			}
		}
	}
	versions := make([]TeamVersionMetrics, 0, len(byVersion))
	for _, entry := range byVersion {
		if entry.Completed > 0 {
			entry.AvgReworkRounds = float64(entry.TotalReworkRounds) / float64(entry.Completed)
		}
		sort.Slice(entry.Disputes, func(i, j int) bool { return entry.Disputes[i].Rejections > entry.Disputes[j].Rejections })
		versions = append(versions, *entry)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
	metrics.Versions = versions
	return metrics
}

// TeamEvolutionSignal is one detected evolution candidate: a slot pair whose
// rejections repeat across runs (docs/agent-teams.md §11). The detector is
// deterministic — no model — and only reports, the human decides.
type TeamEvolutionSignal struct {
	Dispute         TeamSlotDispute `json:"dispute"`
	ObservedProblem string          `json:"observed_problem"`
	ProposedChange  string          `json:"proposed_change"`
	ExpectedEffect  string          `json:"expected_effect"`
}

// DetectTeamEvolution turns version metrics into evolution signals. A pair
// qualifies when rejections repeat (>= 2 rounds) across at least two runs of
// the current version — single-run friction is noise, a pattern is a signal.
func DetectTeamEvolution(metrics TeamMetrics, currentVersion string) []TeamEvolutionSignal {
	var signals []TeamEvolutionSignal
	for _, version := range metrics.Versions {
		if version.Version != currentVersion {
			continue
		}
		for _, dispute := range version.Disputes {
			if dispute.Rejections < 2 || dispute.Runs < 2 {
				continue
			}
			signal := TeamEvolutionSignal{Dispute: dispute}
			signal.ObservedProblem = fmt.Sprintf("reviewer slot %q rejected executor slot %q in %d run(s), %d rework round(s) in total", dispute.RejectedBy, dispute.SlotID, dispute.Runs, dispute.Rejections)
			signal.ProposedChange = fmt.Sprintf("strengthen the output contract of slot %q with the recurring reviewer expectation", dispute.SlotID)
			signal.ExpectedEffect = "fewer rework rounds: the executor sees the recurring requirement up front instead of learning it from rejections"
			if feedback := strings.TrimSpace(dispute.LastFeedback); feedback != "" {
				signal.ProposedChange += ": " + feedback
			}
			signals = append(signals, signal)
		}
	}
	return signals
}

// taskRunAgg accumulates one run while scanning task events; terminal
// fields track the latest terminal event for retry/cancel races.
type taskRunAgg struct {
	breakdown  TaskRunBreakdown
	terminalAt time.Time
	terminal   string
}

// approvalAccumulator tracks the human-approval budget of a saga. Requests
// are matched to resolutions by run + operation_id + arguments_hash — the
// same identity the kernel uses to dedup repeat approvals.
type approvalAccumulator struct {
	requested, granted, rejected, timedOut, autoGranted int
	pending                                             map[string]time.Time
	waitMS, maxWaitMS                                   int64
}

func approvalKey(runID string, data map[string]any) string {
	op, _ := data["operation_id"].(string)
	hash, _ := data["arguments_hash"].(string)
	return runID + "\x00" + op + "\x00" + hash
}

func (acc *approvalAccumulator) ingest(runID string, ev observation.Event) {
	switch ev.Type {
	case "approval.requested":
		acc.requested++
		if acc.pending == nil {
			acc.pending = map[string]time.Time{}
		}
		// A re-request of the same operation restarts the wait clock.
		acc.pending[approvalKey(runID, ev.Data)] = ev.OccurredAt
	case "approval.granted":
		acc.resolve(runID, ev, &acc.granted)
	case "approval.rejected":
		acc.resolve(runID, ev, &acc.rejected)
	case "approval.timed_out":
		acc.resolve(runID, ev, &acc.timedOut)
	case "approval.auto_granted":
		acc.autoGranted++
	}
}

func (acc *approvalAccumulator) resolve(runID string, ev observation.Event, counter *int) {
	*counter++
	key := approvalKey(runID, ev.Data)
	if start, ok := acc.pending[key]; ok {
		wait := ev.OccurredAt.Sub(start).Milliseconds()
		if wait < 0 {
			wait = 0
		}
		acc.waitMS += wait
		if wait > acc.maxWaitMS {
			acc.maxWaitMS = wait
		}
		delete(acc.pending, key)
	}
}

func (acc *approvalAccumulator) stats() ApprovalStats {
	return ApprovalStats{
		Requested:   acc.requested,
		Granted:     acc.granted,
		Rejected:    acc.rejected,
		TimedOut:    acc.timedOut,
		AutoGranted: acc.autoGranted,
		Pending:     len(acc.pending),
		WaitMS:      acc.waitMS,
		MaxWaitMS:   acc.maxWaitMS,
	}
}

// sagaAccumulator ingests lifecycle, model and approval events of one task
// (a saga) and finalizes into rollups. Shared by the per-task endpoint and
// the project budget so both aggregate identically.
type sagaAccumulator struct {
	runs      map[string]*taskRunAgg
	approvals approvalAccumulator
	rootRunID string
	title     string
	firstRoot time.Time
}

func newSagaAccumulator() *sagaAccumulator {
	return &sagaAccumulator{runs: map[string]*taskRunAgg{}}
}

func (a *CostAPI) ingestSagaEvent(s *sagaAccumulator, ev observation.Event) {
	runID := ev.Context.Run
	if runID == "" {
		return
	}
	entry := s.runs[runID]
	if entry == nil {
		entry = &taskRunAgg{}
		s.runs[runID] = entry
		entry.breakdown = TaskRunBreakdown{RunID: runID, Status: "running", StartedAt: ev.OccurredAt}
	}
	switch ev.Type {
	case "run.started":
		if ev.OccurredAt.Before(entry.breakdown.StartedAt) {
			entry.breakdown.StartedAt = ev.OccurredAt
		}
		entry.breakdown.ParentRunID, _ = ev.Data["parent_run_id"].(string)
		entry.breakdown.AgentID, _ = ev.Data["agent_id"].(string)
		// The earliest parentless run is the saga's root; its title names the
		// saga in the project list.
		if entry.breakdown.ParentRunID == "" && (s.rootRunID == "" || ev.OccurredAt.Before(s.firstRoot)) {
			s.rootRunID = runID
			s.firstRoot = ev.OccurredAt
			if title, _ := ev.Data["title"].(string); title != "" {
				s.title = title
			}
		}
	case "run.completed", "run.failed", "run.cancelled":
		// Retries or a cancelled-then-completed race may emit several
		// terminal events; the latest one wins.
		if !ev.OccurredAt.After(entry.terminalAt) {
			return
		}
		entry.terminalAt = ev.OccurredAt
		entry.terminal = map[string]string{
			"run.completed": "completed", "run.failed": "failed", "run.cancelled": "cancelled",
		}[ev.Type]
	case "model.completed":
		model, _ := ev.Data["model"].(string)
		if model == "" {
			model, _ = ev.Data["provider"].(string)
		}
		prompt := toInt(ev.Data["prompt_tokens"])
		cached := toInt(ev.Data["cached_tokens"])
		comp := toInt(ev.Data["completion_tokens"])
		callUSD := a.config.ModelCost(model, prompt, cached, comp)
		b := &entry.breakdown
		b.ModelCalls++
		b.PromptTokens += prompt
		b.CachedTokens += cached
		b.CompletionTokens += comp
		b.TotalTokens += prompt + comp
		b.CostUSD += callUSD
	default:
		s.approvals.ingest(runID, ev)
	}
}

// sagaTotals carries the finalized sums of one saga; shared by TaskCost and
// SagaRollup assembly.
type sagaTotals struct {
	runCount, modelCalls, running int
	firstStartedAt, lastEndedAt   *time.Time
	elapsedMS, activeMS           int64
	promptTokens, cachedTokens    int
	completionTokens, totalTokens int
	costUSD                       float64
	agents                        map[string]*TaskAgentRollup
	runs                          []TaskRunBreakdown
}

func (a *CostAPI) finalizeSaga(s *sagaAccumulator, now time.Time) sagaTotals {
	out := sagaTotals{agents: map[string]*TaskAgentRollup{}}
	for _, entry := range s.runs {
		b := entry.breakdown
		if entry.terminal != "" {
			b.Status = entry.terminal
			b.EndedAt = &entry.terminalAt
			b.DurationMS = entry.terminalAt.Sub(b.StartedAt).Milliseconds()
		} else {
			b.DurationMS = now.Sub(b.StartedAt).Milliseconds()
			out.running++
		}
		if b.AgentID == "" {
			b.AgentID = unknownAgentID
		}
		agent := out.agents[b.AgentID]
		if agent == nil {
			agent = &TaskAgentRollup{AgentID: b.AgentID}
			out.agents[b.AgentID] = agent
		}
		agent.Runs++
		agent.ModelCalls += b.ModelCalls
		agent.ActiveMS += b.DurationMS
		agent.PromptTokens += b.PromptTokens
		agent.CachedTokens += b.CachedTokens
		agent.CompletionTokens += b.CompletionTokens
		agent.TotalTokens += b.TotalTokens
		agent.CostUSD += b.CostUSD

		if out.firstStartedAt == nil || b.StartedAt.Before(*out.firstStartedAt) {
			out.firstStartedAt = &b.StartedAt
		}
		if b.EndedAt != nil && (out.lastEndedAt == nil || b.EndedAt.After(*out.lastEndedAt)) {
			out.lastEndedAt = b.EndedAt
		}
		out.runCount++
		out.modelCalls += b.ModelCalls
		out.activeMS += b.DurationMS
		out.promptTokens += b.PromptTokens
		out.cachedTokens += b.CachedTokens
		out.completionTokens += b.CompletionTokens
		out.totalTokens += b.TotalTokens
		out.costUSD += b.CostUSD
		out.runs = append(out.runs, b)
	}
	// Elapsed is the calendar span so far: while any run is live it keeps
	// growing to now; once everything ended it is the last end - first start.
	out.elapsedMS = spanElapsedMS(out.firstStartedAt, out.lastEndedAt, out.running, now)
	sort.Slice(out.runs, func(i, j int) bool { return out.runs[i].StartedAt.Before(out.runs[j].StartedAt) })
	return out
}

// spanElapsedMS returns the calendar span of a scope: from its first start to
// its last end, or up to now while anything is still running.
func spanElapsedMS(first, last *time.Time, running int, now time.Time) int64 {
	if first == nil {
		return 0
	}
	end := now
	if last != nil && running == 0 {
		end = *last
	}
	return end.Sub(*first).Milliseconds()
}

// aggregateTask folds one task's events into a TaskCost (per-saga endpoint).
func (a *CostAPI) aggregateTask(project, taskID string, events []observation.Event) TaskCost {
	now := a.now()
	saga := newSagaAccumulator()
	for _, ev := range events {
		if ev.Context.Task != taskID {
			continue // defense in depth: the journal query already filters by task
		}
		a.ingestSagaEvent(saga, ev)
	}
	totals := a.finalizeSaga(saga, now)
	tasks := TaskCost{
		Project: project, TaskID: taskID,
		RunCount: totals.runCount, ModelCalls: totals.modelCalls,
		FirstStartedAt: totals.firstStartedAt, LastEndedAt: totals.lastEndedAt,
		ElapsedMS: totals.elapsedMS, ActiveMS: totals.activeMS,
		TotalPromptTokens: totals.promptTokens, TotalCachedTokens: totals.cachedTokens,
		TotalCompletionTokens: totals.completionTokens, TotalTokens: totals.totalTokens,
		TotalCostUSD: totals.costUSD,
		Approvals:    saga.approvals.stats(),
		Runs:         totals.runs,
	}
	tasks.Agents = make([]TaskAgentRollup, 0, len(totals.agents))
	for _, agent := range totals.agents {
		tasks.Agents = append(tasks.Agents, *agent)
	}
	sort.Slice(tasks.Agents, func(i, j int) bool {
		if tasks.Agents[i].TotalTokens != tasks.Agents[j].TotalTokens {
			return tasks.Agents[i].TotalTokens > tasks.Agents[j].TotalTokens
		}
		return tasks.Agents[i].AgentID < tasks.Agents[j].AgentID
	})
	return tasks
}

// sagaMeta is the registered identity of a saga (docs/sagas.md): id, title,
// goal and who registered it. Titles come from saga.registered, then the
// latest saga.renamed overrides them — attaches never rename.
type sagaMeta struct {
	sagaID       string
	title, goal  string
	mergedInto   string
	registeredBy string
	registeredAt *time.Time
}

// sagaEventTask resolves the task an event talks about: agent-emitted events
// carry it in context, human-UI ones in data.task_id.
func sagaEventTask(ev observation.Event) string {
	if ev.Context.Task != "" {
		return ev.Context.Task
	}
	taskID, _ := ev.Data["task_id"].(string)
	return taskID
}

// sagaBindings projects saga.* events into task→saga bindings plus saga meta.
// Bindings compare by time, not journal arrival order (merged type queries are
// not globally chronological), and the latest binding per task wins — a
// re-attach moves the task to another saga, a saga.detached drops it back to
// unbound. Renames override meta by the same last-wins rule. Merges re-project
// every binding of the source saga onto its target and mark the source
// mergedInto (chains resolve to the final target). Bindings to unknown saga
// ids have no effect.
func sagaBindings(events []observation.Event) (map[string]string, map[string]*sagaMeta) {
	bindings := map[string]string{}
	sagas := map[string]*sagaMeta{}
	type binding struct {
		sagaID   string
		detached bool
		at       time.Time
	}
	latest := map[string]binding{}
	type rename struct {
		title, goal string
		at          time.Time
	}
	renames := map[string]rename{}
	type merge struct {
		target string
		at     time.Time
	}
	merges := map[string]merge{}
	applyBinding := func(taskID, sagaID string, at time.Time) {
		if taskID == "" {
			return
		}
		if prev, ok := latest[taskID]; ok && prev.at.After(at) {
			return
		}
		latest[taskID] = binding{sagaID: sagaID, at: at}
	}
	for _, ev := range events {
		switch ev.Type {
		case "saga.registered":
			sagaID, _ := ev.Data["saga_id"].(string)
			if sagaID == "" {
				continue
			}
			meta := sagas[sagaID]
			if meta == nil {
				at := ev.OccurredAt
				meta = &sagaMeta{sagaID: sagaID, registeredAt: &at, registeredBy: ev.Context.Run}
				if title, _ := ev.Data["title"].(string); title != "" {
					meta.title = title
				}
				if goal, _ := ev.Data["goal"].(string); goal != "" {
					meta.goal = goal
				}
				sagas[sagaID] = meta
			}
			applyBinding(sagaEventTask(ev), sagaID, ev.OccurredAt)
		case "saga.attached":
			sagaID, _ := ev.Data["saga_id"].(string)
			if sagaID == "" {
				continue
			}
			applyBinding(sagaEventTask(ev), sagaID, ev.OccurredAt)
		case "saga.detached":
			taskID := sagaEventTask(ev)
			if taskID == "" {
				continue
			}
			if prev, ok := latest[taskID]; ok && prev.at.After(ev.OccurredAt) {
				continue
			}
			latest[taskID] = binding{detached: true, at: ev.OccurredAt}
		case "saga.renamed":
			sagaID, _ := ev.Data["saga_id"].(string)
			if sagaID == "" {
				continue
			}
			if prev, ok := renames[sagaID]; ok && prev.at.After(ev.OccurredAt) {
				continue
			}
			item := rename{at: ev.OccurredAt}
			item.title, _ = ev.Data["title"].(string)
			item.goal, _ = ev.Data["goal"].(string)
			renames[sagaID] = item
		case "saga.merged":
			target, _ := ev.Data["saga_id"].(string)
			if target == "" {
				continue
			}
			sources, _ := ev.Data["source_saga_ids"].([]any)
			for _, entry := range sources {
				source, _ := entry.(string)
				if source == "" || source == target {
					continue
				}
				if prev, ok := merges[source]; ok && prev.at.After(ev.OccurredAt) {
					continue
				}
				merges[source] = merge{target: target, at: ev.OccurredAt}
			}
		}
	}
	for sagaID, item := range renames {
		if meta := sagas[sagaID]; meta != nil {
			if item.title != "" {
				meta.title = item.title
			}
			if item.goal != "" {
				meta.goal = item.goal
			}
		}
	}
	// Merge chains: a target merged further on resolves to its final target.
	resolve := func(sagaID string) string {
		seen := map[string]bool{}
		for {
			next, ok := merges[sagaID]
			if !ok || seen[sagaID] {
				break
			}
			seen[sagaID] = true
			sagaID = next.target
		}
		return sagaID
	}
	for _, meta := range sagas {
		if merge, ok := merges[meta.sagaID]; ok {
			meta.mergedInto = resolve(merge.target)
		}
	}
	for taskID, b := range latest {
		if b.detached || b.sagaID == "" {
			continue
		}
		target := resolve(b.sagaID)
		if _, known := sagas[target]; known {
			bindings[taskID] = target
		}
	}
	return bindings, sagas
}

// aggregateProjectBudget rolls a project's events into per-task rows and per-
// saga rows (explicit registrations, docs/sagas.md), plus project-wide totals.
// Rows sort by latest activity first so live work stays on top; ties break on
// ids for determinism.
func (a *CostAPI) aggregateProjectBudget(project string, events []observation.Event) ProjectBudget {
	now := a.now()
	bindings, sagaMetas := sagaBindings(events)
	// Tasks: every task with lifecycle events rolls up on its own row (bound
	// tasks carry their saga_id). Sagas: events of bound tasks feed one shared
	// accumulator per saga, so multi-task sagas sum correctly.
	taskAccs := map[string]*sagaAccumulator{}
	sagaAccs := map[string]*sagaAccumulator{}
	for _, ev := range events {
		taskID := ev.Context.Task
		if taskID == "" || strings.HasPrefix(ev.Type, "saga.") {
			continue // taskless events have no task; saga events carry no spend
		}
		taskAcc := taskAccs[taskID]
		if taskAcc == nil {
			taskAcc = newSagaAccumulator()
			taskAccs[taskID] = taskAcc
		}
		a.ingestSagaEvent(taskAcc, ev)
		if sagaID := bindings[taskID]; sagaID != "" {
			sagaAcc := sagaAccs[sagaID]
			if sagaAcc == nil {
				sagaAcc = newSagaAccumulator()
				sagaAccs[sagaID] = sagaAcc
			}
			a.ingestSagaEvent(sagaAcc, ev)
		}
	}
	budget := ProjectBudget{Project: project, Sagas: []SagaRollup{}, Tasks: []TaskRollup{}}
	for taskID, acc := range taskAccs {
		totals := a.finalizeSaga(acc, now)
		rollup := TaskRollup{
			TaskID: taskID, SagaID: bindings[taskID],
			Title: acc.title, RootRunID: acc.rootRunID,
			RunCount: totals.runCount, Running: totals.running, ModelCalls: totals.modelCalls,
			AgentCount:     len(totals.agents),
			FirstStartedAt: totals.firstStartedAt, LastEndedAt: totals.lastEndedAt,
			ElapsedMS: totals.elapsedMS, ActiveMS: totals.activeMS,
			TotalPromptTokens: totals.promptTokens, TotalCachedTokens: totals.cachedTokens,
			TotalCompletionTokens: totals.completionTokens, TotalTokens: totals.totalTokens,
			TotalCostUSD: totals.costUSD,
			Approvals:    acc.approvals.stats(),
		}
		budget.TaskCount++
		budget.RunCount += rollup.RunCount
		budget.ModelCalls += rollup.ModelCalls
		if budget.FirstStartedAt == nil || (rollup.FirstStartedAt != nil && rollup.FirstStartedAt.Before(*budget.FirstStartedAt)) {
			budget.FirstStartedAt = rollup.FirstStartedAt
		}
		if rollup.LastEndedAt != nil && (budget.LastEndedAt == nil || rollup.LastEndedAt.After(*budget.LastEndedAt)) {
			budget.LastEndedAt = rollup.LastEndedAt
		}
		budget.ActiveMS += rollup.ActiveMS
		budget.TotalPromptTokens += rollup.TotalPromptTokens
		budget.TotalCachedTokens += rollup.TotalCachedTokens
		budget.TotalCompletionTokens += rollup.TotalCompletionTokens
		budget.TotalTokens += rollup.TotalTokens
		budget.TotalCostUSD += rollup.TotalCostUSD
		budget.Approvals.Requested += rollup.Approvals.Requested
		budget.Approvals.Granted += rollup.Approvals.Granted
		budget.Approvals.Rejected += rollup.Approvals.Rejected
		budget.Approvals.TimedOut += rollup.Approvals.TimedOut
		budget.Approvals.AutoGranted += rollup.Approvals.AutoGranted
		budget.Approvals.Pending += rollup.Approvals.Pending
		budget.Approvals.WaitMS += rollup.Approvals.WaitMS
		if rollup.Approvals.MaxWaitMS > budget.Approvals.MaxWaitMS {
			budget.Approvals.MaxWaitMS = rollup.Approvals.MaxWaitMS
		}
		budget.Tasks = append(budget.Tasks, rollup)
	}
	// Registered sagas appear even without spend (registered-only rows); bound
	// tasks that were re-bound away leave the saga's task list but its earlier
	// registration stays visible.
	for sagaID, meta := range sagaMetas {
		rollup := SagaRollup{
			SagaID: sagaID, Title: meta.title, Goal: meta.goal, MergedInto: meta.mergedInto,
			RegisteredByRun: meta.registeredBy, RegisteredAt: meta.registeredAt,
			TaskIDs: []string{}, Approvals: ApprovalStats{},
		}
		for _, task := range budget.Tasks {
			if task.SagaID == sagaID {
				rollup.TaskIDs = append(rollup.TaskIDs, task.TaskID)
			}
		}
		// Tasks roll newest-first; a saga's task list reads chronologically.
		startedAt := map[string]*time.Time{}
		for _, task := range budget.Tasks {
			if task.SagaID == sagaID {
				startedAt[task.TaskID] = task.FirstStartedAt
			}
		}
		sort.Slice(rollup.TaskIDs, func(i, j int) bool {
			aAt, bAt := startedAt[rollup.TaskIDs[i]], startedAt[rollup.TaskIDs[j]]
			if aAt == nil || bAt == nil {
				if aAt != bAt {
					return aAt != nil
				}
				return rollup.TaskIDs[i] < rollup.TaskIDs[j]
			}
			if !aAt.Equal(*bAt) {
				return aAt.Before(*bAt)
			}
			return rollup.TaskIDs[i] < rollup.TaskIDs[j]
		})
		if acc := sagaAccs[sagaID]; acc != nil {
			totals := a.finalizeSaga(acc, now)
			rollup.RootRunID = acc.rootRunID
			rollup.RunCount = totals.runCount
			rollup.Running = totals.running
			rollup.ModelCalls = totals.modelCalls
			rollup.AgentCount = len(totals.agents)
			rollup.FirstStartedAt = totals.firstStartedAt
			rollup.LastEndedAt = totals.lastEndedAt
			rollup.ElapsedMS = totals.elapsedMS
			rollup.ActiveMS = totals.activeMS
			rollup.TotalPromptTokens = totals.promptTokens
			rollup.TotalCachedTokens = totals.cachedTokens
			rollup.TotalCompletionTokens = totals.completionTokens
			rollup.TotalTokens = totals.totalTokens
			rollup.TotalCostUSD = totals.costUSD
			rollup.Approvals = acc.approvals.stats()
			if rollup.Title == "" {
				// Registration carried no title: name the saga after its earliest
				// task's root run before falling back to the bare id.
				rollup.Title = acc.title
			}
		}
		if rollup.Title == "" {
			rollup.Title = sagaID // ids alone are opaque, but better than empty
		}
		budget.SagaCount++
		budget.Sagas = append(budget.Sagas, rollup)
	}
	// Project elapsed spans first task start → last activity (now while
	// anything is running), mirroring the per-task definition.
	anyRunning := false
	for _, task := range budget.Tasks {
		if task.Running > 0 {
			anyRunning = true
			break
		}
	}
	budget.ElapsedMS = spanElapsedMS(budget.FirstStartedAt, budget.LastEndedAt, boolInt(anyRunning), now)
	sort.Slice(budget.Sagas, func(i, j int) bool {
		a_, b_ := budget.Sagas[i], budget.Sagas[j]
		aAt, bAt := sagaSortAt(a_.FirstStartedAt, a_.RegisteredAt), sagaSortAt(b_.FirstStartedAt, b_.RegisteredAt)
		if aAt == nil || bAt == nil {
			if aAt != bAt {
				return aAt != nil
			}
			return a_.SagaID < b_.SagaID
		}
		// Newest saga first: recent and live work floats to the top.
		if !aAt.Equal(*bAt) {
			return aAt.After(*bAt)
		}
		return a_.SagaID < b_.SagaID
	})
	sort.Slice(budget.Tasks, func(i, j int) bool {
		a_, b_ := budget.Tasks[i], budget.Tasks[j]
		aAt, bAt := a_.FirstStartedAt, b_.FirstStartedAt
		if aAt == nil || bAt == nil {
			if aAt != bAt {
				return aAt != nil
			}
			return a_.TaskID < b_.TaskID
		}
		if !aAt.Equal(*bAt) {
			return aAt.After(*bAt)
		}
		return a_.TaskID < b_.TaskID
	})
	return budget
}

// sagaSortAt picks a saga's activity anchor: real spend start when present,
// else its registration time.
func sagaSortAt(first, registered *time.Time) *time.Time {
	if first != nil {
		return first
	}
	return registered
}

// boolInt keeps spanElapsedMS call sites readable.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (a *CostAPI) queryEvents(r *http.Request, project, runID string) ([]observation.Event, error) {
	return a.queryEventsFiltered(r, project, runID, "", "model.completed")
}

// queryEventsCtx is the context-based variant for non-handler callers.
func (a *CostAPI) queryEventsCtx(ctx context.Context, project string, eventTypes ...string) ([]observation.Event, error) {
	return a.queryEventsFilteredCtx(ctx, project, "", "", eventTypes...)
}

// queryEventsFiltered pages through journal events. Extra event types beyond
// the first are OR-queried with separate requests (the journal filter takes a
// single type) and results are merged in arrival order.
func (a *CostAPI) queryEventsFiltered(r *http.Request, project, runID, taskID string, eventTypes ...string) ([]observation.Event, error) {
	return a.queryEventsFilteredCtx(r.Context(), project, runID, taskID, eventTypes...)
}

func (a *CostAPI) queryEventsFilteredCtx(ctx context.Context, project, runID, taskID string, eventTypes ...string) ([]observation.Event, error) {
	return a.queryEventsFilteredSince(ctx, project, runID, taskID, nil, eventTypes...)
}

// queryEventsFilteredSince pages through journal events like
// queryEventsFilteredCtx but bounds the scan by since (nil = unbounded, the
// behaviour of the classic queries): the journal drops events older than the
// bound server-side, so windowed aggregations do not page through history.
func (a *CostAPI) queryEventsFilteredSince(ctx context.Context, project, runID, taskID string, since *time.Time, eventTypes ...string) ([]observation.Event, error) {
	var all []observation.Event
	for _, eventType := range eventTypes {
		if err := a.queryOneType(ctx, project, runID, taskID, eventType, &all, since); err != nil {
			return nil, err
		}
	}
	return all, nil
}

func (a *CostAPI) queryOneType(ctx context.Context, project, runID, taskID, eventType string, all *[]observation.Event, since *time.Time) error {
	cursor := ""
	for {
		url := fmt.Sprintf("%s/v1/observations/events?project=%s&type=%s&limit=500", a.journalURL, project, eventType)
		if runID != "" {
			url += "&run=" + runID
		}
		if taskID != "" {
			url += "&task=" + taskID
		}
		if since != nil {
			url += "&since=" + neturl.QueryEscape(since.Format(time.RFC3339Nano))
		}
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		if a.token != "" {
			req.Header.Set("Authorization", "Bearer "+a.token)
		}
		resp, err := a.http.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var page struct {
			Events     []observation.Event `json:"events"`
			NextCursor string              `json:"next_cursor"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			return err
		}
		*all = append(*all, page.Events...)
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

func (a *CostAPI) aggregateRun(project, runID string, events []observation.Event) RunCost {
	cost := RunCost{RunID: runID, Project: project}
	for _, ev := range events {
		if ev.Context.Run != runID {
			continue
		}
		model, _ := ev.Data["model"].(string)
		if model == "" {
			model, _ = ev.Data["provider"].(string)
		}
		prompt := toInt(ev.Data["prompt_tokens"])
		cached := toInt(ev.Data["cached_tokens"])
		comp := toInt(ev.Data["completion_tokens"])
		provider, _ := ev.Data["provider"].(string)
		costUSD := a.config.ModelCost(model, prompt, cached, comp)
		cc := CallCost{
			RunID:            runID,
			Model:            model,
			Provider:         provider,
			PromptTokens:     prompt,
			CachedTokens:     cached,
			CompletionTokens: comp,
			CostUSD:          costUSD,
			At:               ev.OccurredAt,
		}
		cost.Calls = append(cost.Calls, cc)
		cost.TotalCostUSD += costUSD
		cost.TotalPromptTokens += prompt
		cost.TotalCachedTokens += cached
		cost.TotalCompletionTokens += comp
		cost.CallCount++
	}
	return cost
}

func (a *CostAPI) aggregateProject(project string, events []observation.Event) ProjectCost {
	pc := ProjectCost{Project: project}
	byRun := map[string]*RunCost{}
	for _, ev := range events {
		runID := ev.Context.Run
		if byRun[runID] == nil {
			rc := &RunCost{RunID: runID, Project: project}
			byRun[runID] = rc
		}
		model, _ := ev.Data["model"].(string)
		if model == "" {
			model, _ = ev.Data["provider"].(string)
		}
		prompt := toInt(ev.Data["prompt_tokens"])
		cached := toInt(ev.Data["cached_tokens"])
		comp := toInt(ev.Data["completion_tokens"])
		provider, _ := ev.Data["provider"].(string)
		costUSD := a.config.ModelCost(model, prompt, cached, comp)
		cc := CallCost{
			RunID:            runID,
			Model:            model,
			Provider:         provider,
			PromptTokens:     prompt,
			CachedTokens:     cached,
			CompletionTokens: comp,
			CostUSD:          costUSD,
			At:               ev.OccurredAt,
		}
		byRun[runID].Calls = append(byRun[runID].Calls, cc)
		byRun[runID].TotalCostUSD += costUSD
		byRun[runID].TotalPromptTokens += prompt
		byRun[runID].TotalCachedTokens += cached
		byRun[runID].TotalCompletionTokens += comp
		byRun[runID].CallCount++
	}
	for _, rc := range byRun {
		pc.Runs = append(pc.Runs, *rc)
		pc.TotalCostUSD += rc.TotalCostUSD
		pc.TotalPromptTokens += rc.TotalPromptTokens
		pc.TotalCachedTokens += rc.TotalCachedTokens
		pc.TotalCompletionTokens += rc.TotalCompletionTokens
		pc.RunCount++
	}
	return pc
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
