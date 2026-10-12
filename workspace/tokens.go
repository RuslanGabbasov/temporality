package workspace

import (
	"context"
	"log/slog"

	"github.com/temporality-project/temporality/controlplane"
)

// PrincipalForUser resolves the effective principal of one workspace user —
// the same projection LoadTokenPrincipals builds for token auth: role, org
// visibility chain, unit role grants and visible projects. The intake
// pipeline uses it to authorize start_task as the identified sender instead
// of as the kernel's internal token (docs/inbound-messages.md §9): the task
// lands with the sender's rights, never wider ones.
func (s *Store) PrincipalForUser(ctx context.Context, log *slog.Logger, userID string) (controlplane.Principal, bool) {
	u, err := s.GetUser(ctx, userID)
	if err != nil || !u.Active {
		return controlplane.Principal{}, false
	}
	role, err := controlplane.ParseRole(u.Role)
	if err != nil {
		log.Error("skip principal: unknown role", "user", u.ID, "role", u.Role)
		return controlplane.Principal{}, false
	}
	units, err := s.ListOrgUnits(ctx)
	if err != nil {
		log.Error("principal org units", "user", u.ID, "error", err)
		return controlplane.Principal{}, false
	}
	unitByID := make(map[string]OrgUnit, len(units))
	for _, unit := range units {
		unitByID[unit.ID] = unit
	}
	var visible []string
	if u.OrgUnitID != "" {
		if unit, ok := unitByID[u.OrgUnitID]; ok {
			visible = unit.Ancestors()
		} else {
			log.Error("skip org visibility for user: unknown unit", "user", u.ID, "unit", u.OrgUnitID)
		}
	}
	grants, err := s.ListAllOrgUnitRoleGrants(ctx)
	if err != nil {
		log.Error("principal role grants", "user", u.ID, "error", err)
		return controlplane.Principal{}, false
	}
	orgRoles := []controlplane.OrgRoleGrant{}
	for _, g := range grants {
		if g.UserID != userID {
			continue
		}
		grantRole, rErr := controlplane.ParseRole(g.Role)
		if rErr != nil {
			log.Error("skip org unit role grant: unknown role", "user", g.UserID, "role", g.Role)
			continue
		}
		path := ""
		if unit, ok := unitByID[g.OrgUnitID]; ok {
			path = unit.SelfPath()
		}
		orgRoles = append(orgRoles, controlplane.OrgRoleGrant{UnitID: g.OrgUnitID, Path: path, Role: grantRole})
	}
	projects := []string{"*"}
	if role < controlplane.RoleAdmin && u.OrgUnitID != "" && visible != nil {
		if ids, pErr := s.VisibleProjectIDs(ctx, u.ID, false, visible); pErr != nil {
			log.Error("resolve visible projects for user", "user", u.ID, "error", pErr)
		} else {
			projects = ids
		}
	}
	return controlplane.Principal{
		Subject: u.Name, Role: role, Projects: projects, UserID: u.ID,
		OrgUnitID: u.OrgUnitID, VisibleUnits: visible, OrgRoles: orgRoles,
	}, true
}

// LoadTokenPrincipals builds the auth gate's bearer-token → principal map
// from the workspace store: active users with tokens, their org visibility
// chains, org role grants and visible projects (docs/org-structure.md §13-15).
// The kernel and the journal both call it so a workspace user token
// authorizes identically on every service that fronts the gate. Individual
// broken rows (unknown role, unknown org unit) are skipped with a log line;
// a database error returns the error and no map.
func (s *Store) LoadTokenPrincipals(ctx context.Context, log *slog.Logger) (map[string]controlplane.Principal, error) {
	users, err := s.ListUsersWithTokens(ctx)
	if err != nil {
		return nil, err
	}
	// One query for the whole tree; user chains resolve from the paths.
	units, err := s.ListOrgUnits(ctx)
	if err != nil {
		return nil, err
	}
	unitByID := make(map[string]OrgUnit, len(units))
	for _, u := range units {
		unitByID[u.ID] = u
	}
	// Org role grants: one query, grouped per user; each grant acts on its
	// unit and subtree.
	grants, err := s.ListAllOrgUnitRoleGrants(ctx)
	if err != nil {
		return nil, err
	}
	grantByUser := map[string][]controlplane.OrgRoleGrant{}
	for _, g := range grants {
		role, rErr := controlplane.ParseRole(g.Role)
		if rErr != nil {
			log.Error("skip org unit role grant: unknown role", "user", g.UserID, "role", g.Role)
			continue
		}
		path := ""
		if unit, ok := unitByID[g.OrgUnitID]; ok {
			path = unit.SelfPath()
		}
		grantByUser[g.UserID] = append(grantByUser[g.UserID], controlplane.OrgRoleGrant{
			UnitID: g.OrgUnitID, Path: path, Role: role,
		})
	}
	principals := make(map[string]controlplane.Principal, len(users))
	for _, u := range users {
		if u.Token == "" || !u.Active {
			continue
		}
		role, err := controlplane.ParseRole(u.Role)
		if err != nil {
			log.Error("skip workspace user token: unknown role", "user", u.Name, "role", u.Role)
			continue
		}
		var visible []string
		if u.OrgUnitID != "" {
			if unit, ok := unitByID[u.OrgUnitID]; ok {
				visible = unit.Ancestors()
			} else {
				log.Error("skip org visibility for user: unknown unit", "user", u.Name, "unit", u.OrgUnitID)
			}
		}
		// Project scope follows the org model (docs/org-structure.md §15):
		// admins and org-unassigned users keep the transition "*"; assigned
		// users get the concrete id list — org intersection plus explicit
		// membership. The legacy user.projects column is no longer consulted.
		projects := []string{"*"}
		if role < controlplane.RoleAdmin && u.OrgUnitID != "" && visible != nil {
			if ids, pErr := s.VisibleProjectIDs(ctx, u.ID, false, visible); pErr != nil {
				log.Error("resolve visible projects for user", "user", u.Name, "error", pErr)
			} else {
				projects = ids
			}
		}
		principals[u.Token] = controlplane.Principal{
			Subject: u.Name, Role: role, Projects: projects, UserID: u.ID,
			OrgUnitID: u.OrgUnitID, VisibleUnits: visible,
			OrgRoles: grantByUser[u.ID],
		}
	}
	return principals, nil
}
