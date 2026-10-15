// Package cost provides model-call cost accounting. Prices are configured
// via KERNEL_MODEL_PRICES as "model:prompt_per_1k:completion_per_1k" comma
// entries. Costs are derived from token counts in model.completed events;
// no new persistent storage is needed.
package cost

import (
	"encoding/json"
	"fmt"
	"net/http"
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

// SagaRollup is one long-running task inside a project: everything spent on
// it across runs, agents and delegation stages — the per-saga row of the
// project budget.
type SagaRollup struct {
	TaskID                string        `json:"task_id"`
	Title                 string        `json:"title,omitempty"` // root run's title
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

// ProjectBudget is the project-level rollup: totals across every saga plus
// one row per saga, so long-running cross-agent tasks become visible next to
// each other.
type ProjectBudget struct {
	Project               string        `json:"project"`
	SagaCount             int           `json:"saga_count"`
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

// ProjectBudgetHandler returns the project rollup: totals across every saga
// plus a row per saga (task) with tokens, time, cost and approval budget.
func (a *CostAPI) ProjectBudgetHandler(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		writeError(w, 400, "project is required")
		return
	}
	events, err := a.queryEventsFiltered(r, project, "", "",
		"run.started", "run.completed", "run.failed", "run.cancelled", "model.completed",
		"approval.requested", "approval.granted", "approval.rejected", "approval.timed_out", "approval.auto_granted")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, a.aggregateProjectBudget(project, events))
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
	if out.firstStartedAt != nil {
		end := now
		if out.lastEndedAt != nil && out.running == 0 {
			end = *out.lastEndedAt
		}
		out.elapsedMS = end.Sub(*out.firstStartedAt).Milliseconds()
	}
	sort.Slice(out.runs, func(i, j int) bool { return out.runs[i].StartedAt.Before(out.runs[j].StartedAt) })
	return out
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

// aggregateProjectBudget groups a project's events by task and rolls each
// saga up, plus project-wide totals. Sagas sort by latest activity first so
// live work stays on top; ties break on task id for determinism.
func (a *CostAPI) aggregateProjectBudget(project string, events []observation.Event) ProjectBudget {
	now := a.now()
	sagas := map[string]*sagaAccumulator{} // deterministic order comes from the sort below
	for _, ev := range events {
		taskID := ev.Context.Task
		if taskID == "" {
			continue // saga = task; taskless events have no saga to belong to
		}
		saga := sagas[taskID]
		if saga == nil {
			saga = newSagaAccumulator()
			sagas[taskID] = saga
		}
		a.ingestSagaEvent(saga, ev)
	}
	budget := ProjectBudget{Project: project}
	for taskID, saga := range sagas {
		totals := a.finalizeSaga(saga, now)
		rollup := SagaRollup{
			TaskID: taskID, Title: saga.title, RootRunID: saga.rootRunID,
			RunCount: totals.runCount, Running: totals.running, ModelCalls: totals.modelCalls,
			AgentCount:     len(totals.agents),
			FirstStartedAt: totals.firstStartedAt, LastEndedAt: totals.lastEndedAt,
			ElapsedMS: totals.elapsedMS, ActiveMS: totals.activeMS,
			TotalPromptTokens: totals.promptTokens, TotalCachedTokens: totals.cachedTokens,
			TotalCompletionTokens: totals.completionTokens, TotalTokens: totals.totalTokens,
			TotalCostUSD: totals.costUSD,
			Approvals:    saga.approvals.stats(),
		}
		budget.SagaCount++
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
		budget.Sagas = append(budget.Sagas, rollup)
	}
	// Project elapsed spans first saga start → last activity (now while
	// anything is running), mirroring the per-saga definition.
	if budget.FirstStartedAt != nil {
		end := now
		if budget.LastEndedAt != nil {
			anyRunning := false
			for _, saga := range budget.Sagas {
				if saga.Running > 0 {
					anyRunning = true
					break
				}
			}
			if !anyRunning {
				end = *budget.LastEndedAt
			}
		}
		budget.ElapsedMS = end.Sub(*budget.FirstStartedAt).Milliseconds()
	}
	sort.Slice(budget.Sagas, func(i, j int) bool {
		a_, b_ := budget.Sagas[i], budget.Sagas[j]
		aAt, bAt := a_.FirstStartedAt, b_.FirstStartedAt
		if aAt == nil || bAt == nil {
			if aAt != bAt {
				return aAt != nil
			}
			return a_.TaskID < b_.TaskID
		}
		// Newest saga first: recent and live work floats to the top.
		if !aAt.Equal(*bAt) {
			return aAt.After(*bAt)
		}
		return a_.TaskID < b_.TaskID
	})
	return budget
}

func (a *CostAPI) queryEvents(r *http.Request, project, runID string) ([]observation.Event, error) {
	return a.queryEventsFiltered(r, project, runID, "", "model.completed")
}

// queryEventsFiltered pages through journal events. Extra event types beyond
// the first are OR-queried with separate requests (the journal filter takes a
// single type) and results are merged in arrival order.
func (a *CostAPI) queryEventsFiltered(r *http.Request, project, runID, taskID string, eventTypes ...string) ([]observation.Event, error) {
	var all []observation.Event
	for _, eventType := range eventTypes {
		if err := a.queryOneType(r, project, runID, taskID, eventType, &all); err != nil {
			return nil, err
		}
	}
	return all, nil
}

func (a *CostAPI) queryOneType(r *http.Request, project, runID, taskID, eventType string, all *[]observation.Event) error {
	cursor := ""
	for {
		url := fmt.Sprintf("%s/v1/observations/events?project=%s&type=%s&limit=500", a.journalURL, project, eventType)
		if runID != "" {
			url += "&run=" + runID
		}
		if taskID != "" {
			url += "&task=" + taskID
		}
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		req, err := http.NewRequestWithContext(r.Context(), "GET", url, nil)
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
