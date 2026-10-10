package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Org units and visibility (docs/org-structure.md). The tree is stored flat
// with a materialized ancestor path per node; visibility resolves without
// recursion: a resource bound to unit U is visible from any node whose
// ancestor chain contains U.

// ErrUnitNotEmpty reports a delete attempt on a unit that still has children,
// bound resources, members or linked projects.
var ErrUnitNotEmpty = errors.New("org unit is not empty: move its children, resources, users and projects first")

// ErrCycle reports a move that would place a unit inside its own subtree.
var ErrCycle = errors.New("cannot move an org unit into its own subtree")

// CreateOrgUnit inserts a node. ParentID empty creates a root; the path of a
// root is "" and for any other node it is the parent's self path.
func (s *Store) CreateOrgUnit(ctx context.Context, u *OrgUnit) error {
	if !ValidOrgKinds[u.Kind] {
		return fmt.Errorf("unknown org unit kind %q (supported: organization, department, team)", u.Kind)
	}
	path := ""
	if u.ParentID != "" {
		parent, err := s.GetOrgUnit(ctx, u.ParentID)
		if err != nil {
			return err
		}
		path = parent.SelfPath()
	}
	now := time.Now().UTC()
	u.Path = path
	u.CreatedAt = now
	u.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_unit (id, parent_id, kind, name, path, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		u.ID, nullString(u.ParentID), u.Kind, u.Name, u.Path, u.CreatedAt, u.UpdatedAt)
	return err
}

func (s *Store) GetOrgUnit(ctx context.Context, id string) (OrgUnit, error) {
	var u OrgUnit
	var parent *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, parent_id, kind, name, path, created_at, updated_at FROM org_unit WHERE id = $1`, id).
		Scan(&u.ID, &parent, &u.Kind, &u.Name, &u.Path, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.ParentID = derefPtr(parent)
	return u, nil
}

// ListOrgUnits returns every unit in one query, ordered so parents come
// before children (by path length, then path, then id).
func (s *Store) ListOrgUnits(ctx context.Context) ([]OrgUnit, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, parent_id, kind, name, path, created_at, updated_at
		 FROM org_unit
		 ORDER BY char_length(path), path, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []OrgUnit{}
	for rows.Next() {
		var u OrgUnit
		var parent *string
		if err := rows.Scan(&u.ID, &parent, &u.Kind, &u.Name, &u.Path, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.ParentID = derefPtr(parent)
		result = append(result, u)
	}
	return result, rows.Err()
}

// UpdateOrgUnit renames a node or changes its kind. Moving is a separate
// operation because it rewrites the subtree's paths.
func (s *Store) UpdateOrgUnit(ctx context.Context, u OrgUnit) error {
	if !ValidOrgKinds[u.Kind] {
		return fmt.Errorf("unknown org unit kind %q (supported: organization, department, team)", u.Kind)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE org_unit SET kind = $2, name = $3, updated_at = now() WHERE id = $1`,
		u.ID, u.Kind, u.Name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveOrgUnit re-parents a node and rewrites the materialized paths of its
// whole subtree in one transaction. Moving under itself or a descendant is
// rejected.
func (s *Store) MoveOrgUnit(ctx context.Context, id, newParentID string) error {
	unit, err := s.GetOrgUnit(ctx, id)
	if err != nil {
		return err
	}
	if id == newParentID {
		return ErrCycle
	}
	newPath := "" // new path of the node itself: ancestors of the new position
	if newParentID != "" {
		parent, err := s.GetOrgUnit(ctx, newParentID)
		if err != nil {
			return err
		}
		// Cycle guard: the new parent must not live inside the moved subtree,
		// i.e. its self path must not start with the moved unit's self path.
		parentSelf := parent.SelfPath()
		if parentSelf == unit.SelfPath() || strings.HasPrefix(parentSelf, unit.SelfPath()+".") {
			return ErrCycle
		}
		newPath = parentSelf
	}
	oldSelf := unit.SelfPath()
	newSelf := joinPath(newPath, unit.ID)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE org_unit SET parent_id = $2, path = $3, updated_at = now() WHERE id = $1`,
		id, nullString(newParentID), newPath)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// Descendants carry the moved unit's old self path as their path prefix;
	// swap that prefix for the new one. left() comparison instead of LIKE so
	// ids containing '_' or '%' cannot corrupt the match.
	_, err = tx.Exec(ctx,
		`UPDATE org_unit
		 SET path = $2 || substring(path from char_length($1) + 1), updated_at = now()
		 WHERE left(path, char_length($1) + 1) = $1 || '.'`,
		oldSelf, newSelf)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteOrgUnit removes a node. Only empty units can be deleted: no children,
// no bound resources, no member users, no linked projects. The RESTRICT
// foreign keys are the second line of defense.
func (s *Store) DeleteOrgUnit(ctx context.Context, id string) error {
	var children, agents, skills, mcp, providers, triggers, users, projects, roles bool
	err := s.pool.QueryRow(ctx,
		`SELECT
			EXISTS(SELECT 1 FROM org_unit WHERE parent_id = $1),
			EXISTS(SELECT 1 FROM workspace_agent WHERE org_unit_id = $1),
			EXISTS(SELECT 1 FROM workspace_skill WHERE org_unit_id = $1),
			EXISTS(SELECT 1 FROM workspace_mcp_server WHERE org_unit_id = $1),
			EXISTS(SELECT 1 FROM workspace_provider WHERE org_unit_id = $1),
			EXISTS(SELECT 1 FROM workspace_trigger WHERE org_unit_id = $1),
			EXISTS(SELECT 1 FROM workspace_user WHERE org_unit_id = $1),
			EXISTS(SELECT 1 FROM workspace_project_org_unit WHERE org_unit_id = $1),
			EXISTS(SELECT 1 FROM org_unit_role WHERE org_unit_id = $1)`, id).
		Scan(&children, &agents, &skills, &mcp, &providers, &triggers, &users, &projects, &roles)
	if err != nil {
		return err
	}
	if children || agents || skills || mcp || providers || triggers || users || projects || roles {
		return ErrUnitNotEmpty
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM org_unit WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Resource kinds that can be bound to an org unit.
const (
	OrgResourceAgent     = "agent"
	OrgResourceSkill     = "skill"
	OrgResourceMCPServer = "mcp-server"
	OrgResourceProvider  = "provider"
	OrgResourceTrigger   = "trigger"
	OrgResourceTeam      = "team"
)

var orgResourceTables = map[string]string{
	OrgResourceAgent:     "workspace_agent",
	OrgResourceSkill:     "workspace_skill",
	OrgResourceMCPServer: "workspace_mcp_server",
	OrgResourceProvider:  "workspace_provider",
	OrgResourceTrigger:   "workspace_trigger",
	OrgResourceTeam:      "workspace_team",
}

// SetResourceOrgUnit binds a resource to an org unit; unitID empty clears the
// binding back to "whole installation".
func (s *Store) SetResourceOrgUnit(ctx context.Context, kind, resourceID, unitID string) error {
	table, ok := orgResourceTables[kind]
	if !ok {
		return fmt.Errorf("unknown resource kind %q (supported: agent, skill, mcp-server, provider, trigger)", kind)
	}
	if unitID != "" {
		if _, err := s.GetOrgUnit(ctx, unitID); err != nil {
			return err
		}
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE `+table+` SET org_unit_id = $2, updated_at = now() WHERE id = $1`,
		resourceID, nullString(unitID))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UnitResources is the inspection payload of one org unit: everything bound
// to or living in it.
type UnitResources struct {
	Agents     []NamedRef        `json:"agents"`
	Skills     []NamedRef        `json:"skills"`
	MCPServers []NamedRef        `json:"mcp_servers"`
	Providers  []NamedRef        `json:"providers"`
	Triggers   []NamedRef        `json:"triggers"`
	Teams      []NamedRef        `json:"teams"`
	Users      []NamedRef        `json:"users"`
	Projects   []NamedRef        `json:"projects"`
	Roles      []OrgUnitRoleInfo `json:"roles"`
}

// NamedRef is an id/name pair for inspection lists.
type NamedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListUnitResources returns everything attached to the unit: bound resources,
// member users and linked projects.
func (s *Store) ListUnitResources(ctx context.Context, unitID string) (UnitResources, error) {
	out := UnitResources{
		Agents: []NamedRef{}, Skills: []NamedRef{}, MCPServers: []NamedRef{},
		Providers: []NamedRef{}, Triggers: []NamedRef{}, Teams: []NamedRef{},
		Users: []NamedRef{}, Projects: []NamedRef{},
		Roles: []OrgUnitRoleInfo{},
	}
	queries := []struct {
		sql  string
		sink *[]NamedRef
	}{
		{`SELECT id, name FROM workspace_agent WHERE org_unit_id = $1 ORDER BY name`, &out.Agents},
		{`SELECT id, name FROM workspace_skill WHERE org_unit_id = $1 ORDER BY name`, &out.Skills},
		{`SELECT id, name FROM workspace_mcp_server WHERE org_unit_id = $1 ORDER BY name`, &out.MCPServers},
		{`SELECT id, name FROM workspace_provider WHERE org_unit_id = $1 ORDER BY name`, &out.Providers},
		{`SELECT id, name FROM workspace_trigger WHERE org_unit_id = $1 ORDER BY name`, &out.Triggers},
		{`SELECT id, name FROM workspace_team WHERE org_unit_id = $1 ORDER BY name`, &out.Teams},
		{`SELECT id, name FROM workspace_user WHERE org_unit_id = $1 ORDER BY name`, &out.Users},
		{`SELECT p.id, p.name FROM workspace_project p
		   JOIN workspace_project_org_unit pu ON pu.project_id = p.id
		   WHERE pu.org_unit_id = $1 ORDER BY p.name`, &out.Projects},
	}
	for _, q := range queries {
		rows, err := s.pool.Query(ctx, q.sql, unitID)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		for rows.Next() {
			var ref NamedRef
			if err := rows.Scan(&ref.ID, &ref.Name); err != nil {
				return out, err
			}
			*q.sink = append(*q.sink, ref)
		}
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	// Role grants carry different columns than the NamedRef lists.
	roles, err := s.ListOrgUnitRoles(ctx, unitID)
	if err != nil {
		return out, err
	}
	out.Roles = roles
	return out, nil
}

// SetProjectOrgUnits replaces the set of org units a project spans.
func (s *Store) SetProjectOrgUnits(ctx context.Context, projectID string, units []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM workspace_project_org_unit WHERE project_id = $1`, projectID); err != nil {
		return err
	}
	for _, unit := range units {
		if _, err := tx.Exec(ctx,
			`INSERT INTO workspace_project_org_unit (project_id, org_unit_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			projectID, unit); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ProjectOrgUnits returns the org units a project is linked to.
func (s *Store) ProjectOrgUnits(ctx context.Context, projectID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT org_unit_id FROM workspace_project_org_unit WHERE project_id = $1 ORDER BY org_unit_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var units []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		units = append(units, id)
	}
	return units, rows.Err()
}

// allProjectOrgUnits loads the project→units map in one query.
func (s *Store) allProjectOrgUnits(ctx context.Context) (map[string][]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT project_id, org_unit_id FROM workspace_project_org_unit`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var project, unit string
		if err := rows.Scan(&project, &unit); err != nil {
			return nil, err
		}
		out[project] = append(out[project], unit)
	}
	return out, rows.Err()
}

// --- Org unit roles (docs/org-structure.md §13) ---

// OrgUnitRoleInfo is a grant enriched with the grantee's name for UI lists.
type OrgUnitRoleInfo struct {
	OrgUnitRole
	UserName string `json:"user_name"`
}

// ListOrgUnitRoles returns the role grants of one unit, newest grant first,
// joined with user names for display.
func (s *Store) ListOrgUnitRoles(ctx context.Context, unitID string) ([]OrgUnitRoleInfo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT r.user_id, r.org_unit_id, r.role, r.granted_by, r.granted_at, COALESCE(u.name, '')
		 FROM org_unit_role r LEFT JOIN workspace_user u ON u.id = r.user_id
		 WHERE r.org_unit_id = $1 ORDER BY r.granted_at DESC, r.user_id`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []OrgUnitRoleInfo{}
	for rows.Next() {
		var info OrgUnitRoleInfo
		if err := rows.Scan(&info.UserID, &info.OrgUnitID, &info.Role, &info.GrantedBy, &info.GrantedAt, &info.UserName); err != nil {
			return nil, err
		}
		result = append(result, info)
	}
	return result, rows.Err()
}

// SetOrgUnitRole upserts a grant: an existing grant for the same user and
// unit is replaced. Both the user and the unit must exist.
func (s *Store) SetOrgUnitRole(ctx context.Context, unitID, userID, role, grantedBy string) error {
	if _, err := s.GetOrgUnit(ctx, unitID); err != nil {
		return err
	}
	if _, err := s.GetUser(ctx, userID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO org_unit_role (user_id, org_unit_id, role, granted_by)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (user_id, org_unit_id) DO UPDATE SET role = EXCLUDED.role, granted_by = EXCLUDED.granted_by, granted_at = now()`,
		userID, unitID, role, grantedBy)
	return err
}

// RemoveOrgUnitRole drops a grant; missing grants answer ErrNotFound.
func (s *Store) RemoveOrgUnitRole(ctx context.Context, unitID, userID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM org_unit_role WHERE org_unit_id = $1 AND user_id = $2`, unitID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAllOrgUnitRoleGrants returns every grant in one query; the auth gate
// reload turns them into per-principal upgrade scopes.
func (s *Store) ListAllOrgUnitRoleGrants(ctx context.Context) ([]OrgUnitRole, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, org_unit_id, role, granted_by, granted_at FROM org_unit_role ORDER BY user_id, org_unit_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []OrgUnitRole{}
	for rows.Next() {
		var g OrgUnitRole
		if err := rows.Scan(&g.UserID, &g.OrgUnitID, &g.Role, &g.GrantedBy, &g.GrantedAt); err != nil {
			return nil, err
		}
		result = append(result, g)
	}
	return result, rows.Err()
}

// --- Explicit project membership (docs/org-structure.md §16) ---

// ListProjectMembers returns the explicit members of a project.
func (s *Store) ListProjectMembers(ctx context.Context, projectID string) ([]ProjectMember, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, role, created_at, created_by FROM workspace_project_member WHERE project_id = $1 ORDER BY user_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProjectMember{}
	for rows.Next() {
		var m ProjectMember
		if err := rows.Scan(&m.UserID, &m.Role, &m.CreatedAt, &m.CreatedBy); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// AddProjectMember upserts a membership row: an existing member's role is
// updated in place. Both project and user must exist.
func (s *Store) AddProjectMember(ctx context.Context, projectID, userID, role, createdBy string) error {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return err
	}
	if _, err := s.GetUser(ctx, userID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_project_member (project_id, user_id, role, created_by)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		projectID, userID, role, createdBy)
	return err
}

// RemoveProjectMember drops a membership row.
func (s *Store) RemoveProjectMember(ctx context.Context, projectID, userID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM workspace_project_member WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// allProjectMembers loads the project→member-set map in one query.
func (s *Store) allProjectMembers(ctx context.Context) (map[string]map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT project_id, user_id FROM workspace_project_member`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var project, user string
		if err := rows.Scan(&project, &user); err != nil {
			return nil, err
		}
		if out[project] == nil {
			out[project] = map[string]bool{}
		}
		out[project][user] = true
	}
	return out, rows.Err()
}

// ProjectVisible is the exported project-visibility check for callers outside
// the package (kernel run authorization): org-unit intersection or explicit
// membership.
func ProjectVisible(boundUnits, units []string, isMember bool) bool {
	return projectVisibleFor(boundUnits, units, isMember)
}

// OrgVisible reports whether a resource bound to boundUnit is visible to a
// viewer whose ancestor chain is units. Empty boundUnit means org-neutral.
// Nil units means unfiltered access.
func OrgVisible(boundUnit string, units []string) bool {
	if boundUnit == "" || units == nil {
		return true
	}
	for _, u := range units {
		if u == boundUnit {
			return true
		}
	}
	return false
}

// UnitChain resolves the ancestor chain (self included) of a unit id. Empty
// id returns nil (unassigned viewer, no filtering).
func (s *Store) UnitChain(ctx context.Context, unitID string) ([]string, error) {
	if unitID == "" {
		return nil, nil
	}
	unit, err := s.GetOrgUnit(ctx, unitID)
	if err != nil {
		return nil, err
	}
	return unit.Ancestors(), nil
}
