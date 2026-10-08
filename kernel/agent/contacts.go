package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// human_contacts (docs/triggers-and-escalations.md §5–6): before ask_human the
// agent has to decide WHO to ask. The tool searches the workspace user
// directory and resolves escalation targets ("ask the manager") without ever
// exposing channel addresses — delivery is resolved by the kernel (§6), so
// only channel TYPES are reported. The webhook URL or matrix room a user
// registered is private data, not agent context.

// orgUnitLite is the org tree slice the tool needs: identity and placement.
// The full policy surface of a unit stays in the workspace API.
type orgUnitLite struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Path     string `json:"path"`
}

// ancestors mirrors workspace.OrgUnit.Ancestors: the unit itself plus every
// ancestor from the materialized path, oldest first.
func (u orgUnitLite) ancestors() []string {
	chain := make([]string, 0, 4)
	if u.Path != "" {
		chain = append(chain, strings.Split(u.Path, ".")...)
	}
	return append(chain, u.ID)
}

// contactCard is the agent-facing projection of a workspace user. Note what
// is deliberately absent: channel addresses (private), channels that are
// disabled, and any credential material.
type contactCard struct {
	UserID      string   `json:"user_id"`
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	OrgUnitID   string   `json:"org_unit_id,omitempty"`
	OrgUnit     string   `json:"org_unit,omitempty"`
	Channels    []string `json:"channels"`
	Preferred   string   `json:"preferred_channel,omitempty"`
	ViaWebInbox bool     `json:"via_web_inbox"`
}

// handleHumanContacts serves the human_contacts tool: directory search or
// escalation resolution. Both paths return contact cards the agent can pass
// straight into ask_human's recipient field.
func (a *Activities) handleHumanContacts(ctx context.Context, request ToolRequest) (ToolResult, error) {
	query, _ := request.Arguments["query"].(string)
	role, _ := request.Arguments["role"].(string)
	unitID, _ := request.Arguments["org_unit_id"].(string)
	escalateFor, _ := request.Arguments["escalate_for"].(string)

	users, ok := a.fetchWorkspaceUsers(ctx)
	if !ok {
		return ToolResult{Content: "error: workspace user list is unavailable"}, nil
	}
	units, ok := a.fetchOrgUnits(ctx)
	if !ok {
		units = nil // search still works without names; escalation needs the tree
	}

	if escalateFor != "" {
		if units == nil {
			return ToolResult{Content: "error: org structure is unavailable, escalation cannot be resolved"}, nil
		}
		return ToolResult{Content: renderEscalation(users, units, escalateFor)}, nil
	}
	return ToolResult{Content: renderContacts(users, units, query, role, unitID)}, nil
}

// fetchOrgUnits pulls the org tree once per call. A missing tree degrades
// search (no unit names) but blocks escalation — the ancestor walk is the
// whole point there.
func (a *Activities) fetchOrgUnits(ctx context.Context) ([]orgUnitLite, bool) {
	endpoint := fmt.Sprintf("%s/v1/org/units", a.WorkspaceURL)
	body, status, err := a.workspaceDo(ctx, http.MethodGet, endpoint, nil)
	if err != nil || status != http.StatusOK {
		return nil, false
	}
	var list struct {
		Units []orgUnitLite `json:"units"`
	}
	if json.Unmarshal([]byte(body), &list) != nil {
		return nil, false
	}
	return list.Units, true
}

// renderContacts filters the active directory by query (name/id substring,
// case-insensitive), role and org unit, then emits cards. The list is capped
// so a large installation cannot flood the agent's context.
func renderContacts(users []WorkspaceUser, units []orgUnitLite, query, role, unitID string) string {
	names := unitNames(units)
	query = strings.ToLower(strings.TrimSpace(query))
	role = strings.TrimSpace(role)
	unitID = strings.TrimSpace(unitID)

	const limit = 50
	matches := make([]WorkspaceUser, 0, len(users))
	for _, u := range users {
		if !u.Active {
			continue
		}
		if role != "" && u.Role != role {
			continue
		}
		if unitID != "" && u.OrgUnitID != unitID {
			continue
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(u.Name), query) &&
			!strings.Contains(strings.ToLower(u.ID), query) {
			continue
		}
		matches = append(matches, u)
	}

	hint := "pass user_id (or the exact name) as ask_human's recipient"
	if len(matches) == 0 {
		hint = "no active users match; drop filters or escalate via escalate_for"
	}
	payload := map[string]any{
		"total": len(matches),
		"hint":  hint,
	}
	cards := make([]contactCard, 0, len(matches))
	for _, u := range matches {
		if len(cards) == limit {
			payload["truncated"] = true
			break
		}
		cards = append(cards, contactCardFor(u, names))
	}
	payload["contacts"] = cards
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

// renderEscalation answers "who do I escalate to for this user": the nearest
// org ancestor level holding active users (excluding the user), falling back
// to active admins when the tree above is empty (docs/org-structure.md §4,
// docs/triggers-and-escalations.md §6).
func renderEscalation(users []WorkspaceUser, units []orgUnitLite, forUser string) string {
	contacts, unitID, reason := resolveEscalation(users, units, forUser)
	names := unitNames(units)
	payload := map[string]any{
		"escalate_for": strings.TrimSpace(forUser),
		"reason":       reason,
		"contacts":     make([]contactCard, 0, len(contacts)),
	}
	if unitID != "" {
		payload["escalation_unit"] = map[string]string{
			"id":   unitID,
			"name": names[unitID],
		}
	}
	cards := make([]contactCard, 0, len(contacts))
	for _, u := range contacts {
		cards = append(cards, contactCardFor(u, names))
	}
	payload["contacts"] = cards
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

// resolveEscalation is the pure escalation walk, unit-testable without HTTP:
// find the user (by id or name), walk ancestor levels bottom-up skipping the
// user's own unit, and return the first level with active users. Admins are
// the installation-wide fallback; an empty result means nobody is reachable.
func resolveEscalation(users []WorkspaceUser, units []orgUnitLite, forUser string) ([]WorkspaceUser, string, string) {
	forUser = strings.TrimSpace(forUser)
	byID := map[string]WorkspaceUser{}
	for _, u := range users {
		byID[u.ID] = u
		if u.Name != "" {
			byID[u.Name] = u
		}
	}
	target, found := byID[forUser]
	if !found {
		return nil, "", fmt.Sprintf("user %q not found in the workspace", forUser)
	}

	byUnit := map[string]orgUnitLite{}
	for _, unit := range units {
		byUnit[unit.ID] = unit
	}
	if target.OrgUnitID == "" {
		// No org placement: the installation admin is the steward of
		// unattached users (docs/org-structure.md §3.3) — the only sane
		// escalation target. Listing random users would be noise, not
		// escalation.
		if admins := filterRole(activeExcept(users, target.ID, rolePriority), "admin"); len(admins) > 0 {
			return admins, "", "user belongs to no org unit; installation admins are the escalation target"
		}
		return nil, "", "user belongs to no org unit and no active admins to escalate to"
	}

	unit, hasUnit := byUnit[target.OrgUnitID]
	if !hasUnit {
		// The user's unit vanished mid-run (docs/org-structure.md §34): fall
		// back to admins rather than guessing a path from stale data.
		if admins := filterRole(activeExcept(users, target.ID, rolePriority), "admin"); len(admins) > 0 {
			return admins, "", "user's org unit no longer exists; installation admins are the escalation target"
		}
		return nil, "", "user's org unit no longer exists and no active admins to escalate to"
	}

	// Bottom-up: the closest ancestor level with active people is the
	// "manager" — org trees do not always encode an explicit manager edge,
	// so proximity is the honest proxy. The candidate pool is computed once.
	candidates := activeExcept(users, target.ID, rolePriority)
	chain := unit.ancestors()
	for i := len(chain) - 2; i >= 0; i-- {
		level := chain[i]
		if inUnit := filterUnit(candidates, level); len(inUnit) > 0 {
			return inUnit, level, "nearest org level above the user's unit with active users"
		}
	}
	if admins := filterRole(candidates, "admin"); len(admins) > 0 {
		return admins, "", "no active users in org ancestors; installation admins are the escalation target"
	}
	return nil, "", "no active users above the user's unit and no active admins"
}

// rolePriority orders escalation candidates: admin, operator, writer, reader,
// then anything custom. Lower is more senior for sorting purposes.
func rolePriority(role string) int {
	switch role {
	case "admin":
		return 0
	case "operator":
		return 1
	case "writer":
		return 2
	case "reader":
		return 3
	}
	return 4
}

// activeExcept returns active users other than excludeID, sorted by role
// seniority then name — deterministic for a given directory snapshot.
func activeExcept(users []WorkspaceUser, excludeID string, priority func(string) int) []WorkspaceUser {
	result := make([]WorkspaceUser, 0, len(users))
	for _, u := range users {
		if u.Active && u.ID != excludeID {
			result = append(result, u)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		pi, pj := priority(result[i].Role), priority(result[j].Role)
		if pi != pj {
			return pi < pj
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func filterUnit(users []WorkspaceUser, unitID string) []WorkspaceUser {
	result := make([]WorkspaceUser, 0, len(users))
	for _, u := range users {
		if u.OrgUnitID == unitID {
			result = append(result, u)
		}
	}
	return result
}

func filterRole(users []WorkspaceUser, role string) []WorkspaceUser {
	result := make([]WorkspaceUser, 0, len(users))
	for _, u := range users {
		if u.Role == role {
			result = append(result, u)
		}
	}
	return result
}

// contactCardFor projects a workspace user into the agent-facing card:
// enabled channel types only (never addresses), web inbox always implied.
func contactCardFor(u WorkspaceUser, unitNames map[string]string) contactCard {
	channels := make([]string, 0, len(u.Channels))
	for _, channel := range u.Channels {
		if channel.Enabled && channel.Type != "" {
			channels = append(channels, channel.Type)
		}
	}
	card := contactCard{
		UserID:      u.ID,
		Name:        u.Name,
		Role:        u.Role,
		Channels:    channels,
		ViaWebInbox: true,
	}
	if u.Preferred != "" {
		card.Preferred = u.Preferred
	}
	if u.OrgUnitID != "" {
		card.OrgUnitID = u.OrgUnitID
		card.OrgUnit = unitNames[u.OrgUnitID]
	}
	if len(channels) == 0 {
		card.Channels = []string{}
	}
	return card
}

func unitNames(units []orgUnitLite) map[string]string {
	names := make(map[string]string, len(units))
	for _, unit := range units {
		names[unit.ID] = unit.Name
	}
	return names
}
