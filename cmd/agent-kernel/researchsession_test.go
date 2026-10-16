package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// fakeResearchSessionStore satisfies the router's storage surface without a
// database; ids are assigned sequentially like the BIGSERIAL column.
type fakeResearchSessionStore struct {
	mu       sync.Mutex
	nextID   int64
	sessions map[int64]workspace.ResearchSession
	audits   []storedAudit
}

func newFakeResearchSessionStore() *fakeResearchSessionStore {
	return &fakeResearchSessionStore{sessions: map[int64]workspace.ResearchSession{}}
}

func (f *fakeResearchSessionStore) CreateResearchSession(_ context.Context, session *workspace.ResearchSession) error {
	if err := workspace.ValidateResearchSession(session.Project, session.Title, session.Payload); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	session.ID = f.nextID
	f.sessions[session.ID] = *session
	return nil
}

func (f *fakeResearchSessionStore) GetResearchSession(_ context.Context, id int64) (workspace.ResearchSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[id]
	if !ok {
		return workspace.ResearchSession{}, workspace.ErrNotFound
	}
	return session, nil
}

func (f *fakeResearchSessionStore) ListResearchSessions(_ context.Context, project string, limit int) ([]workspace.ResearchSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	result := []workspace.ResearchSession{}
	for id := int64(1); id <= f.nextID && len(result) < limit; id++ {
		if session, ok := f.sessions[id]; ok && session.Project == project {
			result = append(result, session)
		}
	}
	return result, nil
}

func (f *fakeResearchSessionStore) UpdateResearchSession(_ context.Context, id int64, title string, payload map[string]any) (workspace.ResearchSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[id]
	if !ok {
		return workspace.ResearchSession{}, workspace.ErrNotFound
	}
	session.Title = title
	session.Payload = payload
	f.sessions[id] = session
	return session, nil
}

func (f *fakeResearchSessionStore) DeleteResearchSession(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[id]; !ok {
		return workspace.ErrNotFound
	}
	delete(f.sessions, id)
	return nil
}

func (f *fakeResearchSessionStore) RecordAccessAudit(_ context.Context, actor, action, entityKind, entityID string, details map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, storedAudit{actor: actor, action: action, entityKind: entityKind, entityID: entityID, details: details})
	return nil
}

func newResearchSessionServer(t *testing.T, store *fakeResearchSessionStore) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*\nadmintoken:admin:admin:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterResearchSessionRoutes(mux, gate, store)
	return httptest.NewServer(gate.Authenticate(mux))
}

func researchSessionDo(t *testing.T, server *httptest.Server, method, token, path, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(raw)
}

// A writer's create persists the session and answers 201 with the stored row;
// the audit trail names the actor and the session id.
func TestResearchSessionCreateAnswers201(t *testing.T) {
	store := newFakeResearchSessionStore()
	server := newResearchSessionServer(t, store)
	defer server.Close()

	status, body := researchSessionDo(t, server, http.MethodPost, "writertoken", "/v1/workspace/research-sessions",
		`{"project":"demo","title":"Миграции","payload":{"version":1,"question":"как делать миграции","notes":""}}`)
	require.Equal(t, 201, status)
	require.Contains(t, body, `"title":"Миграции"`)
	require.Contains(t, body, `"id":1`)
	require.Len(t, store.audits, 1)
	require.Equal(t, workspace.AuditResearchSessionCreated, store.audits[0].action)
	require.Equal(t, "writer", store.audits[0].actor)
}

// Readers may list and read but never write; a missing title or project is a
// 422, not a half-saved session.
func TestResearchSessionRBACAndValidation(t *testing.T) {
	store := newFakeResearchSessionStore()
	server := newResearchSessionServer(t, store)
	defer server.Close()

	status, _ := researchSessionDo(t, server, http.MethodPost, "readertoken", "/v1/workspace/research-sessions",
		`{"project":"demo","title":"x","payload":{}}`)
	require.Equal(t, 403, status)

	status, _ = researchSessionDo(t, server, http.MethodPost, "writertoken", "/v1/workspace/research-sessions",
		`{"project":"demo","title":"  ","payload":{}}`)
	require.Equal(t, 422, status)

	status, _ = researchSessionDo(t, server, http.MethodPost, "writertoken", "/v1/workspace/research-sessions",
		`{"project":"","title":"x","payload":{}}`)
	require.Equal(t, 422, status)

	status, _ = researchSessionDo(t, server, http.MethodPost, "writertoken", "/v1/workspace/research-sessions", `{`)
	require.Equal(t, 400, status)
}

// List requires a project and returns the project's sessions only.
func TestResearchSessionListScopedByProject(t *testing.T) {
	store := newFakeResearchSessionStore()
	server := newResearchSessionServer(t, store)
	defer server.Close()

	require.NoError(t, store.CreateResearchSession(context.Background(), &workspace.ResearchSession{Project: "demo", Title: "a"}))
	require.NoError(t, store.CreateResearchSession(context.Background(), &workspace.ResearchSession{Project: "other", Title: "b"}))

	status, _ := researchSessionDo(t, server, http.MethodGet, "readertoken", "/v1/workspace/research-sessions", "")
	require.Equal(t, 400, status)

	status, body := researchSessionDo(t, server, http.MethodGet, "readertoken", "/v1/workspace/research-sessions?project=demo", "")
	require.Equal(t, 200, status)
	require.Contains(t, body, `"title":"a"`)
	require.NotContains(t, body, `"title":"b"`)
}

// Get/update/delete resolve by numeric id: 404 for missing rows, 400 for
// malformed ones, and the update writes an audit record.
func TestResearchSessionGetUpdateDelete(t *testing.T) {
	store := newFakeResearchSessionStore()
	server := newResearchSessionServer(t, store)
	defer server.Close()

	created := workspace.ResearchSession{Project: "demo", Title: "first", Payload: map[string]any{"version": 1}}
	require.NoError(t, store.CreateResearchSession(context.Background(), &created))

	status, body := researchSessionDo(t, server, http.MethodGet, "readertoken", "/v1/workspace/research-sessions/"+strconv.FormatInt(created.ID, 10), "")
	require.Equal(t, 200, status)
	require.Contains(t, body, `"payload":{"version":1}`)

	status, body = researchSessionDo(t, server, http.MethodPut, "writertoken", "/v1/workspace/research-sessions/"+strconv.FormatInt(created.ID, 10),
		`{"title":"second","payload":{"version":1,"conclusion":"done"}}`)
	require.Equal(t, 200, status)
	require.Contains(t, body, `"title":"second"`)
	require.Equal(t, workspace.AuditResearchSessionUpdated, store.audits[len(store.audits)-1].action)

	status, _ = researchSessionDo(t, server, http.MethodPut, "readertoken", "/v1/workspace/research-sessions/"+strconv.FormatInt(created.ID, 10), `{"title":"nope"}`)
	require.Equal(t, 403, status)

	status, _ = researchSessionDo(t, server, http.MethodDelete, "writertoken", "/v1/workspace/research-sessions/999", "")
	require.Equal(t, 404, status)

	status, _ = researchSessionDo(t, server, http.MethodGet, "readertoken", "/v1/workspace/research-sessions/not-a-number", "")
	require.Equal(t, 400, status)

	status, body = researchSessionDo(t, server, http.MethodDelete, "writertoken", "/v1/workspace/research-sessions/"+strconv.FormatInt(created.ID, 10), "")
	require.Equal(t, 200, status)
	require.Contains(t, body, `"deleted":true`)
	require.Equal(t, workspace.AuditResearchSessionDeleted, store.audits[len(store.audits)-1].action)
}
