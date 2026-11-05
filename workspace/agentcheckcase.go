package workspace

import (
	"context"
	"fmt"
)

// Agent check cases (docs/agent-evolution.md §2.4): the minimalist replacement
// of the retired evaluation suites — an input with pattern expectations,
// usually saved from a production run in one click. «Проверить на примерах» is
// an optional smoke run against a chosen version: an informational signal,
// never a condition of applying anything.

// checkCaseColumns is the projection every check-case read scans — the column
// order and scanAgentCheckCase are position-sensitive.
const checkCaseColumns = `id, agent_id, input, must_contain, must_not_contain, from_run_id, created_by, created_at`

func scanAgentCheckCase(row interface{ Scan(dest ...any) error }) (AgentCheckCase, error) {
	var c AgentCheckCase
	if err := row.Scan(&c.ID, &c.AgentID, &c.Input, &c.MustContain, &c.MustNotContain,
		&c.FromRunID, &c.CreatedBy, &c.CreatedAt); err != nil {
		return AgentCheckCase{}, err
	}
	if c.MustContain == nil {
		c.MustContain = []string{}
	}
	if c.MustNotContain == nil {
		c.MustNotContain = []string{}
	}
	return c, nil
}

// ListAgentCheckCases returns the agent's control examples, oldest first.
func (s *Store) ListAgentCheckCases(ctx context.Context, agentID string) ([]AgentCheckCase, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+checkCaseColumns+` FROM workspace_agent_check_case WHERE agent_id = $1 ORDER BY created_at, id`,
		agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AgentCheckCase{}
	for rows.Next() {
		c, err := scanAgentCheckCase(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// AddAgentCheckCase stores one control example. An empty input is a conflict —
// a case without input cannot be replayed (docs/agent-evolution.md §2.4).
func (s *Store) AddAgentCheckCase(ctx context.Context, c *AgentCheckCase) error {
	if c.Input == "" {
		return fmt.Errorf("%w: check case input is required", ErrConflict)
	}
	if c.MustContain == nil {
		c.MustContain = []string{}
	}
	if c.MustNotContain == nil {
		c.MustNotContain = []string{}
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_agent_check_case
		 (agent_id, input, must_contain, must_not_contain, from_run_id, created_by)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, created_at`,
		c.AgentID, c.Input, textListArg(c.MustContain), textListArg(c.MustNotContain), c.FromRunID, c.CreatedBy).
		Scan(&c.ID, &c.CreatedAt)
}

// DeleteAgentCheckCase removes one control example scoped to its agent; a
// foreign id is a not-found, never someone else's delete.
func (s *Store) DeleteAgentCheckCase(ctx context.Context, agentID string, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM workspace_agent_check_case WHERE id = $1 AND agent_id = $2`, id, agentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
