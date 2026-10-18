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

func (f *fakeChatStore) ListChats(_ context.Context, projectID string, limit int) ([]workspace.Chat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []workspace.Chat
	for _, c := range f.chats {
		if c.ProjectID == projectID {
			out = append(out, c)
		}
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
	gate, err := controlplane.NewGate("writertoken:writer:writer:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterChatRoutes(mux, gate, store)
	return httptest.NewServer(gate.Authenticate(mux))
}

func TestChatPersistenceRoundTrip(t *testing.T) {
	store := newFakeChatStore()
	server := newChatServer(t, store)

	body := `{"project_id":"omni","agent_id":"lead","title":"Разбор инцидента","transcript":[{"role":"user","content":"почему упало"},{"role":"assistant","content":"вот трейс"}]}`
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
	require.Contains(t, string(payload.Chats[0].Transcript), "почему упало")

	// Delete closes the circle.
	del, _ := http.NewRequest(http.MethodDelete, server.URL+"/v1/workspace/chats/conv-1", nil)
	del.Header.Set("Authorization", "Bearer writertoken")
	dresp, err := server.Client().Do(del)
	require.NoError(t, err)
	defer dresp.Body.Close()
	require.Equal(t, 200, dresp.StatusCode)

	remaining, err := store.ListChats(context.Background(), "omni", 10)
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
