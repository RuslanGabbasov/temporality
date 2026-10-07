package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Skill version lifecycle (docs/knowledge-evolution.md Wave A): agent
// proposals land as drafts and only become the current revision through an
// explicit human apply.
const (
	SkillVersionDraft  = "draft"
	SkillVersionActive = "active"
)

// Skill version origins: who authored the change.
const (
	SkillOriginInitial       = "initial"
	SkillOriginHumanEdit     = "human-edit"
	SkillOriginAgentProposal = "agent-proposal"
)

// Skill is a Living Skill: a versioned capability (SKILL.md + skill.yaml
// manifest). Manifest is stored as parsed JSON; versions are immutable.
// Skills are workspace-global: agents bind them across every project; where
// a skill ran is recorded per execution in SkillExecution.ProjectID.
type Skill struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Markdown    string          `json:"markdown"`
	Manifest    json.RawMessage `json:"manifest"`
	// Org binding (docs/org-structure.md §3.2): empty = whole installation.
	OrgUnitID string `json:"org_unit_id,omitempty"`
	// VersionStatus is the lifecycle status of the current version row:
	// "draft" awaits human apply and is never served to agents; "active" is
	// the normal state. Empty when no version row matches the pointer.
	VersionStatus string    `json:"version_status,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SkillProvenance records why a version exists: who authored it and which
// runs, evidence and knowledge produced it, so the evolution chain
// (why vN became vN+1) stays reconstructible (docs/knowledge-evolution.md §8).
// For agent evolution proposals (docs/living-skills.md §21) the anatomy is
// explicit: what problem was observed, what change is proposed and what
// effect it is expected to have.
type SkillProvenance struct {
	Origin          string   `json:"origin"`
	SourceRuns      []string `json:"source_runs,omitempty"`
	EvidenceRefs    []string `json:"evidence_refs,omitempty"`
	KnowledgeIDs    []string `json:"knowledge_ids,omitempty"`
	ChangeSummary   string   `json:"change_summary,omitempty"`
	ObservedProblem string   `json:"observed_problem,omitempty"`
	ProposedChange  string   `json:"proposed_change,omitempty"`
	ExpectedEffect  string   `json:"expected_effect,omitempty"`
}

// SkillVersion is an immutable snapshot of a skill's content plus the
// provenance of the change that produced it.
type SkillVersion struct {
	SkillID  string          `json:"skill_id"`
	Version  string          `json:"version"`
	Markdown string          `json:"markdown"`
	Manifest json.RawMessage `json:"manifest"`
	SkillProvenance
	// Status is draft until a human applies the version, active afterwards.
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// SkillExecution links a run to the exact skill version used.
// Status is derived from the run; this row is the provenance record.
type SkillExecution struct {
	ID           int64     `json:"id"`
	SkillID      string    `json:"skill_id"`
	SkillVersion string    `json:"skill_version"`
	ProjectID    string    `json:"project_id"`
	RunID        string    `json:"run_id"`
	AgentID      string    `json:"agent_id"`
	StartedAt    time.Time `json:"started_at"`
}

// Skills

func (s *Store) CreateSkill(ctx context.Context, sk *Skill) error {
	return s.createSkill(ctx, sk, SkillProvenance{Origin: SkillOriginInitial}, SkillVersionActive)
}

// CreateSkillDraft creates the skill with its first version as a draft:
// visible in the Skills UI for review, but never served to agents until a
// human applies it (docs/knowledge-evolution.md Wave A).
func (s *Store) CreateSkillDraft(ctx context.Context, sk *Skill, prov SkillProvenance) error {
	if prov.Origin == "" {
		prov.Origin = SkillOriginAgentProposal
	}
	if err := s.createSkill(ctx, sk, prov, SkillVersionDraft); err != nil {
		return err
	}
	sk.VersionStatus = SkillVersionDraft
	return nil
}

func (s *Store) createSkill(ctx context.Context, sk *Skill, prov SkillProvenance, status string) error {
	now := time.Now().UTC()
	sk.CreatedAt = now
	sk.UpdatedAt = now
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_skill (id, name, description, version, markdown, manifest, org_unit_id, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		sk.ID, sk.Name, sk.Description, sk.Version, sk.Markdown, manifestOrNull(sk.Manifest), nullString(sk.OrgUnitID), sk.CreatedAt, sk.UpdatedAt); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_skill_version (skill_id, version, markdown, manifest, created_at, origin, source_runs, evidence_refs, knowledge_ids, change_summary, observed_problem, proposed_change, expected_effect, status)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			 ON CONFLICT (skill_id, version) DO NOTHING`,
		sk.ID, sk.Version, sk.Markdown, manifestOrNull(sk.Manifest), now, prov.Origin, textArray(prov.SourceRuns), textArray(prov.EvidenceRefs), textArray(prov.KnowledgeIDs), prov.ChangeSummary, prov.ObservedProblem, prov.ProposedChange, prov.ExpectedEffect, status)
	return err
}

func (s *Store) GetSkill(ctx context.Context, id string) (Skill, error) {
	var sk Skill
	var manifest []byte
	var orgUnitID *string
	err := s.pool.QueryRow(ctx,
		`SELECT s.id, s.name, s.description, s.version, s.markdown, s.manifest, s.org_unit_id, s.created_at, s.updated_at, COALESCE(cv.status, '')
			 FROM workspace_skill s LEFT JOIN workspace_skill_version cv ON cv.skill_id = s.id AND cv.version = s.version
			 WHERE s.id = $1`, id).
		Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Version, &sk.Markdown, &manifest, &orgUnitID, &sk.CreatedAt, &sk.UpdatedAt, &sk.VersionStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return sk, ErrNotFound
	}
	if err != nil {
		return sk, err
	}
	sk.OrgUnitID = derefPtr(orgUnitID)
	sk.Manifest = rawMessage(manifest)
	return sk, nil
}

func (s *Store) ListAllSkills(ctx context.Context) ([]Skill, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT s.id, s.name, s.description, s.version, s.markdown, s.manifest, s.org_unit_id, s.created_at, s.updated_at, COALESCE(cv.status, '')
			 FROM workspace_skill s LEFT JOIN workspace_skill_version cv ON cv.skill_id = s.id AND cv.version = s.version
			 ORDER BY s.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSkills(rows)
}

// ListSkillsVisible returns skills visible to a viewer whose org chain is
// units: org-neutral skills plus those bound inside the chain. nil units
// disables filtering (admin and service tokens).
func (s *Store) ListSkillsVisible(ctx context.Context, units []string) ([]Skill, error) {
	if units == nil {
		return s.ListAllSkills(ctx)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT s.id, s.name, s.description, s.version, s.markdown, s.manifest, s.org_unit_id, s.created_at, s.updated_at, COALESCE(cv.status, '')
			 FROM workspace_skill s LEFT JOIN workspace_skill_version cv ON cv.skill_id = s.id AND cv.version = s.version
			 WHERE s.org_unit_id IS NULL OR s.org_unit_id = ANY($1) ORDER BY s.created_at`, units)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSkills(rows)
}

func scanSkills(rows pgx.Rows) ([]Skill, error) {
	var result []Skill
	for rows.Next() {
		var sk Skill
		var manifest []byte
		var orgUnitID *string
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Version, &sk.Markdown, &manifest, &orgUnitID, &sk.CreatedAt, &sk.UpdatedAt, &sk.VersionStatus); err != nil {
			return nil, err
		}
		sk.OrgUnitID = derefPtr(orgUnitID)
		sk.Manifest = rawMessage(manifest)
		result = append(result, sk)
	}
	return result, rows.Err()
}

// UpdateSkill updates the current revision and records a new immutable version
// when the content actually changed. Version bumps are the caller's decision.
// When the caller keeps the same version string the snapshot is refreshed in
// place and marked active: a human save is itself the approval, so an edited
// draft stops being a draft instead of trapping the skill un-applied.
func (s *Store) UpdateSkill(ctx context.Context, sk *Skill) error {
	sk.UpdatedAt = time.Now().UTC()
	sk.VersionStatus = SkillVersionActive
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_skill SET name = $2, description = $3, version = $4, markdown = $5, manifest = $6, org_unit_id = $7, updated_at = $8 WHERE id = $1`,
		sk.ID, sk.Name, sk.Description, sk.Version, sk.Markdown, manifestOrNull(sk.Manifest), nullString(sk.OrgUnitID), sk.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO workspace_skill_version (skill_id, version, markdown, manifest, created_at, origin, change_summary, status)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 'active')
			 ON CONFLICT (skill_id, version) DO UPDATE SET markdown = EXCLUDED.markdown, manifest = EXCLUDED.manifest, origin = EXCLUDED.origin, change_summary = EXCLUDED.change_summary, status = 'active'`,
		sk.ID, sk.Version, sk.Markdown, manifestOrNull(sk.Manifest), sk.UpdatedAt, SkillOriginHumanEdit, "")
	return err
}

func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_skill WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Versions

func (s *Store) ListSkillVersions(ctx context.Context, skillID string) ([]SkillVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT skill_id, version, markdown, manifest, origin, source_runs, evidence_refs, knowledge_ids, change_summary, observed_problem, proposed_change, expected_effect, status, created_at
			 FROM workspace_skill_version WHERE skill_id = $1 ORDER BY created_at DESC`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SkillVersion
	for rows.Next() {
		var v SkillVersion
		var manifest []byte
		if err := rows.Scan(&v.SkillID, &v.Version, &v.Markdown, &manifest, &v.Origin, &v.SourceRuns, &v.EvidenceRefs, &v.KnowledgeIDs, &v.ChangeSummary, &v.ObservedProblem, &v.ProposedChange, &v.ExpectedEffect, &v.Status, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Manifest = rawMessage(manifest)
		result = append(result, v)
	}
	return result, rows.Err()
}

// ProposeSkillVersion adds a draft version to an existing skill without
// touching the current revision: agents keep running the previous content
// until a human applies the draft. An empty version picks the next free
// patch after the skill's current version; an explicit version that already
// exists is rejected with ErrConflict.
func (s *Store) ProposeSkillVersion(ctx context.Context, skillID, version, markdown string, manifest json.RawMessage, prov SkillProvenance) (SkillVersion, error) {
	var current string
	err := s.pool.QueryRow(ctx, `SELECT version FROM workspace_skill WHERE id = $1`, skillID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillVersion{}, ErrNotFound
	}
	if err != nil {
		return SkillVersion{}, err
	}
	rows, err := s.pool.Query(ctx, `SELECT version FROM workspace_skill_version WHERE skill_id = $1`, skillID)
	if err != nil {
		return SkillVersion{}, err
	}
	defer rows.Close()
	taken := map[string]bool{}
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return SkillVersion{}, err
		}
		taken[existing] = true
	}
	if err := rows.Err(); err != nil {
		return SkillVersion{}, err
	}
	if version == "" {
		version = nextFreePatch(current, taken)
	} else if taken[version] {
		return SkillVersion{}, ErrConflict
	}
	if prov.Origin == "" {
		prov.Origin = SkillOriginAgentProposal
	}
	now := time.Now().UTC()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_skill_version (skill_id, version, markdown, manifest, created_at, origin, source_runs, evidence_refs, knowledge_ids, change_summary, observed_problem, proposed_change, expected_effect, status)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'draft')`,
		skillID, version, markdown, manifestOrNull(manifest), now, prov.Origin, textArray(prov.SourceRuns), textArray(prov.EvidenceRefs), textArray(prov.KnowledgeIDs), prov.ChangeSummary, prov.ObservedProblem, prov.ProposedChange, prov.ExpectedEffect); err != nil {
		return SkillVersion{}, err
	}
	return SkillVersion{
		SkillID: skillID, Version: version, Markdown: markdown, Manifest: rawMessage(manifest),
		SkillProvenance: prov, Status: SkillVersionDraft, CreatedAt: now,
	}, nil
}

// ApplySkillVersion promotes a version to the current revision. Drafts become
// active; applying an already-active older version is the rollback path
// (docs/living-skills.md §24) — the version's content simply becomes current
// again. Returns the updated skill and the applied version row.
func (s *Store) ApplySkillVersion(ctx context.Context, skillID, version string) (Skill, SkillVersion, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Skill{}, SkillVersion{}, err
	}
	defer tx.Rollback(ctx)
	var applied SkillVersion
	var manifest []byte
	err = tx.QueryRow(ctx,
		`SELECT skill_id, version, markdown, manifest, origin, source_runs, evidence_refs, knowledge_ids, change_summary, observed_problem, proposed_change, expected_effect, status, created_at
			 FROM workspace_skill_version WHERE skill_id = $1 AND version = $2 FOR UPDATE`, skillID, version).
		Scan(&applied.SkillID, &applied.Version, &applied.Markdown, &manifest, &applied.Origin, &applied.SourceRuns, &applied.EvidenceRefs, &applied.KnowledgeIDs, &applied.ChangeSummary, &applied.ObservedProblem, &applied.ProposedChange, &applied.ExpectedEffect, &applied.Status, &applied.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Skill{}, SkillVersion{}, ErrNotFound
	}
	if err != nil {
		return Skill{}, SkillVersion{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE workspace_skill_version SET status = 'active' WHERE skill_id = $1 AND version = $2`, skillID, version); err != nil {
		return Skill{}, SkillVersion{}, err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE workspace_skill SET version = $2, markdown = $3, manifest = $4, updated_at = now() WHERE id = $1`,
		skillID, version, applied.Markdown, manifestOrNull(manifest))
	if err != nil {
		return Skill{}, SkillVersion{}, err
	}
	if tag.RowsAffected() == 0 {
		return Skill{}, SkillVersion{}, ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return Skill{}, SkillVersion{}, err
	}
	applied.Manifest = rawMessage(manifest)
	applied.Status = SkillVersionActive
	skill, err := s.GetSkill(ctx, skillID)
	return skill, applied, err
}

// RejectSkillVersion dismisses a draft proposal (docs/living-skills.md §23):
// the version row stays in history for audit but is marked rejected and can
// never be applied. Rejecting a non-draft is a conflict — published versions
// are immutable history.
func (s *Store) RejectSkillVersion(ctx context.Context, skillID, version string) (SkillVersion, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_skill_version SET status = 'rejected' WHERE skill_id = $1 AND version = $2 AND status = 'draft'`, skillID, version)
	if err != nil {
		return SkillVersion{}, err
	}
	if tag.RowsAffected() == 0 {
		// Distinguish "no such version" from "not a draft" for an honest error.
		var status string
		err := s.pool.QueryRow(ctx, `SELECT status FROM workspace_skill_version WHERE skill_id = $1 AND version = $2`, skillID, version).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return SkillVersion{}, ErrNotFound
		}
		if err != nil {
			return SkillVersion{}, err
		}
		return SkillVersion{}, fmt.Errorf("%w: only draft proposals can be rejected (status %q)", ErrConflict, status)
	}
	v, err := s.GetSkillVersion(ctx, skillID, version)
	if err != nil {
		return SkillVersion{}, err
	}
	return v, nil
}

// GetSkillVersion returns one immutable version row (for diffs and evals).
func (s *Store) GetSkillVersion(ctx context.Context, skillID, version string) (SkillVersion, error) {
	var v SkillVersion
	var manifest []byte
	err := s.pool.QueryRow(ctx,
		`SELECT skill_id, version, markdown, manifest, origin, source_runs, evidence_refs, knowledge_ids, change_summary, observed_problem, proposed_change, expected_effect, status, created_at
			 FROM workspace_skill_version WHERE skill_id = $1 AND version = $2`, skillID, version).
		Scan(&v.SkillID, &v.Version, &v.Markdown, &manifest, &v.Origin, &v.SourceRuns, &v.EvidenceRefs, &v.KnowledgeIDs, &v.ChangeSummary, &v.ObservedProblem, &v.ProposedChange, &v.ExpectedEffect, &v.Status, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.Manifest = rawMessage(manifest)
	return v, nil
}

// nextFreePatch returns the first patch bump after current that no existing
// version row occupies, so concurrent proposals never collide.
func nextFreePatch(current string, taken map[string]bool) string {
	segments := strings.SplitN(strings.TrimSpace(current), ".", 3)
	major, minor := 0, 1
	patch := 0
	if len(segments) == 3 {
		if parsed, err := strconv.Atoi(segments[0]); err == nil {
			major = parsed
		}
		if parsed, err := strconv.Atoi(segments[1]); err == nil {
			minor = parsed
		}
		if parsed, err := strconv.Atoi(strings.SplitN(segments[2], "-", 2)[0]); err == nil {
			patch = parsed
		}
	}
	for patch++; ; patch++ {
		candidate := fmt.Sprintf("%d.%d.%d", major, minor, patch)
		if !taken[candidate] {
			return candidate
		}
	}
}

// textArray keeps NOT NULL text[] columns happy: a nil slice encodes as NULL.
func textArray(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// Executions

func (s *Store) RecordSkillExecution(ctx context.Context, e *SkillExecution) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_skill_execution (skill_id, skill_version, project_id, run_id, agent_id, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		e.SkillID, e.SkillVersion, e.ProjectID, e.RunID, e.AgentID, e.StartedAt)
	return err
}

func (s *Store) ListSkillExecutions(ctx context.Context, skillID string, limit int) ([]SkillExecution, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, skill_id, skill_version, project_id, run_id, agent_id, started_at
		 FROM workspace_skill_execution WHERE skill_id = $1 ORDER BY started_at DESC LIMIT $2`, skillID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SkillExecution
	for rows.Next() {
		var e SkillExecution
		if err := rows.Scan(&e.ID, &e.SkillID, &e.SkillVersion, &e.ProjectID, &e.RunID, &e.AgentID, &e.StartedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// ListSkillExecutionsByRun returns executions recorded for a run.
func (s *Store) ListSkillExecutionsByRun(ctx context.Context, runID string) ([]SkillExecution, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, skill_id, skill_version, project_id, run_id, agent_id, started_at
		 FROM workspace_skill_execution WHERE run_id = $1 ORDER BY started_at DESC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SkillExecution
	for rows.Next() {
		var e SkillExecution
		if err := rows.Scan(&e.ID, &e.SkillID, &e.SkillVersion, &e.ProjectID, &e.RunID, &e.AgentID, &e.StartedAt); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func manifestOrNull(m json.RawMessage) any {
	if len(m) == 0 {
		return nil
	}
	return []byte(m)
}

func rawMessage(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}
