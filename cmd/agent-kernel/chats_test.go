package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

type fakeChatStore struct {
	mu    sync.Mutex
	chats map[string]workspace.Chat
}

func newFakeChatStore() *fakeChatStore {
	return &fakeChatStore{chats: map[string]workspace.Chat{}}
}

func (f *fakeChatStore) UpsertChat(_ context.Context, c *workspace.Chat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := workspace.ValidateChat(c.ID, c.ProjectID, c.Transcript); err != nil {
		return err
	}
	f.chats[c.ID] = *c
	return nil
}

func (f *fakeChatStore) GetChat(_ context.Context, id string) (workspace.Chat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.chats[id]
	if !ok {
		return workspace.Chat{}, workspace.ErrNotFound
	}
	return c, nil
}

func (f *fakeChatStore) ListChats(_ context.Context, projectID string, limit int, scope workspace.ChatScope) ([]workspace.Chat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []workspace.Chat
	for _, c := range f.chats {
		if c.ProjectID != projectID {
			continue
		}
		if !scope.All {
			owned := false
			for _, owner := range scope.Owners {
				if c.OwnerUserID == owner {
					owned = true
					break
				}
			}
			if !owned && !(scope.IncludeOwnerless && c.OwnerUserID == "") {
				continue
			}
		}
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeChatStore) DeleteChat(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.chats[id]; !ok {
		return workspace.ErrNotFound
	}
	delete(f.chats, id)
	return nil
}

func newChatServer(t *testing.T, store *fakeChatStore) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("writertoken:writer:writer:*;othertoken:otheruser:writer:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterChatRoutes(mux, gate, store)
	return httptest.NewServer(gate.Authenticate(mux))
}

func TestChatPersistenceRoundTrip(t *testing.T) {
	store := newFakeChatStore()
	server := newChatServer(t, store)

	body := `{"project_id":"omni","agent_id":"lead","task_id":"task-42","title":"Разбор инцидента","transcript":[{"role":"user","content":"почему упало"},{"role":"assistant","content":"вот трейс"}]}`
	req, _ := http.NewRequest(http.MethodPut, server.URL+"/v1/workspace/chats/conv-1", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer writertoken")
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// The list returns the stored transcript verbatim.
	list, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/chats?project=omni", nil)
	list.Header.Set("Authorization", "Bearer writertoken")
	page, err := server.Client().Do(list)
	require.NoError(t, err)
	defer page.Body.Close()
	require.Equal(t, 200, page.StatusCode)
	var payload struct {
		Chats []workspace.Chat `json:"chats"`
	}
	require.NoError(t, json.NewDecoder(page.Body).Decode(&payload))
	require.Len(t, payload.Chats, 1)
	require.Equal(t, "conv-1", payload.Chats[0].ID)
	require.Equal(t, "task-42", payload.Chats[0].TaskID, "the task link must survive the round trip — restored chats rejoin live-run state by it")
	require.Contains(t, string(payload.Chats[0].Transcript), "почему упало")

	// Delete closes the circle.
	del, _ := http.NewRequest(http.MethodDelete, server.URL+"/v1/workspace/chats/conv-1", nil)
	del.Header.Set("Authorization", "Bearer writertoken")
	dresp, err := server.Client().Do(del)
	require.NoError(t, err)
	defer dresp.Body.Close()
	require.Equal(t, 200, dresp.StatusCode)

	remaining, err := store.ListChats(context.Background(), "omni", 10, workspace.ChatScope{All: true})
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func TestChatUpsertValidation(t *testing.T) {
	store := newFakeChatStore()
	server := newChatServer(t, store)

	// Oversized transcript rejected (bound, not silent).
	big := `{"project_id":"omni","transcript":"` + strings.Repeat("x", workspace.TranscriptSizeLimit+10) + `"}`
	req, _ := http.NewRequest(http.MethodPut, server.URL+"/v1/workspace/chats/big", strings.NewReader(big))
	req.Header.Set("Authorization", "Bearer writertoken")
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 400, resp.StatusCode)

	// Anonymous (no token on an enabled gate) — 401, auth is the gate's job.
	anon, _ := http.NewRequest(http.MethodPut, server.URL+"/v1/workspace/chats/anon", strings.NewReader(`{"project_id":"omni"}`))
	aresp, err := server.Client().Do(anon)
	require.NoError(t, err)
	defer aresp.Body.Close()
	require.Equal(t, 401, aresp.StatusCode)
}

// Ownership scoping (migration 0025): the listing serves each user their
// OWN chats — the shared wall leaked everyone's conversations into every
// sidebar (the «чужие чаты в моём списке» report). Foreign writes and
// deletes are refused: a stray delete used to remove the chat for its true
// owner.
func TestChatOwnershipScoping(t *testing.T) {
	store := newFakeChatStore()
	server := newChatServer(t, store)
	defer server.Close()

	put := func(token, id, title string) *http.Response {
		t.Helper()
		body := `{"project_id":"omni","title":"` + title + `","transcript":[{"role":"user","content":"hi"}]}`
		req, _ := http.NewRequest(http.MethodPut, server.URL+"/v1/workspace/chats/"+id, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		require.NoError(t, err)
		return resp
	}
	list := func(token string) []workspace.Chat {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/workspace/chats?project=omni", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := server.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
		var payload struct {
			Chats []workspace.Chat `json:"chats"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
		return payload.Chats
	}

	// Two users create their chats in the same project.
	resp := put("writertoken", "mine-1", "writer chat")
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	resp = put("othertoken", "theirs-1", "other chat")
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// Each listing sees only its own chat.
	mine := list("writertoken")
	require.Len(t, mine, 1)
	require.Equal(t, "mine-1", mine[0].ID)
	require.Equal(t, "writer", mine[0].OwnerUserID, "the creator is stamped server-side")
	theirs := list("othertoken")
	require.Len(t, theirs, 1)
	require.Equal(t, "theirs-1", theirs[0].ID)

	// A foreign update is refused (no hijacking content or ownership).
	forbidden := put("othertoken", "mine-1", "hijacked")
	defer forbidden.Body.Close()
	require.Equal(t, 403, forbidden.StatusCode)

	// A foreign delete is refused — it used to remove the chat for the owner.
	del, _ := http.NewRequest(http.MethodDelete, server.URL+"/v1/workspace/chats/theirs-1", nil)
	del.Header.Set("Authorization", "Bearer writertoken")
	dresp, err := server.Client().Do(del)
	require.NoError(t, err)
	defer dresp.Body.Close()
	require.Equal(t, 403, dresp.StatusCode)

	// Both chats still stand for their owners.
	require.Len(t, list("writertoken"), 1)
	require.Len(t, list("othertoken"), 1)
}

// Ownerless legacy rows (owner_user_id = '' — env-token clients of the
// pre-fallback era) used to fold into EVERY admin's sidebar, re-importing
// foreign conversations after the ownership fix. An admin's default listing
// is own chats only; the unattributed pool is reachable through ?all=1.
func TestChatAdminListSkipsOwnerlessRows(t *testing.T) {
	store := newFakeChatStore()
	// Seed directly: an ownerless legacy row and another user's chat.
	store.chats["legacy-1"] = workspace.Chat{ID: "legacy-1", ProjectID: "omni", OwnerUserID: "", Title: "legacy"}
	store.chats["theirs-1"] = workspace.Chat{ID: "theirs-1", ProjectID: "omni", OwnerUserID: "otheruser", Title: "theirs"}

	gate, err := controlplane.NewGate("admintoken:adminuser:admin:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterChatRoutes(mux, gate, store)
	server := httptest.NewServer(gate.Authenticate(mux))
	defer server.Close()

	// The admin's own chat.
	putOwn := func() *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, server.URL+"/v1/workspace/chats/mine-1", strings.NewReader(`{"project_id":"omni","title":"mine","transcript":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer admintoken")
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		require.NoError(t, err)
		return resp
	}
	resp := putOwn()
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	list := func(all bool) []workspace.Chat {
		t.Helper()
		url := server.URL + "/v1/workspace/chats?project=omni"
		if all {
			url += "&all=1"
		}
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer admintoken")
		resp, err := server.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
		var payload struct {
			Chats []workspace.Chat `json:"chats"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
		return payload.Chats
	}

	ids := func(chats []workspace.Chat) []string {
		out := make([]string, 0, len(chats))
		for _, c := range chats {
			out = append(out, c.ID)
		}
		return out
	}

	// Default listing: the admin's own chat only — no legacy row, no foreign
	// conversation (the «чужой чат в моём списке» report).
	require.Equal(t, []string{"mine-1"}, ids(list(false)))

	// The debugging escape still reaches everything.
	require.ElementsMatch(t, []string{"mine-1", "legacy-1", "theirs-1"}, ids(list(true)))
}
