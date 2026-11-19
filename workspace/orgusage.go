package workspace

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Monthly org usage accounting (docs/org-structure.md §24, policy monthly
// caps): org_month_usage accumulates tokens/cost/runs per org node per
// calendar month; org_usage_sync carries the model.completed watermark. The
// policy admission gate sums the subtree rows; the usage stats view reads
// the same rows per month.

// OrgUsageWatermark returns the model.completed fold position (zero: never
// folded).
func (s *Store) OrgUsageWatermark(ctx context.Context) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx, `SELECT watermark FROM org_usage_sync WHERE id`).Scan(&at)
	if err != nil {
		if err == pgx.ErrNoRows {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return at, nil
}

// AdvanceOrgUsageSync moves the usage watermark forward (never back).
func (s *Store) AdvanceOrgUsageSync(ctx context.Context, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_usage_sync (id, watermark) VALUES (TRUE, $1)
		 ON CONFLICT (id) DO UPDATE SET watermark = EXCLUDED.watermark
		 WHERE org_usage_sync.watermark < EXCLUDED.watermark`, at)
	return err
}

// AddOrgMonthUsage accumulates one attributed model call into the node's
// monthly row (upsert-add: many calls land in the same month).
func (s *Store) AddOrgMonthUsage(ctx context.Context, orgNodeID, month string, runs, tokens int64, costUSD float64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_month_usage (org_node_id, month, runs, tokens, cost_usd, updated_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (org_node_id, month) DO UPDATE SET
		   runs = org_month_usage.runs + EXCLUDED.runs,
		   tokens = org_month_usage.tokens + EXCLUDED.tokens,
		   cost_usd = org_month_usage.cost_usd + EXCLUDED.cost_usd,
		   updated_at = now()`,
		orgNodeID, month, runs, tokens, costUSD)
	return err
}

// OrgMonthUsageRow is one org node × calendar month cell of the usage table.
type OrgMonthUsageRow struct {
	OrgNodeID string  `json:"org_node_id"`
	Month     string  `json:"month"`
	Runs      int64   `json:"runs"`
	Tokens    int64   `json:"tokens"`
	CostUSD   float64 `json:"cost_usd"`
}

// OrgMonthUsage returns the usage rows for the org nodes and months the
// caller asks for (nodes empty = every node; months are 'YYYY-MM').
func (s *Store) OrgMonthUsage(ctx context.Context, orgNodeIDs []string, months []string) ([]OrgMonthUsageRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT org_node_id, month, runs, tokens, cost_usd
		 FROM org_month_usage
		 WHERE ($1::text[] IS NULL OR org_node_id = ANY ($1::text[]))
		   AND ($2::text[] IS NULL OR month = ANY ($2::text[]))
		 ORDER BY month DESC, org_node_id`,
		orgNodeIDs, months)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrgMonthUsageRow{}
	for rows.Next() {
		var row OrgMonthUsageRow
		if err := rows.Scan(&row.OrgNodeID, &row.Month, &row.Runs, &row.Tokens, &row.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// OrgMonthSubtreeUsage sums the month-to-date usage of an org subtree: the
// monthly policy admission gate compares THIS against the cap the source row
// sets. IDs nil/empty = the whole installation (the installation-wide policy
// row sees everything).
func (s *Store) OrgMonthSubtreeUsage(ctx context.Context, orgNodeIDs []string, month string) (tokens int64, costUSD float64, runs int64, err error) {
	query := `SELECT COALESCE(SUM(tokens), 0), COALESCE(SUM(cost_usd), 0), COALESCE(SUM(runs), 0)
			 FROM org_month_usage WHERE month = $1`
	if len(orgNodeIDs) > 0 {
		query += ` AND org_node_id = ANY ($2::text[])`
		err = s.pool.QueryRow(ctx, query, month, orgNodeIDs).Scan(&tokens, &costUSD, &runs)
	} else {
		err = s.pool.QueryRow(ctx, query, month).Scan(&tokens, &costUSD, &runs)
	}
	return tokens, costUSD, runs, err
}

// OrgUnitSubtreeIDs expands a unit to itself plus every descendant (the
// monthly usage a unit's policy row SEES). Empty id = the whole installation
// (nil ids, no query needed).
func (s *Store) OrgUnitSubtreeIDs(ctx context.Context, unitID string) ([]string, error) {
	if unitID == "" {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`WITH RECURSIVE subtree AS (
			 SELECT id FROM org_unit WHERE id = $1
			 UNION ALL
			 SELECT u.id FROM org_unit u JOIN subtree s ON u.parent_id = s.id
		 )
		 SELECT id FROM subtree`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// OrgNodeForRun resolves the org position a run was attributed to (the
// boards' attribution snapshot). Empty string = unattributed.
func (s *Store) OrgNodeForRun(ctx context.Context, project, runID string) (string, error) {
	var node string
	err := s.pool.QueryRow(ctx,
		`SELECT org_node_id FROM org_active_run WHERE project_id = $1 AND run_id = $2`,
		project, runID).Scan(&node)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return node, nil
}
