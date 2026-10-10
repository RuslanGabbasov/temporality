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

// Agent teams (docs/agent-teams.md). A team is a versioned definition of a
// multi-agent composition: role slots with requirements and contracts plus a
// protocol from the small topology vocabulary. The manifest is data — the
// runtime compiles it into plan/delegate calls (Wave B); agents are bound to
// slots at launch time, so the definition survives agent churn.

// Team protocol kinds (docs/agent-teams.md §6). The vocabulary is deliberately
// small; a new kind is a product decision, not runtime freedom.
const (
	TeamProtocolPipeline    = "pipeline"
	TeamProtocolLeadWorkers = "lead_workers"
	TeamProtocolFanOut      = "fan_out"
	TeamProtocolReviewGate  = "review_gate"
	TeamProtocolDAG         = "dag"
)

// Slot binding modes: a fixed slot names a concrete agent; a role slot declares
// requirements and is matched at launch (docs/agent-teams.md §7).
const (
	TeamBindingFixed = "fixed"
	TeamBindingRole  = "role"
)

// TeamVersion lifecycle statuses, mirroring skills: a draft awaits a human
// apply and is never executed; active is the normal state.
const (
	TeamVersionDraft  = "draft"
	TeamVersionActive = "active"
)

// Version origins.
const (
	TeamOriginInitial       = "initial"
	TeamOriginHumanEdit     = "human_edit"
	TeamOriginAgentProposal = "agent_proposal"
)

// Team is the current revision of a team definition. Manifest holds the parsed
// TeamManifest JSON (protocol + slots + defaults).
type Team struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Manifest    json.RawMessage `json:"manifest"`
	// Org binding (docs/org-structure.md §3.2): empty = whole installation.
	OrgUnitID string `json:"org_unit_id,omitempty"`
	// VersionStatus is the lifecycle status of the current version row:
	// "draft" awaits human apply and is never launched; empty when no
	// version row matches the pointer.
	VersionStatus string    `json:"version_status,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TeamProvenance records why a version exists (docs/agent-teams.md §12):
// evolution proposals name the observed problem, the proposed change and the
// expected effect so the "why vN became vN+1" chain stays reconstructible.
type TeamProvenance struct {
	Origin          string   `json:"origin"`
	SourceRuns      []string `json:"source_runs,omitempty"`
	ChangeSummary   string   `json:"change_summary,omitempty"`
	ObservedProblem string   `json:"observed_problem,omitempty"`
	ProposedChange  string   `json:"proposed_change,omitempty"`
	ExpectedEffect  string   `json:"expected_effect,omitempty"`
}

// TeamVersion is an immutable snapshot of a team definition.
type TeamVersion struct {
	TeamID   string          `json:"team_id"`
	Version  string          `json:"version"`
	Manifest json.RawMessage `json:"manifest"`
	TeamProvenance
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// TeamManifest is the parsed definition: protocol, role slots and defaults.
type TeamManifest struct {
	Protocol TeamProtocol `json:"protocol"`
	Slots    []TeamSlot   `json:"slots"`
	Defaults TeamDefaults `json:"defaults,omitempty"`
}

// TeamProtocol is the collaboration topology plus its knobs.
type TeamProtocol struct {
	Kind string `json:"kind"`
	// MaxRework caps rework rounds per review gate; 0 = kernel default.
	MaxRework int `json:"max_rework,omitempty"`
	// Steps drive the dag protocol: explicit task ids with dependencies and
	// review gates. Other protocols derive steps from slots.
	Steps []TeamStep `json:"steps,omitempty"`
}

// TeamStep is one step of a dag-protocol team: which slot executes it, what it
// depends on and which steps it reviews.
type TeamStep struct {
	ID        string   `json:"id"`
	SlotID    string   `json:"slot_id"`
	DependsOn []string `json:"depends_on,omitempty"`
	ReviewOf  []string `json:"review_of,omitempty"`
}

// TeamSlot is a role in the team: responsibility, requirements, contract and
// binding mode (docs/agent-teams.md §3.2).
type TeamSlot struct {
	ID             string           `json:"id"`
	Title          string           `json:"title"`
	Responsibility string           `json:"responsibility,omitempty"`
	Requirements   TeamRequirements `json:"requirements,omitempty"`
	// Contract is the expected output the next stage relies on.
	Contract string      `json:"contract,omitempty"`
	Binding  TeamBinding `json:"binding"`
}

// TeamRequirements describes what an agent must have to fill a role slot.
type TeamRequirements struct {
	Capabilities []string `json:"capabilities,omitempty"`
	Skills       []string `json:"skills,omitempty"`
}

// TeamBinding is how a slot is filled: fixed names an agent, role matches at
// launch with an optional preference as a tie-breaker.
type TeamBinding struct {
	Mode             string `json:"mode"`
	AgentID          string `json:"agent_id,omitempty"`
	PreferredAgentID string `json:"preferred_agent_id,omitempty"`
}

// TeamDefaults are launch knobs applied to every step unless overridden.
type TeamDefaults struct {
	MaxTurns      int     `json:"max_turns,omitempty"`
	BudgetUSD     float64 `json:"budget_usd,omitempty"`
	ParallelLimit int     `json:"parallel_limit,omitempty"`
}

// ValidateTeamManifest checks a manifest against the spec: known protocol,
// non-empty unique slots with valid bindings, dag steps referencing known
// slots without cycles. Returns a human-readable error for 422 responses.
func ValidateTeamManifest(raw []byte) error {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || string(raw) == "{}" {
		return errors.New("manifest is required")
	}
	var m TeamManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("manifest is not valid JSON: %w", err)
	}
	switch m.Protocol.Kind {
	case TeamProtocolPipeline, TeamProtocolLeadWorkers, TeamProtocolFanOut, TeamProtocolReviewGate, TeamProtocolDAG:
	case "":
		return errors.New("protocol.kind is required")
	default:
		return fmt.Errorf("unknown protocol kind %q (supported: pipeline, lead_workers, fan_out, review_gate, dag)", m.Protocol.Kind)
	}
	if len(m.Slots) == 0 {
		return errors.New("at least one slot is required")
	}
	seen := map[string]bool{}
	for i, slot := range m.Slots {
		if strings.TrimSpace(slot.ID) == "" {
			return fmt.Errorf("slot[%d].id is required", i)
		}
		if seen[slot.ID] {
			return fmt.Errorf("duplicate slot id %q", slot.ID)
		}
		seen[slot.ID] = true
		switch slot.Binding.Mode {
		case TeamBindingFixed:
			if strings.TrimSpace(slot.Binding.AgentID) == "" {
				return fmt.Errorf("slot %q: fixed binding requires agent_id", slot.ID)
			}
		case TeamBindingRole:
			// preferred_agent_id is optional; requirements may be empty (any
			// visible agent can fill the role).
		case "":
			return fmt.Errorf("slot %q: binding.mode is required (fixed or role)", slot.ID)
		default:
			return fmt.Errorf("slot %q: unknown binding mode %q (supported: fixed, role)", slot.ID, slot.Binding.Mode)
		}
	}
	// Protocol-specific slot expectations.
	switch m.Protocol.Kind {
	case TeamProtocolReviewGate:
		if len(m.Slots) < 2 {
			return errors.New("review_gate requires at least two slots (executor and reviewer)")
		}
	case TeamProtocolFanOut:
		if len(m.Slots) < 2 {
			return errors.New("fan_out requires worker slots plus a reducer slot")
		}
	case TeamProtocolDAG:
		if len(m.Protocol.Steps) == 0 {
			return errors.New("dag protocol requires steps")
		}
		stepSeen := map[string]bool{}
		for i, step := range m.Protocol.Steps {
			if strings.TrimSpace(step.ID) == "" {
				return fmt.Errorf("steps[%d].id is required", i)
			}
			if stepSeen[step.ID] {
				return fmt.Errorf("duplicate step id %q", step.ID)
			}
			stepSeen[step.ID] = true
			if !seen[step.SlotID] {
				return fmt.Errorf("step %q references unknown slot %q", step.ID, step.SlotID)
			}
		}
		if err := validateDAGSteps(m.Protocol.Steps); err != nil {
			return err
		}
	}
	return nil
}

// validateDAGSteps rejects cycles and dangling references the same way the
// kernel's plan tool does — before anything launches (effect=none).
func validateDAGSteps(steps []TeamStep) error {
	byID := map[string]TeamStep{}
	for _, step := range steps {
		byID[step.ID] = step
	}
	const visiting = "visiting"
	const done = "done"
	state := map[string]string{}
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("step graph contains a cycle at %q", id)
		}
		state[id] = visiting
		for _, dep := range byID[id].DependsOn {
			if _, ok := byID[dep]; !ok {
				return fmt.Errorf("step %q depends on unknown step %q", id, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		for _, rev := range byID[id].ReviewOf {
			if _, ok := byID[rev]; !ok {
				return fmt.Errorf("step %q reviews unknown step %q", id, rev)
			}
		}
		state[id] = done
		return nil
	}
	for _, step := range steps {
		if err := visit(step.ID); err != nil {
			return err
		}
	}
	return nil
}

// ParseTeamManifest parses and validates in one call.
func ParseTeamManifest(raw []byte) (TeamManifest, error) {
	var m TeamManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("manifest is not valid JSON: %w", err)
	}
	return m, nil
}

// Teams

func (s *Store) CreateTeam(ctx context.Context, team *Team) error {
	return s.createTeam(ctx, team, TeamProvenance{Origin: TeamOriginInitial}, TeamVersionActive)
}

// CreateTeamDraft creates the team with its first version as a draft: visible
// in the UI for review, never launched until a human applies it.
func (s *Store) CreateTeamDraft(ctx context.Context, team *Team, prov TeamProvenance) error {
	if prov.Origin == "" {
		prov.Origin = TeamOriginAgentProposal
	}
	if err := s.createTeam(ctx, team, prov, TeamVersionDraft); err != nil {
		return err
	}
	team.VersionStatus = TeamVersionDraft
	return nil
}

func (s *Store) createTeam(ctx context.Context, team *Team, prov TeamProvenance, status string) error {
	now := time.Now().UTC()
	team.CreatedAt = now
	team.UpdatedAt = now
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_team (id, name, description, version, manifest, org_unit_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		team.ID, team.Name, team.Description, team.Version, manifestOrNull(team.Manifest), nullString(team.OrgUnitID), team.CreatedAt, team.UpdatedAt); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_team_version (team_id, version, manifest, origin, change_summary, observed_problem, proposed_change, expected_effect, source_runs, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (team_id, version) DO NOTHING`,
		team.ID, team.Version, manifestOrNull(team.Manifest), prov.Origin, prov.ChangeSummary, prov.ObservedProblem, prov.ProposedChange, prov.ExpectedEffect, textArray(prov.SourceRuns), status)
	return err
}

func (s *Store) GetTeam(ctx context.Context, id string) (Team, error) {
	var team Team
	var manifest []byte
	var orgUnitID *string
	err := s.pool.QueryRow(ctx,
		`SELECT t.id, t.name, t.description, t.version, t.manifest, t.org_unit_id, t.created_at, t.updated_at, COALESCE(cv.status, '')
		 FROM workspace_team t LEFT JOIN workspace_team_version cv ON cv.team_id = t.id AND cv.version = t.version
		 WHERE t.id = $1`, id).
		Scan(&team.ID, &team.Name, &team.Description, &team.Version, &manifest, &orgUnitID, &team.CreatedAt, &team.UpdatedAt, &team.VersionStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return team, ErrNotFound
	}
	if err != nil {
		return team, err
	}
	team.OrgUnitID = derefPtr(orgUnitID)
	team.Manifest = rawMessage(manifest)
	return team, nil
}

func (s *Store) ListAllTeams(ctx context.Context) ([]Team, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT t.id, t.name, t.description, t.version, t.manifest, t.org_unit_id, t.created_at, t.updated_at, COALESCE(cv.status, '')
		 FROM workspace_team t LEFT JOIN workspace_team_version cv ON cv.team_id = t.id AND cv.version = t.version
		 ORDER BY t.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTeams(rows)
}

// ListTeamsVisible returns teams visible to a viewer whose org chain is units:
// org-neutral teams plus those bound inside the chain. nil units disables
// filtering (admin and service tokens).
func (s *Store) ListTeamsVisible(ctx context.Context, units []string) ([]Team, error) {
	if units == nil {
		return s.ListAllTeams(ctx)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT t.id, t.name, t.description, t.version, t.manifest, t.org_unit_id, t.created_at, t.updated_at, COALESCE(cv.status, '')
		 FROM workspace_team t LEFT JOIN workspace_team_version cv ON cv.team_id = t.id AND cv.version = t.version
		 WHERE t.org_unit_id IS NULL OR t.org_unit_id = ANY($1) ORDER BY t.created_at`, units)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTeams(rows)
}

func scanTeams(rows pgx.Rows) ([]Team, error) {
	var result []Team
	for rows.Next() {
		var team Team
		var manifest []byte
		var orgUnitID *string
		if err := rows.Scan(&team.ID, &team.Name, &team.Description, &team.Version, &manifest, &orgUnitID, &team.CreatedAt, &team.UpdatedAt, &team.VersionStatus); err != nil {
			return nil, err
		}
		team.OrgUnitID = derefPtr(orgUnitID)
		team.Manifest = rawMessage(manifest)
		result = append(result, team)
	}
	return result, rows.Err()
}

// UpdateTeam updates the current revision and records a new immutable version
// row (refreshed in place when the version string is unchanged — a human save
// is itself the approval, mirroring skills).
func (s *Store) UpdateTeam(ctx context.Context, team *Team) error {
	team.UpdatedAt = time.Now().UTC()
	team.VersionStatus = TeamVersionActive
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_team SET name = $2, description = $3, version = $4, manifest = $5, updated_at = $6 WHERE id = $1`,
		team.ID, team.Name, team.Description, team.Version, manifestOrNull(team.Manifest), team.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO workspace_team_version (team_id, version, manifest, origin, change_summary, status)
		 VALUES ($1, $2, $3, $4, $5, 'active')
		 ON CONFLICT (team_id, version) DO UPDATE SET manifest = EXCLUDED.manifest, origin = EXCLUDED.origin, change_summary = EXCLUDED.change_summary, status = 'active'`,
		team.ID, team.Version, manifestOrNull(team.Manifest), TeamOriginHumanEdit, "")
	return err
}

func (s *Store) DeleteTeam(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_team WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Versions

func (s *Store) ListTeamVersions(ctx context.Context, teamID string) ([]TeamVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT team_id, version, manifest, origin, change_summary, observed_problem, proposed_change, expected_effect, source_runs, status, created_at
		 FROM workspace_team_version WHERE team_id = $1 ORDER BY created_at DESC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TeamVersion
	for rows.Next() {
		var v TeamVersion
		var manifest []byte
		if err := rows.Scan(&v.TeamID, &v.Version, &manifest, &v.Origin, &v.ChangeSummary, &v.ObservedProblem, &v.ProposedChange, &v.ExpectedEffect, &v.SourceRuns, &v.Status, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Manifest = rawMessage(manifest)
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Store) GetTeamVersion(ctx context.Context, teamID, version string) (TeamVersion, error) {
	var v TeamVersion
	var manifest []byte
	err := s.pool.QueryRow(ctx,
		`SELECT team_id, version, manifest, origin, change_summary, observed_problem, proposed_change, expected_effect, source_runs, status, created_at
		 FROM workspace_team_version WHERE team_id = $1 AND version = $2`, teamID, version).
		Scan(&v.TeamID, &v.Version, &manifest, &v.Origin, &v.ChangeSummary, &v.ObservedProblem, &v.ProposedChange, &v.ExpectedEffect, &v.SourceRuns, &v.Status, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.Manifest = rawMessage(manifest)
	return v, nil
}

// ProposeTeamVersion adds a draft version without touching the current
// revision: launches keep using the previous definition until a human applies
// the draft. An empty version picks the next free patch; an explicit version
// that already exists is rejected with ErrConflict.
func (s *Store) ProposeTeamVersion(ctx context.Context, teamID, version string, manifest json.RawMessage, prov TeamProvenance) (TeamVersion, error) {
	if prov.Origin == "" {
		prov.Origin = TeamOriginAgentProposal
	}
	team, err := s.GetTeam(ctx, teamID)
	if err != nil {
		return TeamVersion{}, err
	}
	if version == "" {
		taken := map[string]bool{}
		existing, err := s.ListTeamVersions(ctx, teamID)
		if err != nil {
			return TeamVersion{}, err
		}
		for _, v := range existing {
			taken[v.Version] = true
		}
		version = nextFreePatch(team.Version, taken)
	} else if _, err := s.GetTeamVersion(ctx, teamID, version); err == nil {
		return TeamVersion{}, ErrConflict
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO workspace_team_version (team_id, version, manifest, origin, change_summary, observed_problem, proposed_change, expected_effect, source_runs, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'draft')`,
		teamID, version, manifestOrNull(manifest), prov.Origin, prov.ChangeSummary, prov.ObservedProblem, prov.ProposedChange, prov.ExpectedEffect, textArray(prov.SourceRuns))
	if err != nil {
		return TeamVersion{}, err
	}
	return s.GetTeamVersion(ctx, teamID, version)
}

// ApplyTeamVersion promotes a draft to the team's current definition.
func (s *Store) ApplyTeamVersion(ctx context.Context, teamID, version string) (Team, TeamVersion, error) {
	v, err := s.GetTeamVersion(ctx, teamID, version)
	if err != nil {
		return Team{}, v, err
	}
	if v.Status != TeamVersionDraft {
		return Team{}, v, ErrConflict
	}
	team, err := s.GetTeam(ctx, teamID)
	if err != nil {
		return team, v, err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE workspace_team_version SET status = 'active' WHERE team_id = $1 AND version = $2`,
		teamID, version); err != nil {
		return team, v, err
	}
	team.Version = version
	team.Manifest = v.Manifest
	team.UpdatedAt = time.Now().UTC()
	if _, err := s.pool.Exec(ctx,
		`UPDATE workspace_team SET version = $2, manifest = $3, updated_at = $4 WHERE id = $1`,
		teamID, version, manifestOrNull(v.Manifest), team.UpdatedAt); err != nil {
		return team, v, err
	}
	team.VersionStatus = TeamVersionActive
	return team, v, nil
}

// RejectTeamVersion marks a draft as rejected: it stays in history but can no
// longer be applied.
func (s *Store) RejectTeamVersion(ctx context.Context, teamID, version string) (TeamVersion, error) {
	v, err := s.GetTeamVersion(ctx, teamID, version)
	if err != nil {
		return v, err
	}
	if v.Status != TeamVersionDraft {
		return v, ErrConflict
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE workspace_team_version SET status = 'rejected' WHERE team_id = $1 AND version = $2`,
		teamID, version); err != nil {
		return v, err
	}
	return s.GetTeamVersion(ctx, teamID, version)
}
