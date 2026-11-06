package workspace

import (
	"context"
	"strings"
)

// Run visibility scoping (docs/activity-scoping.md, the operator model):
// activity is global but a non-admin principal only sees the runs they have a
// stake in — their own work (including delegation/plan child runs: those
// inherit the initiating actor), the runs of triggers they configured, and
// the runs of people inside org units they were granted a role on. Admins
// (and unscoped service principals) see everything.
//
// The scope is expressed as the LIST OF RUN INITIATORS the principal may see:
// workspace_run's exec_context carries 'actor' (user id for interactive runs,
// trigger id for automated ones) and authorized_by.user_id, and every
// delegation child stamps the root user — one ANY-of match covers own runs,
// their delegation trees and owned triggers without walking parents.

// RunVisibilityActors resolves which run initiators the principal may see.
// A nil result means NO restriction (admin / unscoped service principal);
// an empty non-nil slice means the principal sees no runs at all. The user's
// org grants are read here (not from the principal) so every caller — kernel
// HTTP handlers, the journal's token reloader — resolves the same scope.
func (s *Store) RunVisibilityActors(ctx context.Context, p PrincipalSeed) ([]string, error) {
	if p.Admin || p.UserID == "" {
		return nil, nil
	}
	actors := []string{p.UserID}
	// Triggers the user configured: their automated runs carry the trigger id
	// as the initiating actor.
	owned, err := s.triggersCreatedBy(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	actors = append(actors, owned...)
	// Org grants: a role on a unit opens that unit's whole subtree (the same
	// subtree semantics org resources use), and every member's runs become
	// visible through their user ids.
	grantUnits := p.GrantUnits
	if len(grantUnits) == 0 {
		grants, gErr := s.ListAllOrgUnitRoleGrants(ctx)
		if gErr != nil {
			return nil, gErr
		}
		for _, grant := range grants {
			if grant.UserID == p.UserID {
				grantUnits = append(grantUnits, grant.OrgUnitID)
			}
		}
	}
	if len(grantUnits) > 0 {
		unitIDs, err := s.subtreeUnitIDs(ctx, grantUnits)
		if err != nil {
			return nil, err
		}
		if len(unitIDs) > 0 {
			members, err := s.userIDsInUnits(ctx, unitIDs)
			if err != nil {
				return nil, err
			}
			actors = append(actors, members...)
		}
	}
	return dedupeStrings(actors), nil
}

// PrincipalSeed is the visibility-relevant projection of a controlplane
// principal: the caller (kernel or journal) maps its own auth types onto it.
type PrincipalSeed struct {
	UserID string
	Admin  bool
	// GrantUnits are the org units the principal holds ANY granted role on
	// (docs/org-structure.md §13): each grant opens the unit's subtree.
	GrantUnits []string
}

func (s *Store) triggersCreatedBy(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM workspace_trigger WHERE created_by = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// subtreeUnitIDs expands the given units to themselves plus all descendants.
// A unit's path is its ancestor chain dot-joined — but unit IDS may contain
// dots themselves (timestamps, slugs), so naive segment splitting corrupts
// them. Instead each root matches as a path PREFIX on a segment boundary:
// root itself, or path == root, or path starts with root + ".".
func (s *Store) subtreeUnitIDs(ctx context.Context, roots []string) ([]string, error) {
	units, err := s.ListOrgUnits(ctx)
	if err != nil {
		return nil, err
	}
	isRoot := make(map[string]bool, len(roots))
	for _, id := range roots {
		isRoot[id] = true
	}
	out := make([]string, 0, len(units))
	for _, unit := range units {
		if isRoot[unit.ID] {
			out = append(out, unit.ID)
			continue
		}
		for _, root := range roots {
			if unit.Path == root || strings.HasPrefix(unit.Path, root+".") {
				out = append(out, unit.ID)
				break
			}
		}
	}
	return out, nil
}

func (s *Store) userIDsInUnits(ctx context.Context, unitIDs []string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM workspace_user WHERE org_unit_id = ANY($1) AND active = true`, textListArg(unitIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
