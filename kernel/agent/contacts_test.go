package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// contactsStub serves both endpoints human_contacts reads: the user
// directory and the org tree.
func contactsStub(t *testing.T, usersJSON, unitsJSON string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/workspace/users" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(usersJSON))
		case r.URL.Path == "/v1/org/units" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(unitsJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func contactsUsers() string {
	return `{"users":[
		{"id":"u-admin","name":"Ada Admin","role":"admin","active":true,"org_unit_id":"acme",
		 "channels":[{"type":"matrix","address":"!a:mx.org","enabled":true},{"type":"telegram","address":"1","enabled":false}],"preferred_channel":"matrix"},
		{"id":"u-lead","name":"Lev Lead","role":"operator","active":true,"org_unit_id":"dev",
		 "channels":[{"type":"telegram","address":"2","enabled":true}],"preferred_channel":"telegram"},
		{"id":"u-qa","name":"Quinn QA","role":"writer","active":true,"org_unit_id":"platform",
		 "channels":[],"preferred_channel":""},
		{"id":"u-dev","name":"Dev Person","role":"writer","active":true,"org_unit_id":"platform"},
		{"id":"u-gone","name":"Gone User","role":"writer","active":false,"org_unit_id":"platform"},
		{"id":"u-free","name":"Free Floater","role":"reader","active":true}
	]}`
}

func contactsUnits() string {
	return `{"units":[
		{"id":"acme","kind":"organization","name":"Acme","path":""},
		{"id":"dev","kind":"department","name":"Development","path":"acme"},
		{"id":"platform","kind":"team","name":"Platform","path":"acme.dev"},
		{"id":"labs","kind":"team","name":"Labs","path":"acme.dev"}
	]}`
}

func decodeContacts(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(content), &payload))
	return payload
}

func contactIDs(payload map[string]any) []string {
	cards, _ := payload["contacts"].([]any)
	ids := make([]string, 0, len(cards))
	for _, card := range cards {
		entry, _ := card.(map[string]any)
		ids = append(ids, entry["user_id"].(string))
	}
	return ids
}

func TestHumanContactsSearchFiltersAndHidesAddresses(t *testing.T) {
	server := contactsStub(t, contactsUsers(), contactsUnits())
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	// Substring search matches names, not only ids.
	result, err := activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{"query": "quinn"},
	})
	require.NoError(t, err)
	payload := decodeContacts(t, result.Content)
	require.EqualValues(t, 1, payload["total"])
	cards := payload["contacts"].([]any)
	card := cards[0].(map[string]any)
	require.Equal(t, "u-qa", card["user_id"])
	require.Equal(t, "Platform", card["org_unit"])
	// Channel privacy: types only, never addresses; disabled channels drop out.
	require.Equal(t, []any{}, card["channels"])
	require.True(t, card["via_web_inbox"].(bool))

	// Role filter.
	result, _ = activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{"role": "admin"},
	})
	require.Equal(t, []string{"u-admin"}, contactIDs(decodeContacts(t, result.Content)))

	// Org unit filter is exact (u-gone is inactive and must not appear).
	result, _ = activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{"org_unit_id": "platform"},
	})
	require.Equal(t, []string{"u-qa", "u-dev"}, contactIDs(decodeContacts(t, result.Content)))

	// Enabled channel types are listed without addresses.
	result, _ = activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{"query": "ada"},
	})
	card = decodeContacts(t, result.Content)["contacts"].([]any)[0].(map[string]any)
	require.Equal(t, []any{"matrix"}, card["channels"])
	require.Equal(t, "matrix", card["preferred_channel"])
	require.NotContains(t, result.Content, "!a:mx.org")

	// No matches is a hint, not an error.
	result, _ = activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{"query": "nobody"},
	})
	payload = decodeContacts(t, result.Content)
	require.EqualValues(t, 0, payload["total"])
	require.Contains(t, payload["hint"], "no active users match")
}

func TestHumanContactsEscalationWalksAncestors(t *testing.T) {
	server := contactsStub(t, contactsUsers(), contactsUnits())
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	// Escalating for the platform engineer finds the closest ancestor level
	// with active people: acme.dev (Lev Lead), not the org-wide admin.
	result, err := activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{"escalate_for": "u-dev"},
	})
	require.NoError(t, err)
	payload := decodeContacts(t, result.Content)
	require.Equal(t, []string{"u-lead"}, contactIDs(payload))
	unit := payload["escalation_unit"].(map[string]any)
	require.Equal(t, "dev", unit["id"])
	require.Equal(t, "Development", unit["name"])

	// Escalating for the department lead skips to the organization level.
	result, _ = activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{"escalate_for": "Lev Lead"},
	})
	payload = decodeContacts(t, result.Content)
	require.Equal(t, []string{"u-admin"}, contactIDs(payload))

	// A user with no org placement escalates to installation admins.
	result, _ = activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{"escalate_for": "u-free"},
	})
	payload = decodeContacts(t, result.Content)
	require.Equal(t, []string{"u-admin"}, contactIDs(payload))
	require.Contains(t, payload["reason"], "no org unit")
}

func TestResolveEscalationDegenerateCases(t *testing.T) {
	users := []WorkspaceUser{
		{ID: "u-1", Name: "One", Role: "writer", Active: true, OrgUnitID: "t"},
		{ID: "u-2", Name: "Two", Role: "reader", Active: true, OrgUnitID: "t"},
	}
	units := []orgUnitLite{{ID: "t", Path: "org"}}

	// Unknown user: no candidates, explanatory reason.
	contacts, unitID, reason := resolveEscalation(users, units, "ghost")
	require.Empty(t, contacts)
	require.Empty(t, unitID)
	require.Contains(t, reason, "not found")

	// The org level above is empty and there are no admins: nothing to
	// escalate to, stated honestly.
	contacts, _, reason = resolveEscalation(users, units, "u-1")
	require.Empty(t, contacts)
	require.Contains(t, reason, "no active admins")

	// With an admin in the directory the same walk falls back to them.
	users = append(users, WorkspaceUser{ID: "u-admin", Name: "Root", Role: "admin", Active: true, OrgUnitID: ""})
	contacts, unitID, reason = resolveEscalation(users, units, "u-1")
	require.Equal(t, []string{"u-admin"}, ids(contacts))
	require.Empty(t, unitID)
	require.Contains(t, reason, "installation admins")

	// The target's unit vanished mid-run: admins, never stale paths.
	contacts, _, reason = resolveEscalation(users, []orgUnitLite{}, "u-1")
	require.Equal(t, []string{"u-admin"}, ids(contacts))
	require.Contains(t, reason, "no longer exists")

	// Role seniority orders candidates within a level: admin first.
	senior := []WorkspaceUser{
		{ID: "u-w", Name: "W", Role: "writer", Active: true, OrgUnitID: "t"},
		{ID: "u-o", Name: "O", Role: "operator", Active: true, OrgUnitID: "t"},
	}
	ordered := activeExcept(senior, "", rolePriority)
	require.Equal(t, []string{"u-o", "u-w"}, ids(ordered))
}

func ids(users []WorkspaceUser) []string {
	result := make([]string, 0, len(users))
	for _, u := range users {
		result = append(result, u.ID)
	}
	return result
}

func TestHumanContactsDegradesWithoutOrgTree(t *testing.T) {
	// The org endpoint is down: search still works (unit names drop out),
	// escalation reports the outage instead of guessing.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/workspace/users" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(contactsUsers()))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{"query": "quinn"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"u-qa"}, contactIDs(decodeContacts(t, result.Content)))

	result, _ = activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{"escalate_for": "u-dev"},
	})
	require.Contains(t, result.Content, "error: org structure is unavailable")
}

func TestHumanContactsCapsLargeDirectories(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`{"users":[`)
	for i := 0; i < 80; i++ {
		if i > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(`{"id":"u-` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `","name":"User ` + string(rune('A'+i%26)) + string(rune('A'+i/26)) + `","role":"writer","active":true,"org_unit_id":"t"}`)
	}
	builder.WriteString(`]}`)
	server := contactsStub(t, builder.String(), contactsUnits())
	activities := &Activities{HTTP: server.Client(), WorkspaceURL: server.URL, WorkspaceToken: "internal"}

	result, err := activities.handleHumanContacts(context.Background(), ToolRequest{
		Name: "human_contacts", Arguments: map[string]any{},
	})
	require.NoError(t, err)
	payload := decodeContacts(t, result.Content)
	require.EqualValues(t, 80, payload["total"])
	require.Len(t, payload["contacts"].([]any), 50)
	require.Equal(t, true, payload["truncated"])
}
