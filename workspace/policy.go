package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Policy storage and restrictive inheritance (docs/org-structure.md §24).
// Policies attach to org units (empty unit = installation-wide) and inherit
// top-down: the effective policy for a run merges every policy bound to the
// run's org-position units (project units, actor unit, trigger unit), each
// expanded to its full ancestor chain.

var ValidPolicyNetworkModes = map[string]bool{"": true, "allow": true, "deny": true}
var ValidPolicySandboxModes = map[string]bool{"": true, "standard": true, "read_only": true}
var ValidPolicyApprovalModes = map[string]bool{"": true, "auto": true, "tools": true}

// ValidatePolicy checks the enum dimensions; the numeric CHECKs live in the
// migration and surface as query errors.
func ValidatePolicy(p Policy) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("policy name is required")
	}
	if !ValidPolicyNetworkModes[p.NetworkMode] {
		return fmt.Errorf("unknown network_mode %q (supported: allow, deny)", p.NetworkMode)
	}
	if !ValidPolicySandboxModes[p.SandboxMode] {
		return fmt.Errorf("unknown sandbox_mode %q (supported: standard, read_only)", p.SandboxMode)
	}
	if !ValidPolicyApprovalModes[p.ApprovalMode] {
		return fmt.Errorf("unknown approval_mode %q (supported: auto, tools)", p.ApprovalMode)
	}
	return nil
}

const policyColumns = `id, org_unit_id, name, allowed_models, allowed_mcp,
max_tokens, max_budget_usd, timeout_seconds, network_mode, sandbox_mode, approval_mode,
enabled, created_at, updated_at`

func scanPolicy(scanner interface{ Scan(dest ...any) error }) (Policy, error) {
	var p Policy
	var orgUnitID *string
	var models, mcp []byte
	var maxTokens, timeout *int
	var budget *float64
	err := scanner.Scan(&p.ID, &orgUnitID, &p.Name, &models, &mcp,
		&maxTokens, &budget, &timeout, &p.NetworkMode, &p.SandboxMode, &p.ApprovalMode,
		&p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return p, err
	}
	p.OrgUnitID = derefPtr(orgUnitID)
	p.AllowedModels = unmarshalStringList(models)
	p.AllowedMCP = unmarshalStringList(mcp)
	p.MaxTokens = maxTokens
	p.MaxBudgetUSD = budget
	p.TimeoutSeconds = timeout
	return p, nil
}

func (s *Store) CreatePolicy(ctx context.Context, p *Policy) error {
	if err := ValidatePolicy(*p); err != nil {
		return err
	}
	normalizePolicyLists(p)
	if p.OrgUnitID != "" {
		if _, err := s.GetOrgUnit(ctx, p.OrgUnitID); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_policy
		 (id, org_unit_id, name, allowed_models, allowed_mcp, max_tokens, max_budget_usd,
		  timeout_seconds, network_mode, sandbox_mode, approval_mode, enabled, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		p.ID, nullString(p.OrgUnitID), p.Name, marshalStrings(p.AllowedModels), marshalStrings(p.AllowedMCP),
		p.MaxTokens, p.MaxBudgetUSD, p.TimeoutSeconds, p.NetworkMode, p.SandboxMode, p.ApprovalMode,
		p.Enabled, p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *Store) GetPolicy(ctx context.Context, id string) (Policy, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+policyColumns+` FROM org_policy WHERE id = $1`, id)
	p, err := scanPolicy(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// ListPolicies returns every policy row; the UI groups them by unit.
func (s *Store) ListPolicies(ctx context.Context) ([]Policy, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+policyColumns+` FROM org_policy ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Policy{}
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) UpdatePolicy(ctx context.Context, p Policy) error {
	if err := ValidatePolicy(p); err != nil {
		return err
	}
	normalizePolicyLists(&p)
	if p.OrgUnitID != "" {
		if _, err := s.GetOrgUnit(ctx, p.OrgUnitID); err != nil {
			return err
		}
	}
	p.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE org_policy
		 SET org_unit_id = $2, name = $3, allowed_models = $4, allowed_mcp = $5,
		     max_tokens = $6, max_budget_usd = $7, timeout_seconds = $8,
		     network_mode = $9, sandbox_mode = $10, approval_mode = $11,
		     enabled = $12, updated_at = $13
		 WHERE id = $1`,
		p.ID, nullString(p.OrgUnitID), p.Name, marshalStrings(p.AllowedModels), marshalStrings(p.AllowedMCP),
		p.MaxTokens, p.MaxBudgetUSD, p.TimeoutSeconds, p.NetworkMode, p.SandboxMode, p.ApprovalMode,
		p.Enabled, p.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeletePolicy(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM org_policy WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// normalizePolicyLists maps an empty submitted allowlist to nil so it reads
// back as "no restriction" — the empty non-nil form is reserved for
// intersections that blocked everything.
func normalizePolicyLists(p *Policy) {
	if len(p.AllowedModels) == 0 {
		p.AllowedModels = nil
	}
	if len(p.AllowedMCP) == 0 {
		p.AllowedMCP = nil
	}
}

// MergePolicies folds policy rows into their restrictive combination
// (docs/org-structure.md §24): allowlists intersect, caps take the minimum,
// restrictive mode values win. A child can never loosen what an ancestor set.
// The returned effective policy carries the merged dimensions; sources lists
// the contributing rows in input order.
func MergePolicies(rows []Policy) (effective Policy, sources []Policy) {
	effective = Policy{AllowedModels: nil, AllowedMCP: nil, Enabled: true}
	// Non-nil from the start: JSON marshalling of a nil slice yields null,
	// which clients reading "sources": [...] must not have to handle.
	sources = make([]Policy, 0, len(rows))
	firstModel := true
	firstMCP := true
	for _, p := range rows {
		if !p.Enabled {
			continue
		}
		sources = append(sources, p)
		// A bare "*" list does not restrict anything, so it never narrows the
		// merge — only concrete names do.
		if models := concreteList(p.AllowedModels); len(models) > 0 {
			if firstModel {
				effective.AllowedModels = models
				firstModel = false
			} else {
				effective.AllowedModels = intersectLists(effective.AllowedModels, models)
			}
		}
		if mcp := concreteList(p.AllowedMCP); len(mcp) > 0 {
			if firstMCP {
				effective.AllowedMCP = mcp
				firstMCP = false
			} else {
				effective.AllowedMCP = intersectLists(effective.AllowedMCP, mcp)
			}
		}
		effective.MaxTokens = minIntPtr(effective.MaxTokens, p.MaxTokens)
		effective.MaxBudgetUSD = minFloatPtr(effective.MaxBudgetUSD, p.MaxBudgetUSD)
		effective.TimeoutSeconds = minIntPtr(effective.TimeoutSeconds, p.TimeoutSeconds)
		if p.NetworkMode == "deny" {
			effective.NetworkMode = "deny"
		}
		if p.SandboxMode == "read_only" {
			effective.SandboxMode = "read_only"
		}
		if p.ApprovalMode == "tools" {
			effective.ApprovalMode = "tools"
		}
	}
	return effective, sources
}

// concreteList drops "*" entries: a list that only contains stars does not
// restrict, so it contributes nothing to the merge. It returns a copy so the
// source row is never mutated.
func concreteList(list []string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != "*" {
			out = append(out, v)
		}
	}
	return out
}

func intersectLists(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	out := make([]string, 0, len(a))
	for _, v := range a {
		if set[v] {
			out = append(out, v)
		}
	}
	return out
}

func minIntPtr(a, b *int) *int {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if *b < *a {
		return b
	}
	return a
}

func minFloatPtr(a, b *float64) *float64 {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if *b < *a {
		return b
	}
	return a
}

// EffectivePolicy resolves the policies that govern a run at the given org
// position: each unit id is expanded to its ancestor chain (the unit itself
// plus ancestors — inheritance flows top-down), the installation-wide rows
// (empty org unit) always apply, and everything merges restrictively.
// Duplicate units and overlapping chains are fine: rows dedupe by id before
// the merge.
func (s *Store) EffectivePolicy(ctx context.Context, unitIDs ...string) (Policy, []Policy, error) {
	// Collect the union of ancestor chains (self included).
	visible := map[string]bool{}
	for _, id := range unitIDs {
		if id == "" {
			continue
		}
		chain, err := s.UnitChain(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue // stale reference: ignore rather than fail the run
			}
			return Policy{}, nil, err
		}
		for _, u := range chain {
			visible[u] = true
		}
	}
	rows, err := s.ListPolicies(ctx)
	if err != nil {
		return Policy{}, nil, err
	}
	applicable := make([]Policy, 0, len(rows))
	for _, p := range rows {
		if p.OrgUnitID == "" || visible[p.OrgUnitID] {
			applicable = append(applicable, p)
		}
	}
	// Deterministic order: installation-wide first, then by creation — the
	// sources list in the UI reads top-down this way.
	sortPolicies(applicable)
	effective, sources := MergePolicies(applicable)
	return effective, sources, nil
}

func sortPolicies(rows []Policy) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			left, right := rows[j-1], rows[j]
			// global rows first
			if left.OrgUnitID == "" && right.OrgUnitID != "" {
				break
			}
			if left.OrgUnitID != "" && right.OrgUnitID == "" {
				rows[j-1], rows[j] = rows[j], rows[j-1]
				continue
			}
			if left.CreatedAt.After(right.CreatedAt) {
				rows[j-1], rows[j] = rows[j], rows[j-1]
				continue
			}
			break
		}
	}
}
