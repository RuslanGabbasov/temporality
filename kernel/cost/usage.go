package cost

// Usage aggregates for self-monitoring and reporting: one fold attributes
// tokens, money (the kernel price config) and human attention (HAB/HWT) to
// org units, employees, projects, sagas, models and providers. The saga
// resolution, the approval identity and the HAB formula are the budget
// fold's own (sagaBindings, approvalAccumulator, humanStats), so every cut
// of the same window tells the same story the Sagas tab does.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/observation"
)

// Usage dimensions (usage_report group_by values).
const (
	UsageGroupOrgUnit  = "org_unit"
	UsageGroupEmployee = "employee"
	UsageGroupProject  = "project"
	UsageGroupSaga     = "saga"
	UsageGroupModel    = "model"
	UsageGroupProvider = "provider"
)

// UsageDimensions is the canonical dimension list — the monthly report walks
// all of them, the tool validates its group_by against this set.
var UsageDimensions = []string{
	UsageGroupOrgUnit, UsageGroupEmployee, UsageGroupProject,
	UsageGroupSaga, UsageGroupModel, UsageGroupProvider,
}

// ValidUsageGroupBy reports whether the dimension is one of the six cuts.
func ValidUsageGroupBy(groupBy string) bool {
	for _, dim := range UsageDimensions {
		if groupBy == dim {
			return true
		}
	}
	return false
}

// usageRunEvents are the run-scoped types the fold needs: call economics,
// run identity/attribution, approvals and answers. Fetched with the window's
// since so a month scan does not page through history.
var usageRunEvents = []string{
	"run.started", "run.completed", "run.failed", "run.cancelled",
	"model.completed", "agent.summary",
	"approval.requested", "approval.granted", "approval.rejected",
	"approval.timed_out", "approval.auto_granted", "human.answered",
}

// usageSagaEvents ride UNBOUNDED fetches on purpose: a binding registered
// long before the window still attributes the window's runs.
var usageSagaEvents = []string{
	"saga.registered", "saga.attached", "saga.detached",
	"saga.renamed", "saga.merged", "saga.deleted",
}

// UsageQuery selects one aggregate cut. Months (default 1) walks back from
// today month-by-month, so 3 = from the first day of two months ago to now;
// explicit Since/Until override the derivation.
type UsageQuery struct {
	GroupBy string
	Project string
	Months  int
	Since   *time.Time
	Until   *time.Time
	Limit   int
}

// UsageRow is one slice of the window: token and money totals plus the
// human-attention rollup (run-level dimensions only — a model call has no
// approval of its own).
type UsageRow struct {
	GroupBy          string  `json:"group_by"`
	Key              string  `json:"key"`
	Label            string  `json:"label"`
	Runs             int     `json:"runs"`
	ModelCalls       int     `json:"model_calls"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	Decisions        int     `json:"decisions"`
	Answers          int     `json:"answers"`
	HABSeconds       int64   `json:"hab_seconds"`
	HWTSeconds       int64   `json:"hwt_seconds"`
	FlowEfficiency   int     `json:"flow_efficiency"`
}

// UsageReport is the answer to one UsageQuery: the sorted rows (cost desc)
// plus the window's grand total.
type UsageReport struct {
	GroupBy     string     `json:"group_by"`
	Project     string     `json:"project,omitempty"`
	Since       time.Time  `json:"since"`
	Until       time.Time  `json:"until"`
	GeneratedAt time.Time  `json:"generated_at"`
	Limit       int        `json:"limit,omitempty"`
	Rows        []UsageRow `json:"rows"`
	Total       UsageRow   `json:"total"`
}

// UsageResolvers turn raw attribution keys into human labels; nil resolvers
// keep raw keys, resolver errors fall back to prefixed raw keys — a missing
// name must never lose a row.
type UsageResolvers struct {
	OrgNodeForRun func(ctx context.Context, project, runID string) (string, error)
	OrgNodeName   func(ctx context.Context, orgNodeID string) (string, error)
	UserName      func(ctx context.Context, userID string) (string, error)
}

// SetUsageResolvers installs the label resolvers (workspace org/user names).
// Set once at boot, next to SetHumanFactors.
func (a *CostAPI) SetUsageResolvers(r UsageResolvers) {
	a.usageResolvers = r
}

// usageRunMeta is what the fold knows about one run: the owning task (the
// saga binding rides it) and the actor the run is attributed to.
type usageRunMeta struct {
	task  string
	actor string
}

// usageSlice accumulates one slice of the window: run economics, the run id
// set (distinct run counting) and the human-attention accumulator. The same
// type serves run-level slices (with approvals) and call-level ones (models,
// providers — approvals never ingested there).
type usageSlice struct {
	runs             map[string]struct{}
	modelCalls       int
	promptTokens     int64
	cachedTokens     int64
	completionTokens int64
	costUSD          float64
	approvals        approvalAccumulator
	answerWords      int
}

func newUsageSlice() *usageSlice { return &usageSlice{runs: map[string]struct{}{}} }

func (s *usageSlice) addCall(runID string, prompt, cached, comp int64, usd float64) {
	if runID != "" {
		s.runs[runID] = struct{}{}
	}
	s.modelCalls++
	s.promptTokens += prompt
	s.cachedTokens += cached
	s.completionTokens += comp
	s.costUSD += usd
}

// UsageReport is the CostAPI entry: validate the dimension, derive the
// window, fetch the window's events (parallel per type) plus unbounded saga
// history, fold.
func (a *CostAPI) UsageReport(ctx context.Context, q UsageQuery) (UsageReport, error) {
	if !ValidUsageGroupBy(q.GroupBy) {
		return UsageReport{}, fmt.Errorf("group_by must be one of: %s", strings.Join(UsageDimensions, ", "))
	}
	now := a.now().UTC()
	since, until := usageWindow(q, now)
	sinceAt := since
	events, err := a.queryEventsFilteredParallelSince(ctx, q.Project, &sinceAt, usageRunEvents...)
	if err != nil {
		return UsageReport{}, err
	}
	sagaEvents, err := a.queryEventsFilteredParallel(ctx, q.Project, "", "", usageSagaEvents...)
	if err != nil {
		return UsageReport{}, err
	}
	return a.foldUsage(ctx, q, since, until, now, events, sagaEvents), nil
}

// usageWindow derives the UTC window: Since/Until win when present, otherwise
// Months months back from now, month-aligned (1 = month to date). Until
// never lands before Since.
func usageWindow(q UsageQuery, now time.Time) (since, until time.Time) {
	if q.Since != nil && !q.Since.IsZero() {
		since = q.Since.UTC()
	} else {
		months := q.Months
		if months <= 0 {
			months = 1
		}
		anchor := now.AddDate(0, -(months - 1), 0)
		since = time.Date(anchor.Year(), anchor.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	if q.Until != nil && !q.Until.IsZero() && q.Until.After(since) {
		until = q.Until.UTC()
	} else {
		until = now
	}
	return since, until
}

// foldUsage is the pure part of the aggregate (unit-testable): events in,
// one sorted report out. Chronological order matters for the approval
// pending map, so the merged feed is sorted first — the per-type fetches are
// type-blocked, not globally chronological.
func (a *CostAPI) foldUsage(ctx context.Context, q UsageQuery, since, until, now time.Time, events, sagaEvents []observation.Event) UsageReport {
	sorted := make([]observation.Event, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].OccurredAt.Before(sorted[j].OccurredAt) })
	bindings, sagas := sagaBindings(sagaEvents)

	runLevel := q.GroupBy != UsageGroupModel && q.GroupBy != UsageGroupProvider
	runSlices := map[string]*usageSlice{}
	callSlices := map[string]*usageSlice{}
	total := newUsageSlice()
	runMeta := map[string]*usageRunMeta{}
	orgNodeCache := map[string]string{}
	project := q.Project

	sliceFor := func(key string) *usageSlice {
		slice := runSlices[key]
		if slice == nil {
			slice = newUsageSlice()
			runSlices[key] = slice
		}
		return slice
	}
	// Call-level keys are model/provider names; an empty name is its own
	// "unknown" bucket. Run-level keys keep "" — the unattributed bucket.
	callSliceFor := func(key string) *usageSlice {
		if key == "" {
			key = "unknown"
		}
		slice := callSlices[key]
		if slice == nil {
			slice = newUsageSlice()
			callSlices[key] = slice
		}
		return slice
	}
	metaFor := func(runID string) *usageRunMeta {
		meta := runMeta[runID]
		if meta == nil {
			meta = &usageRunMeta{}
			runMeta[runID] = meta
		}
		return meta
	}
	orgNodeFor := func(project, runID string) string {
		cacheKey := project + "\x00" + runID
		if node, ok := orgNodeCache[cacheKey]; ok {
			return node
		}
		node := ""
		if resolvers := a.usageResolvers; resolvers.OrgNodeForRun != nil {
			if resolved, err := resolvers.OrgNodeForRun(ctx, project, runID); err == nil {
				node = resolved
			}
		}
		orgNodeCache[cacheKey] = node
		return node
	}
	runKeyFor := func(dim string, meta *usageRunMeta, runID string) string {
		switch dim {
		case UsageGroupEmployee:
			return meta.actor
		case UsageGroupOrgUnit:
			return orgNodeFor(project, runID)
		case UsageGroupProject:
			return project
		case UsageGroupSaga:
			if sagaID, ok := bindings[meta.task]; ok && sagaID != "" {
				return sagaID
			}
			return "unbound"
		}
		return "unknown"
	}

	for i := range sorted {
		ev := &sorted[i]
		if ev.OccurredAt.Before(since) || !ev.OccurredAt.Before(until) {
			continue
		}
		runID := ev.Context.Run
		if runID == "" {
			continue // run-scoped fold: events without a run have no attribution
		}
		meta := metaFor(runID)
		if meta.task == "" {
			meta.task = ev.Context.Task
		}
		switch ev.Type {
		case "run.started":
			// The run starter owns the run's tokens; a human actor on a later
			// event (approver, answerer) must not re-attribute them.
			if actor := ev.Context.Actor.ID; actor != "" {
				meta.actor = actor
			}
		default:
			if meta.actor == "" {
				if actor := ev.Context.Actor.ID; actor != "" && ev.Context.Actor.Type != "system" {
					meta.actor = actor
				}
			}
		}
		if ev.Context.Project != "" {
			project = ev.Context.Project
		}
		// Runs count on every event: a started-but-call-less run still
		// consumed human attention (approvals, answers).
		runSlice := sliceFor(runKeyFor(q.GroupBy, meta, runID))
		runSlice.runs[runID] = struct{}{}
		total.runs[runID] = struct{}{}
		switch ev.Type {
		case "model.completed":
			model, _ := ev.Data["model"].(string)
			provider, _ := ev.Data["provider"].(string)
			prompt := int64(toInt(ev.Data["prompt_tokens"]))
			cached := int64(toInt(ev.Data["cached_tokens"]))
			comp := int64(toInt(ev.Data["completion_tokens"]))
			usd := a.config.ModelCost(model, int(prompt), int(cached), int(comp))
			runSlice.addCall(runID, prompt, cached, comp, usd)
			total.addCall(runID, prompt, cached, comp, usd)
			if q.GroupBy == UsageGroupModel {
				callSliceFor(model).addCall(runID, prompt, cached, comp, usd)
			}
			if q.GroupBy == UsageGroupProvider {
				callSliceFor(provider).addCall(runID, prompt, cached, comp, usd)
			}
		case "agent.summary":
			if answer, _ := ev.Data["answer"].(string); answer != "" {
				words := len(strings.Fields(answer))
				runSlice.answerWords += words
				total.answerWords += words
			}
		case "approval.requested", "approval.granted", "approval.rejected",
			"approval.timed_out", "approval.auto_granted", "human.answered":
			runSlice.approvals.ingest(runID, *ev)
			total.approvals.ingest(runID, *ev)
		}
	}

	labels := map[string]string{}
	labelFor := func(key string) string {
		if label, ok := labels[key]; ok {
			return label
		}
		label := usageLabelFor(q.GroupBy, key, sagas, &a.usageResolvers, ctx)
		labels[key] = label
		return label
	}

	var rows []UsageRow
	index := runSlices
	if !runLevel {
		index = callSlices
	}
	for key, slice := range index {
		row := usageRowFrom(q.GroupBy, key, labelFor(key), slice)
		if runLevel {
			human := a.humanStats(&slice.approvals, int(slice.completionTokens), slice.answerWords)
			row.Decisions = human.Decisions
			row.Answers = human.Answers
			row.HABSeconds = human.HABSeconds
			row.HWTSeconds = human.HWTSeconds
			row.FlowEfficiency = human.FlowEfficiency
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CostUSD != rows[j].CostUSD {
			return rows[i].CostUSD > rows[j].CostUSD
		}
		return rows[i].Key < rows[j].Key
	})
	if q.Limit > 0 && len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}

	totalRow := usageRowFrom("total", "total", "total", total)
	totalHuman := a.humanStats(&total.approvals, int(total.completionTokens), total.answerWords)
	totalRow.Decisions = totalHuman.Decisions
	totalRow.Answers = totalHuman.Answers
	totalRow.HABSeconds = totalHuman.HABSeconds
	totalRow.HWTSeconds = totalHuman.HWTSeconds
	totalRow.FlowEfficiency = totalHuman.FlowEfficiency
	return UsageReport{
		GroupBy:     q.GroupBy,
		Project:     q.Project,
		Since:       since,
		Until:       until,
		GeneratedAt: now,
		Limit:       q.Limit,
		Rows:        rows,
		Total:       totalRow,
	}
}

// usageRowFrom materializes the token/money part of a slice.
func usageRowFrom(dim, key, label string, s *usageSlice) UsageRow {
	return UsageRow{
		GroupBy:          dim,
		Key:              key,
		Label:            label,
		Runs:             len(s.runs),
		ModelCalls:       s.modelCalls,
		PromptTokens:     s.promptTokens,
		CachedTokens:     s.cachedTokens,
		CompletionTokens: s.completionTokens,
		TotalTokens:      s.promptTokens + s.completionTokens,
		CostUSD:          s.costUSD,
	}
}

// usageLabelFor resolves a row label: resolver names for people and org
// units, saga titles from the lifecycle events, raw keys elsewhere. A
// missing name degrades to a prefixed raw key — never a lost row.
func usageLabelFor(dim, key string, sagas map[string]*sagaMeta, resolvers *UsageResolvers, ctx context.Context) string {
	switch dim {
	case UsageGroupOrgUnit:
		if key == "" {
			return "unattributed"
		}
		if resolvers != nil && resolvers.OrgNodeName != nil {
			if name, err := resolvers.OrgNodeName(ctx, key); err == nil && name != "" {
				return name
			}
		}
		return "org:" + key
	case UsageGroupEmployee:
		if key == "" {
			return "unknown"
		}
		if resolvers != nil && resolvers.UserName != nil {
			if name, err := resolvers.UserName(ctx, key); err == nil && name != "" {
				return name
			}
		}
		return "user:" + key
	case UsageGroupSaga:
		if key == "unbound" {
			return key
		}
		if meta := sagas[key]; meta != nil && meta.title != "" {
			return meta.title
		}
		return "saga:" + key
	case UsageGroupModel, UsageGroupProvider, UsageGroupProject:
		if key == "" {
			return "unknown"
		}
		return key
	}
	return key
}

// UsageReportHandler serves GET /v1/agent/usage: group_by (required), project,
// months (1-12), since/until (RFC3339), limit (default 50, cap 200).
func (a *CostAPI) UsageReportHandler(w http.ResponseWriter, r *http.Request) {
	q := UsageQuery{
		GroupBy: r.URL.Query().Get("group_by"),
		Project: r.URL.Query().Get("project"),
	}
	if months, err := strconv.Atoi(r.URL.Query().Get("months")); err == nil && months > 0 {
		if months > 12 {
			months = 12
		}
		q.Months = months
	}
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, 400, "since must be RFC3339")
			return
		}
		q.Since = &parsed
	}
	if raw := r.URL.Query().Get("until"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, 400, "until must be RFC3339")
			return
		}
		q.Until = &parsed
	}
	if limit, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && limit > 0 {
		if limit > 200 {
			limit = 200
		}
		q.Limit = limit
	}
	report, err := a.UsageReport(r.Context(), q)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, report)
}

// queryEventsFilteredParallelSince is queryEventsFilteredParallel with a
// window bound: the fold is order-tolerant, so merged per-type feeds (each
// paged with since) aggregate identically to the sequential variant — while
// a month scan stops paging at the window edge instead of all history.
func (a *CostAPI) queryEventsFilteredParallelSince(ctx context.Context, project string, since *time.Time, eventTypes ...string) ([]observation.Event, error) {
	perType := make([][]observation.Event, len(eventTypes))
	indices := make(chan int)
	errs := make(chan error, len(eventTypes))
	workers := budgetFetchWorkers
	if workers > len(eventTypes) {
		workers = len(eventTypes)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range indices {
				if err := a.queryOneType(ctx, project, "", "", eventTypes[index], &perType[index], since); err != nil {
					errs <- err
				}
			}
		}()
	}
	for index := range eventTypes {
		indices <- index
	}
	close(indices)
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return nil, err
	}
	var all []observation.Event
	for _, part := range perType {
		all = append(all, part...)
	}
	return all, nil
}
