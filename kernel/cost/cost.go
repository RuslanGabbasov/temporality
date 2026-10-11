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
	Runs                  []TaskRunBreakdown `json:"runs"`
	Agents                []TaskAgentRollup  `json:"agents"`
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
// tokens, cost and durations. Delegated children carry the task of their root
// (kernel ResolveAgent copies TaskID), so a task-filtered journal query sees
// the entire tree without walking parent links.
func (a *CostAPI) TaskCostHandler(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	taskID := r.URL.Query().Get("task")
	if project == "" || taskID == "" {
		writeError(w, 400, "project and task are required")
		return
	}
	events, err := a.queryEventsFiltered(r, project, "", taskID,
		"run.started", "run.completed", "run.failed", "run.cancelled", "model.completed")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, a.aggregateTask(project, taskID, events))
}

// taskRunAgg accumulates one run while scanning task events; terminal
// fields track the latest terminal event for retry/cancel races.
type taskRunAgg struct {
	breakdown  TaskRunBreakdown
	terminalAt time.Time
	terminal   string
}

func (a *CostAPI) aggregateTask(project, taskID string, events []observation.Event) TaskCost {
	now := a.now()
	tasks := TaskCost{Project: project, TaskID: taskID}
	runs := map[string]*taskRunAgg{} // deterministic order comes from the sort below
	for _, ev := range events {
		if ev.Context.Task != taskID {
			continue // defense in depth: the journal query already filters by task
		}
		runID := ev.Context.Run
		if runID == "" {
			continue
		}
		entry := runs[runID]
		if entry == nil {
			entry = &taskRunAgg{}
			runs[runID] = entry
			entry.breakdown = TaskRunBreakdown{RunID: runID, Status: "running", StartedAt: ev.OccurredAt}
		}
		switch ev.Type {
		case "run.started":
			if ev.OccurredAt.Before(entry.breakdown.StartedAt) {
				entry.breakdown.StartedAt = ev.OccurredAt
			}
			entry.breakdown.ParentRunID, _ = ev.Data["parent_run_id"].(string)
			entry.breakdown.AgentID, _ = ev.Data["agent_id"].(string)
		case "run.completed", "run.failed", "run.cancelled":
			// Retries or a cancelled-then-completed race may emit several
			// terminal events; the latest one wins.
			if !ev.OccurredAt.After(entry.terminalAt) {
				continue
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
		}
	}
	agents := map[string]*TaskAgentRollup{}
	for _, entry := range runs {
		b := &entry.breakdown
		if entry.terminal != "" {
			b.Status = entry.terminal
			b.EndedAt = &entry.terminalAt
			b.DurationMS = entry.terminalAt.Sub(b.StartedAt).Milliseconds()
		} else {
			b.DurationMS = now.Sub(b.StartedAt).Milliseconds()
		}
		if b.AgentID == "" {
			b.AgentID = unknownAgentID
		}
		agent := agents[b.AgentID]
		if agent == nil {
			agent = &TaskAgentRollup{AgentID: b.AgentID}
			agents[b.AgentID] = agent
		}
		agent.Runs++
		agent.ModelCalls += b.ModelCalls
		agent.ActiveMS += b.DurationMS
		agent.PromptTokens += b.PromptTokens
		agent.CachedTokens += b.CachedTokens
		agent.CompletionTokens += b.CompletionTokens
		agent.TotalTokens += b.TotalTokens
		agent.CostUSD += b.CostUSD

		if tasks.FirstStartedAt == nil || b.StartedAt.Before(*tasks.FirstStartedAt) {
			tasks.FirstStartedAt = &b.StartedAt
		}
		if b.EndedAt != nil && (tasks.LastEndedAt == nil || b.EndedAt.After(*tasks.LastEndedAt)) {
			tasks.LastEndedAt = b.EndedAt
		}
		tasks.RunCount++
		tasks.ModelCalls += b.ModelCalls
		tasks.ActiveMS += b.DurationMS
		tasks.TotalPromptTokens += b.PromptTokens
		tasks.TotalCachedTokens += b.CachedTokens
		tasks.TotalCompletionTokens += b.CompletionTokens
		tasks.TotalTokens += b.TotalTokens
		tasks.TotalCostUSD += b.CostUSD
		tasks.Runs = append(tasks.Runs, *b)
	}
	// Elapsed is the calendar span so far: while any run is live it keeps
	// growing to now; once everything ended it is the last end - first start.
	if tasks.FirstStartedAt != nil {
		end := now
		if tasks.LastEndedAt != nil && allTerminal(runs) {
			end = *tasks.LastEndedAt
		}
		tasks.ElapsedMS = end.Sub(*tasks.FirstStartedAt).Milliseconds()
	}
	sort.Slice(tasks.Runs, func(i, j int) bool { return tasks.Runs[i].StartedAt.Before(tasks.Runs[j].StartedAt) })
	tasks.Agents = make([]TaskAgentRollup, 0, len(agents))
	for _, agent := range agents {
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

func allTerminal(runs map[string]*taskRunAgg) bool {
	for _, entry := range runs {
		if entry.terminal == "" {
			return false
		}
	}
	return true
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
