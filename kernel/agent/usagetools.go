package agent

// usage_report (docs/usage-report.md): the self-monitoring aggregate the
// run's own agent can pull to answer «сколько стоил этот проект», «кто съел
// токены», «какой модели хватит лимита». Read-only fold of the journal via
// the cost API hook; the heavy lifting lives in kernel/cost.

import (
	"context"
	"strings"

	"github.com/temporality-project/temporality/kernel/cost"
)

// usageReportRowLimit caps a tool response; agents rarely need more than the
// top of a dimension, and compactJSON would otherwise eat the tail anyway.
const usageReportRowLimit = 50

// usageReportMaxMonths bounds the window: a tool call never pages through a
// year of history.
const usageReportMaxMonths = 12

func (a *Activities) handleUsageReport(ctx context.Context, request ToolRequest) (ToolResult, error) {
	if a.UsageReport == nil {
		return ToolResult{Content: "error: usage accounting is not available in this installation"}, nil
	}
	if request.Project == "" {
		return ToolResult{Content: "error: project not set"}, nil
	}
	query := cost.UsageQuery{Project: request.Project}
	groupBy, _ := request.Arguments["group_by"].(string)
	query.GroupBy = strings.TrimSpace(groupBy)
	if !cost.ValidUsageGroupBy(query.GroupBy) {
		return ToolResult{Content: "error: group_by must be one of: " + strings.Join(cost.UsageDimensions, ", ")}, nil
	}
	if raw, ok := request.Arguments["project"].(string); ok && strings.TrimSpace(raw) != "" {
		// An explicit project widens the cut beyond the current run's project
		// (the cross-project «куда уходят токены» question).
		query.Project = strings.TrimSpace(raw)
	}
	if months := argInt(request.Arguments, "months"); months > 0 {
		if months > usageReportMaxMonths {
			months = usageReportMaxMonths
		}
		query.Months = months
	}
	if limit := argInt(request.Arguments, "limit"); limit > 0 {
		if limit > usageReportRowLimit {
			limit = usageReportRowLimit
		}
		query.Limit = limit
	} else {
		query.Limit = usageReportRowLimit
	}
	report, err := a.UsageReport(ctx, query)
	if err != nil {
		return ToolResult{Content: "error: " + err.Error()}, nil
	}
	return ToolResult{Content: compactJSON(report, 6000)}, nil
}

// argInt reads a JSON number argument: encoding/json decodes every number
// into float64 before this sees it.
func argInt(arguments map[string]any, key string) int {
	switch n := arguments[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
