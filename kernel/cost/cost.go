// Package cost provides model-call cost accounting. Prices are configured
// via KERNEL_MODEL_PRICES as "model:prompt_per_1k:completion_per_1k" comma
// entries. Costs are derived from token counts in model.completed events;
// no new persistent storage is needed.
package cost

import (
	"encoding/json"
	"fmt"
	"net/http"
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

// ModelCost returns the cost in dollars for a model call.
func (c Config) ModelCost(model string, promptTokens, completionTokens int) float64 {
	price, ok := c.prices[model]
	if !ok {
		return 0
	}
	return (float64(promptTokens)/1000)*price.PromptPer1k + (float64(completionTokens)/1000)*price.CompPer1k
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
	CompletionTokens int       `json:"completion_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	At               time.Time `json:"at"`
}

// RunCost is the aggregated cost for a run.
type RunCost struct {
	RunID                 string     `json:"run_id"`
	Project               string     `json:"project"`
	TotalCostUSD          float64    `json:"total_cost_usd"`
	TotalPromptTokens     int        `json:"total_prompt_tokens"`
	TotalCompletionTokens int        `json:"total_completion_tokens"`
	CallCount             int        `json:"call_count"`
	Calls                 []CallCost `json:"calls,omitempty"`
}

// ProjectCost is the aggregated cost for a project.
type ProjectCost struct {
	Project               string    `json:"project"`
	TotalCostUSD          float64   `json:"total_cost_usd"`
	TotalPromptTokens     int       `json:"total_prompt_tokens"`
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
}

// NewCostAPI creates a cost API handler.
func NewCostAPI(config Config, journalURL string, token string) *CostAPI {
	return &CostAPI{
		config:     config,
		http:       &http.Client{Timeout: 10 * time.Second},
		journalURL: journalURL,
		token:      token,
	}
}

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

func (a *CostAPI) queryEvents(r *http.Request, project, runID string) ([]observation.Event, error) {
	var all []observation.Event
	cursor := ""
	for {
		url := fmt.Sprintf("%s/v1/observations/events?project=%s&type=model.completed&limit=500", a.journalURL, project)
		if runID != "" {
			url += "&run=" + runID
		}
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		req, err := http.NewRequestWithContext(r.Context(), "GET", url, nil)
		if err != nil {
			return nil, err
		}
		if a.token != "" {
			req.Header.Set("Authorization", "Bearer "+a.token)
		}
		resp, err := a.http.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var page struct {
			Events     []observation.Event `json:"events"`
			NextCursor string              `json:"next_cursor"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			return nil, err
		}
		all = append(all, page.Events...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return all, nil
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
		comp := toInt(ev.Data["completion_tokens"])
		provider, _ := ev.Data["provider"].(string)
		costUSD := a.config.ModelCost(model, prompt, comp)
		cc := CallCost{
			RunID:            runID,
			Model:            model,
			Provider:         provider,
			PromptTokens:     prompt,
			CompletionTokens: comp,
			CostUSD:          costUSD,
			At:               ev.OccurredAt,
		}
		cost.Calls = append(cost.Calls, cc)
		cost.TotalCostUSD += costUSD
		cost.TotalPromptTokens += prompt
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
		comp := toInt(ev.Data["completion_tokens"])
		provider, _ := ev.Data["provider"].(string)
		costUSD := a.config.ModelCost(model, prompt, comp)
		cc := CallCost{
			RunID:            runID,
			Model:            model,
			Provider:         provider,
			PromptTokens:     prompt,
			CompletionTokens: comp,
			CostUSD:          costUSD,
			At:               ev.OccurredAt,
		}
		byRun[runID].Calls = append(byRun[runID].Calls, cc)
		byRun[runID].TotalCostUSD += costUSD
		byRun[runID].TotalPromptTokens += prompt
		byRun[runID].TotalCompletionTokens += comp
		byRun[runID].CallCount++
	}
	for _, rc := range byRun {
		pc.Runs = append(pc.Runs, *rc)
		pc.TotalCostUSD += rc.TotalCostUSD
		pc.TotalPromptTokens += rc.TotalPromptTokens
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
