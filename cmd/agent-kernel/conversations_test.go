package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	kernelagent "github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

type memoryConversationStore struct {
	mu            sync.Mutex
	conversations map[string]workspace.Conversation
	messages      map[string][]workspace.ConversationMessage
	sources       map[string]string
	tasks         map[string][]workspace.ConversationTask
}

func newMemoryConversationStore() *memoryConversationStore {
	return &memoryConversationStore{
		conversations: map[string]workspace.Conversation{},
		messages:      map[string][]workspace.ConversationMessage{},
		sources:       map[string]string{},
		tasks:         map[string][]workspace.ConversationTask{},
	}
}

func (s *memoryConversationStore) CreateConversation(_ context.Context, c *workspace.Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.conversations[c.ID]; exists {
		return workspace.ErrConflict
	}
	c.Status = "active"
	c.ContextVersion = 1
	s.conversations[c.ID] = *c
	return nil
}

func (s *memoryConversationStore) GetConversation(_ context.Context, id string) (workspace.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conversations[id]
	if !ok {
		return workspace.Conversation{}, workspace.ErrNotFound
	}
	return c, nil
}

func (s *memoryConversationStore) BindConversationSource(context.Context, string, string, string, string) error {
	return nil
}

func (s *memoryConversationStore) AppendConversationMessage(_ context.Context, m *workspace.ConversationMessage) (workspace.ConversationMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conversations[m.ConversationID]
	if !ok {
		return workspace.ConversationMessage{}, false, workspace.ErrNotFound
	}
	if m.Source != "" && m.SourceMessageID != "" {
		key := m.OrganizationID + ":" + m.Source + ":" + m.SourceMessageID
		if id := s.sources[key]; id != "" {
			for _, existing := range s.messages[c.ID] {
				if existing.ID == id {
					return existing, true, nil
				}
			}
			return workspace.ConversationMessage{}, false, workspace.ErrConflict
		}
		s.sources[key] = m.ID
	}
	c.LastMessageSequence++
	s.conversations[c.ID] = c
	m.Sequence = c.LastMessageSequence
	m.Status = "persisted"
	s.messages[c.ID] = append(s.messages[c.ID], *m)
	return *m, false, nil
}

func (s *memoryConversationStore) ListConversationMessages(_ context.Context, conversationID string, after int64, limit int) ([]workspace.ConversationMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []workspace.ConversationMessage
	for _, m := range s.messages[conversationID] {
		if m.Sequence > after {
			result = append(result, m)
		}
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (s *memoryConversationStore) GetConversationMessage(_ context.Context, conversationID, messageID string) (workspace.ConversationMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, message := range s.messages[conversationID] {
		if message.ID == messageID {
			return message, nil
		}
	}
	return workspace.ConversationMessage{}, workspace.ErrNotFound
}

func (s *memoryConversationStore) TryAcquireConversationTurn(context.Context, string, int64) (func(), bool, error) {
	return func() {}, true, nil
}

func (s *memoryConversationStore) SetConversationMessageStatus(_ context.Context, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conversationID, messages := range s.messages {
		for i := range messages {
			if messages[i].ID == id {
				messages[i].Status = status
				s.messages[conversationID] = messages
				return nil
			}
		}
	}
	return workspace.ErrNotFound
}

func (s *memoryConversationStore) LinkConversationTask(context.Context, string, string, string, string, string, string) error {
	return nil
}

func (s *memoryConversationStore) ListConversationTasks(_ context.Context, conversationID string) ([]workspace.ConversationTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[conversationID], nil
}

func (s *memoryConversationStore) ListConversationInteractions(context.Context, string) ([]workspace.ConversationInteraction, error) {
	return nil, nil
}

func newConversationServer(t *testing.T, store *memoryConversationStore, runner conversationTurnRunner) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("writer:alice:writer:*;other:bob:writer:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterConversationRoutes(mux, gate, store, nil, runner)
	return httptest.NewServer(gate.Authenticate(mux))
}

func TestConversationAPIStoresAndAnswersDirectTurnIdempotently(t *testing.T) {
	store := newMemoryConversationStore()
	runs := 0
	server := newConversationServer(t, store, func(_ context.Context, _ workspace.Conversation, history []workspace.ConversationMessage, _ string) (string, error) {
		runs++
		require.Len(t, history, 1)
		return "Привет!", nil
	})
	defer server.Close()

	create, err := http.NewRequest(http.MethodPost, server.URL+"/v1/conversations", strings.NewReader(`{"id":"conv-1","agent_id":"lead"}`))
	require.NoError(t, err)
	create.Header.Set("Authorization", "Bearer writer")
	create.Header.Set("Content-Type", "application/json")
	created, err := server.Client().Do(create)
	require.NoError(t, err)
	created.Body.Close()
	require.Equal(t, http.StatusCreated, created.StatusCode)

	post := func() *http.Response {
		req, requestErr := http.NewRequest(http.MethodPost, server.URL+"/v1/conversations/conv-1/messages", strings.NewReader(`{"id":"client-msg-1","content":{"text":"Привет"}}`))
		require.NoError(t, requestErr)
		req.Header.Set("Authorization", "Bearer writer")
		req.Header.Set("Content-Type", "application/json")
		response, requestErr := server.Client().Do(req)
		require.NoError(t, requestErr)
		return response
	}
	first := post()
	defer first.Body.Close()
	require.Equal(t, http.StatusCreated, first.StatusCode)
	var firstBody struct {
		Message  workspace.ConversationMessage `json:"message"`
		Response workspace.ConversationMessage `json:"response"`
	}
	require.NoError(t, json.NewDecoder(first.Body).Decode(&firstBody))
	require.Equal(t, int64(1), firstBody.Message.Sequence)
	require.Equal(t, int64(2), firstBody.Response.Sequence)
	require.Equal(t, "client-msg-1", firstBody.Response.ParentMessageID)
	require.Equal(t, "Привет!", conversationMessageText(firstBody.Response.Content))

	second := post()
	defer second.Body.Close()
	require.Equal(t, http.StatusOK, second.StatusCode)
	require.Equal(t, 1, runs, "a retry must reuse the committed response")
}

func TestResolveConversationIntentSeparatesDirectAndTaskExecution(t *testing.T) {
	discussion := resolveConversationIntent("c-1", "What does this error mean?", false)
	require.Equal(t, "question", discussion.Intent)
	require.Equal(t, "direct", discussion.Execution)
	require.Equal(t, ConversationTarget{Type: "conversation", ID: "c-1"}, discussion.Target)

	task := resolveConversationIntent("c-1", "Пожалуйста, реализуй новый endpoint", false)
	require.Equal(t, "task_request", task.Intent)
	require.Equal(t, "agent_execution", task.Execution)

	attachment := resolveConversationIntent("c-1", "", true)
	require.Equal(t, "agent_execution", attachment.Execution)
}

func TestConversationTokenStreamRequiresOwnedHumanMessageAndReplaysCompletion(t *testing.T) {
	store := newMemoryConversationStore()
	require.NoError(t, store.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-stream", OwnerID: "alice"}))
	_, _, err := store.AppendConversationMessage(context.Background(), &workspace.ConversationMessage{
		ID: "human-stream", ConversationID: "conv-stream", SenderType: "human", Content: json.RawMessage(`{"text":"hello"}`),
	})
	require.NoError(t, err)
	tokens := kernelagent.NewTokenBus()
	tokens.Publish(conversationTokenStreamKey("conv-stream", "human-stream"), 1, "hello", "")
	tokens.Close(conversationTokenStreamKey("conv-stream", "human-stream"))
	gate, err := controlplane.NewGate("writer:alice:writer:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterConversationRoutes(mux, gate, store, tokens, nil)
	server := httptest.NewServer(gate.Authenticate(mux))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/conversations/conv-stream/messages/human-stream/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer writer")
	response, err := server.Client().Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, string(body), "event: done")
}

func TestConversationAPIReturnsForbiddenForAnotherOwner(t *testing.T) {
	store := newMemoryConversationStore()
	server := newConversationServer(t, store, nil)
	defer server.Close()

	create, err := http.NewRequest(http.MethodPost, server.URL+"/v1/conversations", strings.NewReader(`{"id":"private","agent_id":"lead"}`))
	require.NoError(t, err)
	create.Header.Set("Authorization", "Bearer writer")
	create.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(create)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)

	read, err := http.NewRequest(http.MethodGet, server.URL+"/v1/conversations/private", nil)
	require.NoError(t, err)
	read.Header.Set("Authorization", "Bearer other")
	response, err = server.Client().Do(read)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusForbidden, response.StatusCode)
}
