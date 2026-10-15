package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Agent evolution proposals (docs/plan-evaluable-agent.md §2.2 stage 10).
// Mirrors the living-skills proposal anatomy: an observed problem, a proposed
// change, the expected effect and evidence refs. A proposal never mutates the
// agent: it lands as pending and is applied by a human through the regular
// update path (version++, immutable snapshot).

// AgentProposal is one pending definition change for an agent.
type AgentProposal struct {
	ID             int64           `json:"id"`
	AgentID        string          `json:"agent_id"`
	BaseVersion    int             `json:"base_version"`
	Definition     AgentDefinition `json:"definition"`
	Description    string          `json:"description"`
	Problem        string          `json:"problem"`
	Change         string          `json:"change"`
	Effect         string          `json:"effect"`
	Evidence       []string        `json:"evidence"`
	Status         string          `json:"status"` // pending | applied | rejected
	Author         string          `json:"author"`
	GeneratorModel string          `json:"generator_model,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	DecidedAt      *time.Time      `json:"decided_at,omitempty"`
	DecidedBy      string          `json:"decided_by,omitempty"`
}

// CreateAgentProposal stores a pending proposal. The definition is the full
// target definition (callers default it to the agent's current one); the
// server, never the client, records the base version it was derived from.
func (s *Store) CreateAgentProposal(ctx context.Context, p *AgentProposal) error {
	if strings.TrimSpace(p.Problem) == "" || strings.TrimSpace(p.Change) == "" {
		return fmt.Errorf("%w: problem and change are required", ErrConflict)
	}
	if p.Status == "" {
		p.Status = "pending"
	}
	if p.Evidence == nil {
		p.Evidence = []string{}
	}
	definition, err := json.Marshal(p.Definition)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(p.Evidence)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx,
		`INSERT INTO workspace_agent_proposal
		 (agent_id, base_version, definition, description, problem, change, effect, evidence, status, author, generator_model)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id, created_at`,
		p.AgentID, p.BaseVersion, definition, p.Description, p.Problem, p.Change, p.Effect,
		evidence, p.Status, p.Author, p.GeneratorModel).
		Scan(&p.ID, &p.CreatedAt)
}

// ListAgentProposals returns proposals for an agent, newest first; an empty
// status filter returns every status so history stays visible after deciding.
func (s *Store) ListAgentProposals(ctx context.Context, agentID, status string, limit int) ([]AgentProposal, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT id, agent_id, base_version, definition, description, problem, change, effect,
	                 evidence, status, author, generator_model, created_at, decided_at, decided_by
	          FROM workspace_agent_proposal WHERE agent_id = $1`
	args := []any{agentID}
	if status != "" {
		query += ` AND status = $2`
		args = append(args, status)
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AgentProposal
	for rows.Next() {
		p, err := scanAgentProposal(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *p)
	}
	return result, rows.Err()
}

// PendingProposalCounts returns the number of pending proposals per agent
// id. Backs the agents-list badge: a human must see where evolution decisions
// are waiting without opening every profile.
func (s *Store) PendingProposalCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT agent_id, count(*) FROM workspace_agent_proposal WHERE status = 'pending' GROUP BY agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var agentID string
		var count int
		if err := rows.Scan(&agentID, &count); err != nil {
			return nil, err
		}
		counts[agentID] = count
	}
	return counts, rows.Err()
}

// GetAgentProposal loads one proposal scoped to its agent.
func (s *Store) GetAgentProposal(ctx context.Context, agentID string, id int64) (AgentProposal, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, agent_id, base_version, definition, description, problem, change, effect,
		        evidence, status, author, generator_model, created_at, decided_at, decided_by
		 FROM workspace_agent_proposal WHERE id = $1 AND agent_id = $2`, id, agentID)
	p, err := scanAgentProposal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentProposal{}, ErrNotFound
	}
	if err != nil {
		return AgentProposal{}, err
	}
	return *p, nil
}

// DecideAgentProposal transitions a pending proposal to applied/rejected.
// Only pending rows can be decided — a second apply attempt is a conflict,
// which is what guards the version bump against double-apply.
func (s *Store) DecideAgentProposal(ctx context.Context, id int64, status, decidedBy string) (AgentProposal, error) {
	if status != "applied" && status != "rejected" {
		return AgentProposal{}, fmt.Errorf("%w: status must be applied or rejected", ErrConflict)
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE workspace_agent_proposal
		 SET status = $2, decided_at = now(), decided_by = $3
		 WHERE id = $1 AND status = 'pending'
		 RETURNING id, agent_id, base_version, definition, description, problem, change, effect,
		           evidence, status, author, generator_model, created_at, decided_at, decided_by`,
		id, status, decidedBy)
	p, err := scanAgentProposal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the row does not exist or it is no longer pending.
		var existing string
		if scanErr := s.pool.QueryRow(ctx,
			`SELECT status FROM workspace_agent_proposal WHERE id = $1`, id).Scan(&existing); scanErr != nil {
			return AgentProposal{}, ErrNotFound
		}
		return AgentProposal{}, ErrConflict
	}
	if err != nil {
		return AgentProposal{}, err
	}
	return *p, nil
}

// ErrBaseVersionStale reports specs/quality-loop.md §5.5/§10.1b: the agent's
// definition version moved past the proposal's base version, so the proposal
// must be rebased instead of applied over a stale base.
var ErrBaseVersionStale = errors.New("base_version_stale")

// ApplyAgentProposal claims a pending proposal and applies its target
// definition to the agent in one transaction — the TOCTOU fix of §10.1a.
// The conditional claim (status 'pending' → 'applied') is the transaction's
// first statement: of two concurrent applies exactly one wins, the loser
// blocks on the proposal row lock, re-reads it after the winner commits,
// fails the predicate and rolls back with ErrConflict before a single agent
// column is touched — UpdateAgent + version++ can never run twice. The agent
// row is then locked and its definition_version checked against the
// proposal's base (§5.5): a version bump that raced the handler's load
// surfaces as ErrBaseVersionStale, never as a silent apply over a stale base.
// The immutable version snapshot rides the same transaction, so the apply
// lands whole (proposal decided + agent updated + snapshot written) or not
// at all. Its transactional body is shared with ApplyAgentProposalAudited.
func (s *Store) ApplyAgentProposal(ctx context.Context, next Agent, proposalID int64, decidedBy string, snapshot *AgentVersion) (AgentProposal, error) {
	return s.applyAgentProposal(ctx, next, proposalID, decidedBy, snapshot, nil)
}

// ApplyAgentProposalAudited is ApplyAgentProposal with the access-audit
// record of the apply written inside the same transaction (F-2,
// specs/quality-loop.md §8.3 gate:false): claim + version++ + snapshot +
// audit land atomically — a failed audit INSERT rolls the whole apply back,
// so a gate-less apply can never go through unjournaled. This is the
// fail-closed counterpart of the best-effort RecordAccessAudit paths: here
// the audit record is part of the contract, so its failure must not be
// survivable; it surfaces as ErrAuditWriteFailed so the kernel answers 503
// instead of a success that lost the bypass trail (CWE-778). A nil audit
// event is a programming error and is refused outright — silently degrading
// to an unjournaled apply would recreate the hole this closes.
func (s *Store) ApplyAgentProposalAudited(ctx context.Context, next Agent, proposalID int64, decidedBy string, snapshot *AgentVersion, audit *AccessAuditEvent) (AgentProposal, error) {
	if audit == nil {
		return AgentProposal{}, errors.New("audited apply requires a non-nil audit event")
	}
	return s.applyAgentProposal(ctx, next, proposalID, decidedBy, snapshot, audit)
}

// applyAgentProposal is the shared transactional body: audit is nil on the
// regular path, the gate-skip record on the audited one (F-2).
func (s *Store) applyAgentProposal(ctx context.Context, next Agent, proposalID int64, decidedBy string, snapshot *AgentVersion, audit *AccessAuditEvent) (AgentProposal, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AgentProposal{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx,
		`UPDATE workspace_agent_proposal
		 SET status = 'applied', decided_at = now(), decided_by = $3
		 WHERE id = $1 AND agent_id = $2 AND status = 'pending'
		 RETURNING id, agent_id, base_version, definition, description, problem, change, effect,
		           evidence, status, author, generator_model, created_at, decided_at, decided_by`,
		proposalID, next.ID, decidedBy)
	decided, err := scanAgentProposal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// The claim missed: the row is gone (404) or already decided (409).
		var status string
		scanErr := tx.QueryRow(ctx,
			`SELECT status FROM workspace_agent_proposal WHERE id = $1 AND agent_id = $2`,
			proposalID, next.ID).Scan(&status)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return AgentProposal{}, ErrNotFound
		}
		if scanErr != nil {
			return AgentProposal{}, scanErr
		}
		return AgentProposal{}, fmt.Errorf("%w: proposal %d is already %s", ErrConflict, proposalID, status)
	}
	if err != nil {
		return AgentProposal{}, err
	}

	var base int
	if err := tx.QueryRow(ctx,
		`SELECT definition_version FROM workspace_agent WHERE id = $1 FOR UPDATE`, next.ID).Scan(&base); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentProposal{}, ErrNotFound
		}
		return AgentProposal{}, err
	}
	if base != decided.BaseVersion {
		return AgentProposal{}, fmt.Errorf("%w: proposal base is %d, agent is at %d", ErrBaseVersionStale, decided.BaseVersion, base)
	}
	if err := updateAgentRow(ctx, tx, next); err != nil {
		return AgentProposal{}, err
	}
	if snapshot != nil {
		if err := insertAgentVersionRow(ctx, tx, *snapshot); err != nil {
			return AgentProposal{}, err
		}
	}
	if audit != nil {
		if err := insertAccessAudit(ctx, tx, audit.Actor, audit.Action, audit.EntityKind, audit.EntityID, audit.Details); err != nil {
			return AgentProposal{}, fmt.Errorf("%w: %v", ErrAuditWriteFailed, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentProposal{}, err
	}
	return *decided, nil
}

func scanAgentProposal(row interface{ Scan(dest ...any) error }) (*AgentProposal, error) {
	var p AgentProposal
	var definition, evidence []byte
	if err := row.Scan(&p.ID, &p.AgentID, &p.BaseVersion, &definition, &p.Description, &p.Problem,
		&p.Change, &p.Effect, &evidence, &p.Status, &p.Author, &p.GeneratorModel,
		&p.CreatedAt, &p.DecidedAt, &p.DecidedBy); err != nil {
		return nil, err
	}
	if def := decodeAgentDefinition(definition); def != nil {
		p.Definition = *def
	}
	if len(evidence) > 0 {
		if err := json.Unmarshal(evidence, &p.Evidence); err != nil {
			return nil, err
		}
	}
	if p.Evidence == nil {
		p.Evidence = []string{}
	}
	return &p, nil
}
