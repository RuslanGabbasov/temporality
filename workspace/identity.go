package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Execution identities (docs/org-structure.md §20): the security context of
// automated runs. A trigger's run never inherits its creator's rights — it
// runs under an identity that names the agents, MCP servers, providers,
// projects and human request targets it may touch. Wiring identities into
// trigger runs is wave D; this file only provides the storage.

func unmarshalStringList(raw []byte) []string {
	var v []string
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

func (s *Store) CreateExecutionIdentity(ctx context.Context, e *ExecutionIdentity) error {
	normalizeExecutionIdentity(e)
	if e.OrgUnitID != "" {
		if _, err := s.GetOrgUnit(ctx, e.OrgUnitID); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	e.CreatedAt = now
	e.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO execution_identity
		 (id, name, description, org_unit_id, allowed_agents, allowed_mcp, allowed_providers, allowed_projects, human_targets, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		e.ID, e.Name, e.Description, nullString(e.OrgUnitID),
		marshalStrings(e.AllowedAgents), marshalStrings(e.AllowedMCP),
		marshalStrings(e.AllowedProviders), marshalStrings(e.AllowedProjects),
		marshalStrings(e.HumanTargets), e.CreatedAt, e.UpdatedAt)
	return err
}

// normalizeExecutionIdentity defaults missing allowed-lists to "*": an absent
// JSON field must not read back as "allows nothing".
func normalizeExecutionIdentity(e *ExecutionIdentity) {
	if len(e.AllowedAgents) == 0 {
		e.AllowedAgents = []string{"*"}
	}
	if len(e.AllowedMCP) == 0 {
		e.AllowedMCP = []string{"*"}
	}
	if len(e.AllowedProviders) == 0 {
		e.AllowedProviders = []string{"*"}
	}
	if len(e.AllowedProjects) == 0 {
		e.AllowedProjects = []string{"*"}
	}
	if e.HumanTargets == nil {
		e.HumanTargets = []string{}
	}
}

func (s *Store) GetExecutionIdentity(ctx context.Context, id string) (ExecutionIdentity, error) {
	var e ExecutionIdentity
	var orgUnitID *string
	var agents, mcp, providers, projects, humans []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, org_unit_id, allowed_agents, allowed_mcp, allowed_providers, allowed_projects, human_targets, created_at, updated_at
		 FROM execution_identity WHERE id = $1`, id).
		Scan(&e.ID, &e.Name, &e.Description, &orgUnitID, &agents, &mcp, &providers, &projects, &humans, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.OrgUnitID = derefPtr(orgUnitID)
	e.AllowedAgents = unmarshalStringList(agents)
	e.AllowedMCP = unmarshalStringList(mcp)
	e.AllowedProviders = unmarshalStringList(providers)
	e.AllowedProjects = unmarshalStringList(projects)
	e.HumanTargets = unmarshalStringList(humans)
	return e, nil
}

func (s *Store) ListExecutionIdentities(ctx context.Context) ([]ExecutionIdentity, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, description, org_unit_id, allowed_agents, allowed_mcp, allowed_providers, allowed_projects, human_targets, created_at, updated_at
		 FROM execution_identity ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ExecutionIdentity{}
	for rows.Next() {
		var e ExecutionIdentity
		var orgUnitID *string
		var agents, mcp, providers, projects, humans []byte
		if err := rows.Scan(&e.ID, &e.Name, &e.Description, &orgUnitID, &agents, &mcp, &providers, &projects, &humans, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.OrgUnitID = derefPtr(orgUnitID)
		e.AllowedAgents = unmarshalStringList(agents)
		e.AllowedMCP = unmarshalStringList(mcp)
		e.AllowedProviders = unmarshalStringList(providers)
		e.AllowedProjects = unmarshalStringList(projects)
		e.HumanTargets = unmarshalStringList(humans)
		result = append(result, e)
	}
	return result, rows.Err()
}

// UpdateExecutionIdentity replaces the mutable fields; org re-binding must
// reference an existing unit and the identity itself must exist.
func (s *Store) UpdateExecutionIdentity(ctx context.Context, e ExecutionIdentity) error {
	normalizeExecutionIdentity(&e)
	if e.OrgUnitID != "" {
		if _, err := s.GetOrgUnit(ctx, e.OrgUnitID); err != nil {
			return err
		}
	}
	e.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE execution_identity
		 SET name = $2, description = $3, org_unit_id = $4,
		     allowed_agents = $5, allowed_mcp = $6, allowed_providers = $7,
		     allowed_projects = $8, human_targets = $9, updated_at = $10
		 WHERE id = $1`,
		e.ID, e.Name, e.Description, nullString(e.OrgUnitID),
		marshalStrings(e.AllowedAgents), marshalStrings(e.AllowedMCP),
		marshalStrings(e.AllowedProviders), marshalStrings(e.AllowedProjects),
		marshalStrings(e.HumanTargets), e.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteExecutionIdentity(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM execution_identity WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
