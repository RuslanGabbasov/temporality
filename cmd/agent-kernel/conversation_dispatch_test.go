package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// fakeConversationInboundStore wraps the legacy inbound fake with the
// conversation-runtime surfaces. It must stay a DISTINCT type from
// fakeInboundStore: adding conversation methods to the legacy fake would
// silently route the intake regression suite through the runtime-first
// dispatch instead of the intake pipeline it pins.
type fakeConversationInboundStore struct {
	*fakeInboundStore

	mu            sync.Mutex
	conversations map[string]workspace.Conversation
	bindings      map[string]string // "channel\x00chatID" -> conversation id
	messages      map[string][]workspace.ConversationMessage
	agents        map[string]workspace.Agent
	tasks         map[string][]workspace.ConversationTask
	outbox        []*workspace.ConversationOutboxEvent
	statusLog     []string
	shadow        []*workspace.RouterShadowRow
}

func newFakeConversationInboundStore() *fakeConversationInboundStore {
	return &fakeConversationInboundStore{
		fakeInboundStore: &fakeInboundStore{},
		conversations:    map[string]workspace.Conversation{},
		bindings:         map[string]string{},
		messages:         map[string][]workspace.ConversationMessage{},
		agents:           map[string]workspace.Agent{},
		tasks:            map[string][]workspace.ConversationTask{},
	}
}

func (s *fakeConversationInboundStore) CreateConversation(_ context.Context, c *workspace.Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.conversations[c.ID]; exists {
		return workspace.ErrConflict
	}
	c.Status = "active"
	s.conversations[c.ID] = *c
	return nil
}

func (s *fakeConversationInboundStore) GetConversation(_ context.Context, id string) (workspace.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conversations[id]
	if !ok {
		return workspace.Conversation{}, workspace.ErrNotFound
	}
	return c, nil
}

func (s *fakeConversationInboundStore) BindConversationSource(_ context.Context, _, source, chatID, conversationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := source + "\x00" + chatID
	if existing, ok := s.bindings[key]; ok && existing != conversationID {
		return workspace.ErrConflict
	}
	s.bindings[key] = conversationID
	return nil
}

func (s *fakeConversationInboundStore) ResolveConversationSource(_ context.Context, _, source, chatID string) (workspace.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.bindings[source+"\x00"+chatID]
	if !ok {
		return workspace.Conversation{}, workspace.ErrNotFound
	}
	return s.conversations[id], nil
}

func (s *fakeConversationInboundStore) ConversationTransportSource(_ context.Context, conversationID string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, id := range s.bindings {
		if id == conversationID {
			parts := strings.SplitN(key, "\x00", 2)
			return parts[0], parts[1], nil
		}
	}
	return "", "", workspace.ErrNotFound
}

func (s *fakeConversationInboundStore) AddConversationParticipant(context.Context, string, string, string) error {
	return nil
}

func (s *fakeConversationInboundStore) RecordRouterShadow(_ context.Context, row *workspace.RouterShadowRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shadow = append(s.shadow, row)
	return nil
}

func (s *fakeConversationInboundStore) AppendConversationMessage(_ context.Context, m *workspace.ConversationMessage) (workspace.ConversationMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.messages[m.ConversationID]
	for _, existing := range list {
		if existing.Source == m.Source && existing.SourceMessageID == m.SourceMessageID && m.SourceMessageID != "" {
			return existing, false, nil
		}
	}
	m.Sequence = int64(len(list) + 1)
	list = append(list, *m)
	s.messages[m.ConversationID] = list
	return *m, true, nil
}

func (s *fakeConversationInboundStore) AppendConversationMessageWithOutbox(_ context.Context, m *workspace.ConversationMessage, event *workspace.ConversationOutboxEvent) (workspace.ConversationMessage, bool, error) {
	appended, inserted, err := s.AppendConversationMessage(context.Background(), m)
	if err == nil && inserted && event != nil {
		s.mu.Lock()
		s.outbox = append(s.outbox, event)
		s.mu.Unlock()
	}
	return appended, inserted, err
}

func (s *fakeConversationInboundStore) ListConversationMessages(_ context.Context, conversationID string, after int64, limit int) ([]workspace.ConversationMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.messages[conversationID]
	result := make([]workspace.ConversationMessage, 0, len(list))
	for _, m := range list {
		if m.Sequence > after {
			result = append(result, m)
		}
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (s *fakeConversationInboundStore) TryAcquireConversationTurn(context.Context, string, int64) (func(), bool, error) {
	return func() {}, true, nil
}

func (s *fakeConversationInboundStore) SetConversationMessageStatus(_ context.Context, id, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conversationID, list := range s.messages {
		for i := range list {
			if list[i].ID == id {
				s.messages[conversationID][i].Status = status
				s.statusLog = append(s.statusLog, id+"->"+status)
				return nil
			}
		}
	}
	return workspace.ErrNotFound
}

func (s *fakeConversationInboundStore) GetAgent(_ context.Context, id string) (workspace.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return workspace.Agent{}, workspace.ErrNotFound
	}
	return a, nil
}

func (s *fakeConversationInboundStore) LinkConversationTask(_ context.Context, conversationID, originMessageID, taskID, runID, projectID, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[conversationID] = append(s.tasks[conversationID], workspace.ConversationTask{
		OriginMessageID: originMessageID, TaskID: taskID, RunID: runID, ProjectID: projectID, Status: status,
	})
	return nil
}

func (s *fakeConversationInboundStore) FindConversationTaskByRunID(_ context.Context, runID string) (workspace.ConversationTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, list := range s.tasks {
		for _, task := range list {
			if task.RunID == runID {
				return task, nil
			}
		}
	}
	return workspace.ConversationTask{}, workspace.ErrNotFound
}

func (s *fakeConversationInboundStore) ListConversationTasks(_ context.Context, conversationID string) ([]workspace.ConversationTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks[conversationID], nil
}

func (s *fakeConversationInboundStore) AttachConversationLead(context.Context, string, string, string) error {
	return nil
}

func (s *fakeConversationInboundStore) EnqueueConversationOutboxEvent(_ context.Context, e *workspace.ConversationOutboxEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outbox = append(s.outbox, e)
	return nil
}

func (s *fakeConversationInboundStore) lastStatus(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.statusLog {
		if strings.HasPrefix(entry, id+"->") {
			return strings.TrimPrefix(entry, id+"->")
		}
	}
	return ""
}

func (s *fakeConversationInboundStore) enqueuedOutbox() []*workspace.ConversationOutboxEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*workspace.ConversationOutboxEvent(nil), s.outbox...)
}

func newConversationDispatchLoop(t *testing.T, server *httptest.Server, store *fakeConversationInboundStore, turn func(ctx context.Context, conversation workspace.Conversation, history []workspace.ConversationMessage, tokenKey string, directives []string) (string, error)) *channelInboundLoop {
	t.Helper()
	driver := matrixTestDriver(t)
	return &channelInboundLoop{
		driver:     driver,
		poller:     driver.Poller(),
		httpClient: server.Client(),
		settings: func(context.Context) (agent.ChannelSettings, error) {
			return agent.ChannelSettings{Channels: map[string]map[string]string{
				"matrix": {"homeserver": server.URL, "access_token": "tok"},
			}}, nil
		},
		store:    store,
		sourceID: "src",
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		turn:     turn,
	}
}

func dispatchMessage(text string) (agent.InboundMessage, workspace.InboundMessage) {
	message := agent.InboundMessage{Channel: "matrix", ID: "$m1", Sender: "@human:hs", ChatID: "!r1:hs", Text: text}
	row := workspace.InboundMessage{ID: workspace.InboundMessageID("matrix", "$m1"), Channel: "matrix", MessageID: "$m1", SenderRaw: "@human:hs", Room: "!r1:hs", SenderUserID: "u1"}
	return message, row
}

func TestChannelDispatchDirectTurnDeliversThroughOutbox(t *testing.T) {
	store := newFakeConversationInboundStore()
	require.NoError(t, store.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-1", AgentID: "agent-1", OwnerID: "u1"}))
	require.NoError(t, store.BindConversationSource(context.Background(), "", "matrix", "!r1:hs", "conv-1"))
	store.agents["agent-1"] = workspace.Agent{ID: "agent-1", Enabled: true}
	var turnDirectives []string
	var turnText string
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, nil)
	loop := newConversationDispatchLoop(t, server, store, func(_ context.Context, _ workspace.Conversation, _ []workspace.ConversationMessage, _ string, directives []string) (string, error) {
		turnDirectives = directives
		turnText = "Готов ответить по существу"
		return turnText, nil
	})
	message, row := dispatchMessage("как продвигается сборка?")
	messageID := stableConversationMessageID("matrix", "!r1:hs", "$m1")
	// The persisted human message precedes the dispatch — production appends
	// it in persistChannelMessage before routing.
	_, _, _ = store.AppendConversationMessage(context.Background(), &workspace.ConversationMessage{
		ID: messageID, ConversationID: "conv-1", SenderType: "human", Status: "received",
	})
	consumed := loop.dispatchConversationMessage(context.Background(), nil, message, "u1", &row)
	require.True(t, consumed, "a plain message in a talk with a lead must be consumed by the conversation runtime")
	waitFor(t, 2*time.Second, func() bool { return store.lastStatus(messageID) == "completed" })
	require.Empty(t, turnDirectives, "direct turn carries no clarify directive")
	events := store.enqueuedOutbox()
	require.Len(t, events, 1, "the answer must ride the durable outbox")
	require.Equal(t, workspace.OutboxConversationMessage, events[0].EventType)
	var payload conversationMessagePayload
	require.NoError(t, json.Unmarshal(events[0].Payload, &payload))
	require.Equal(t, "matrix", payload.Channel)
	require.Equal(t, "!r1:hs", payload.Address)
	require.Equal(t, turnText, payload.Text)
	if latest, _ := store.lastUpdate(row.ID); latest.Status != workspace.InboundStatusAnswered {
		t.Fatalf("inbound row not answered: %+v", latest)
	}
}

func TestChannelDispatchTaskRequestStaysWithIntake(t *testing.T) {
	store := newFakeConversationInboundStore()
	require.NoError(t, store.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-1", AgentID: "agent-1", OwnerID: "u1"}))
	require.NoError(t, store.BindConversationSource(context.Background(), "", "matrix", "!r1:hs", "conv-1"))
	store.agents["agent-1"] = workspace.Agent{ID: "agent-1", Enabled: true}
	turnCalled := false
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, nil)
	loop := newConversationDispatchLoop(t, server, store, func(context.Context, workspace.Conversation, []workspace.ConversationMessage, string, []string) (string, error) {
		turnCalled = true
		return "should not happen", nil
	})
	message, row := dispatchMessage("реализуй выгрузку в формате parquet")
	consumed := loop.dispatchConversationMessage(context.Background(), nil, message, "u1", &row)
	require.False(t, consumed, "consequential work must stay on the intake dispatch")
	require.False(t, turnCalled)
	require.Empty(t, store.enqueuedOutbox())
	waitFor(t, 500*time.Millisecond, func() bool { return turnCalled })
	require.False(t, turnCalled)
	_ = row
}

func TestChannelDispatchClarificationSendsDirective(t *testing.T) {
	store := newFakeConversationInboundStore()
	require.NoError(t, store.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-1", AgentID: "agent-1", OwnerID: "u1"}))
	require.NoError(t, store.BindConversationSource(context.Background(), "", "matrix", "!r1:hs", "conv-1"))
	store.agents["agent-1"] = workspace.Agent{ID: "agent-1", Enabled: true, Model: "test-model"}
	var turnDirectives []string
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, nil)
	loop := newConversationDispatchLoop(t, server, store, func(_ context.Context, _ workspace.Conversation, _ []workspace.ConversationMessage, _ string, directives []string) (string, error) {
		turnDirectives = directives
		return "Уточни, пожалуйста: о чём речь?", nil
	})
	// The fast-model stage classifies the vague message below the clarify
	// threshold: the runtime asks instead of guessing.
	loop.classifyFor = func(workspace.Conversation, workspace.Agent) ConversationMessageClassifier {
		return classifyOnce(ConversationMessageClassifyOutput{Intent: "unknown", Execution: "clarification", Confidence: 0.3}, nil)
	}
	message, row := dispatchMessage("ну, ты понял, о чём я")
	messageID := stableConversationMessageID("matrix", "!r1:hs", "$m1")
	_, _, _ = store.AppendConversationMessage(context.Background(), &workspace.ConversationMessage{
		ID: messageID, ConversationID: "conv-1", SenderType: "human", Status: "received",
	})
	consumed := loop.dispatchConversationMessage(context.Background(), nil, message, "u1", &row)
	require.True(t, consumed)
	waitFor(t, 2*time.Second, func() bool { return store.lastStatus(messageID) == "completed" })
	require.Len(t, turnDirectives, 1)
	require.Equal(t, conversationClarifyDirective, turnDirectives[0])
	_ = row
}

// TestChannelDispatchRouterShadow pins the §23 shadow scoring on every
// dispatch branch: a served direct turn records runtime_served, a declined
// task request records the scored decision for the intake-verdict patch, an
// unbound chat scores conversation-less, and the channel opt-out silences
// the recorder.
func TestChannelDispatchRouterShadow(t *testing.T) {
	t.Run("served direct turn", func(t *testing.T) {
		store := newFakeConversationInboundStore()
		require.NoError(t, store.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-1", AgentID: "agent-1", OwnerID: "u1"}))
		require.NoError(t, store.BindConversationSource(context.Background(), "", "matrix", "!r1:hs", "conv-1"))
		store.agents["agent-1"] = workspace.Agent{ID: "agent-1", Enabled: true}
		server := matrixIntakeHomeServer(t, "!r1:hs", nil, nil)
		loop := newConversationDispatchLoop(t, server, store, func(context.Context, workspace.Conversation, []workspace.ConversationMessage, string, []string) (string, error) {
			return "ответ", nil
		})
		message, row := dispatchMessage("как дела с экспортом?")
		// The persisted human message precedes the dispatch — production
		// appends it in persistChannelMessage before routing.
		_, _, _ = store.AppendConversationMessage(context.Background(), &workspace.ConversationMessage{
			ID: stableConversationMessageID("matrix", "!r1:hs", "$m1"), ConversationID: "conv-1", SenderType: "human", Status: "received",
		})
		require.True(t, loop.dispatchConversationMessage(context.Background(), nil, message, "u1", &row))
		require.Len(t, store.shadow, 1)
		require.True(t, store.shadow[0].RuntimeServed)
		require.Equal(t, "direct", store.shadow[0].RuntimeExecution)
		require.Equal(t, "conv-1", store.shadow[0].ConversationID)
	})
	t.Run("declined task request scores for the verdict", func(t *testing.T) {
		store := newFakeConversationInboundStore()
		require.NoError(t, store.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-1", AgentID: "agent-1", OwnerID: "u1"}))
		require.NoError(t, store.BindConversationSource(context.Background(), "", "matrix", "!r1:hs", "conv-1"))
		store.agents["agent-1"] = workspace.Agent{ID: "agent-1", Enabled: true}
		server := matrixIntakeHomeServer(t, "!r1:hs", nil, nil)
		loop := newConversationDispatchLoop(t, server, store, func(context.Context, workspace.Conversation, []workspace.ConversationMessage, string, []string) (string, error) {
			t.Fatal("task request must not run a direct turn")
			return "", nil
		})
		message, row := dispatchMessage("исследуй флейк на матрице")
		require.False(t, loop.dispatchConversationMessage(context.Background(), nil, message, "u1", &row))
		require.Len(t, store.shadow, 1)
		require.False(t, store.shadow[0].RuntimeServed)
		require.Equal(t, "agent_execution", store.shadow[0].RuntimeExecution)
		require.Equal(t, "deterministic", store.shadow[0].RuntimeLevel)
		require.NotEmpty(t, store.shadow[0].TextExcerpt)
	})
	t.Run("unbound chat scores conversation-less", func(t *testing.T) {
		store := newFakeConversationInboundStore()
		server := matrixIntakeHomeServer(t, "!r1:hs", nil, nil)
		loop := newConversationDispatchLoop(t, server, store, func(context.Context, workspace.Conversation, []workspace.ConversationMessage, string, []string) (string, error) {
			t.Fatal("unbound chat must not run a turn")
			return "", nil
		})
		message, row := dispatchMessage("просто вопрос ни о чём")
		require.False(t, loop.dispatchConversationMessage(context.Background(), nil, message, "u1", &row))
		require.Len(t, store.shadow, 1)
		require.Empty(t, store.shadow[0].ConversationID)
		require.Equal(t, "deterministic", store.shadow[0].RuntimeLevel)
	})
	t.Run("channel opt-out silences the recorder", func(t *testing.T) {
		store := newFakeConversationInboundStore()
		require.NoError(t, store.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-1", OwnerID: "u1"}))
		require.NoError(t, store.BindConversationSource(context.Background(), "", "matrix", "!r1:hs", "conv-1"))
		server := matrixIntakeHomeServer(t, "!r1:hs", nil, nil)
		loop := newConversationDispatchLoop(t, server, store, func(context.Context, workspace.Conversation, []workspace.ConversationMessage, string, []string) (string, error) {
			return "", nil
		})
		message, row := dispatchMessage("любой текст")
		require.False(t, loop.dispatchConversationMessage(context.Background(), map[string]string{"conversation_shadow_enabled": "off"}, message, "u1", &row))
		require.Empty(t, store.shadow)
	})
}

func TestChannelDispatchAgentlessConversationStaysIntake(t *testing.T) {
	store := newFakeConversationInboundStore()
	require.NoError(t, store.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-1", OwnerID: "u1"}))
	require.NoError(t, store.BindConversationSource(context.Background(), "", "matrix", "!r1:hs", "conv-1"))
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, nil)
	loop := newConversationDispatchLoop(t, server, store, func(context.Context, workspace.Conversation, []workspace.ConversationMessage, string, []string) (string, error) {
		t.Fatal("agentless talk must not run a turn")
		return "", nil
	})
	message, row := dispatchMessage("любой текст")
	consumed := loop.dispatchConversationMessage(context.Background(), nil, message, "u1", &row)
	require.False(t, consumed)
	_ = row
}

func TestChannelDispatchPermanentFailureParksMessage(t *testing.T) {
	store := newFakeConversationInboundStore()
	require.NoError(t, store.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-1", AgentID: "agent-1", OwnerID: "u1"}))
	require.NoError(t, store.BindConversationSource(context.Background(), "", "matrix", "!r1:hs", "conv-1"))
	store.agents["agent-1"] = workspace.Agent{ID: "agent-1", Enabled: true}
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, nil)
	loop := newConversationDispatchLoop(t, server, store, func(context.Context, workspace.Conversation, []workspace.ConversationMessage, string, []string) (string, error) {
		// The lead vanished mid-flight: the turn path surfaces the permanent
		// sentinel exactly as runConversationTurn wraps it (its own wrapping
		// is pinned by TestRunConversationTurnWrapsPermanentFailure).
		return "", fmt.Errorf("%w: conversation lead agent is disabled", errConversationTurnPermanent)
	})
	message, row := dispatchMessage("есть вопрос")
	messageID := stableConversationMessageID("matrix", "!r1:hs", "$m1")
	_, _, _ = store.AppendConversationMessage(context.Background(), &workspace.ConversationMessage{
		ID: messageID, ConversationID: "conv-1", SenderType: "human", Status: "received",
	})
	consumed := loop.dispatchConversationMessage(context.Background(), nil, message, "u1", &row)
	require.True(t, consumed)
	waitFor(t, 2*time.Second, func() bool { return store.lastStatus(messageID) == "failed" })
	require.Empty(t, store.enqueuedOutbox(), "a parked turn must not enqueue deliveries")
	_ = row
}

func TestRedispatchStaleTurnRedrivesQueuedTurn(t *testing.T) {
	store := newFakeConversationInboundStore()
	require.NoError(t, store.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-1", AgentID: "agent-1", OwnerID: "u1"}))
	require.NoError(t, store.BindConversationSource(context.Background(), "", "matrix", "!r1:hs", "conv-1"))
	store.agents["agent-1"] = workspace.Agent{ID: "agent-1", Enabled: true}
	messageID := stableConversationMessageID("matrix", "!r1:hs", "$m1")
	stale := workspace.ConversationMessage{ID: messageID, ConversationID: "conv-1", SenderType: "human", Status: "queued", Source: "matrix"}
	_, _, _ = store.AppendConversationMessage(context.Background(), &stale)
	_ = store.SetConversationMessageStatus(context.Background(), messageID, "queued")
	turnCalled := make(chan string, 1)
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, nil)
	loop := newConversationDispatchLoop(t, server, store, func(_ context.Context, _ workspace.Conversation, _ []workspace.ConversationMessage, _ string, _ []string) (string, error) {
		turnCalled <- "done"
		return "восстановленный ответ", nil
	})
	loop.redispatchStaleTurn(stale)
	select {
	case <-turnCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("stale turn was not redriven")
	}
	waitFor(t, 2*time.Second, func() bool { return store.lastStatus(messageID) == "completed" })
	events := store.enqueuedOutbox()
	require.Len(t, events, 1, "the redriven answer must ride the outbox")
}

// fakeOutboxStore is the delivery loop's persistence fake.
type fakeOutboxStore struct {
	mu        sync.Mutex
	events    []workspace.ConversationOutboxEvent
	completed []string
	retries   map[string]time.Duration
	failures  []string
	stale     []workspace.ConversationMessage
}

func (s *fakeOutboxStore) ClaimDueConversationOutboxEvents(_ context.Context, limit int, _ time.Duration) ([]workspace.ConversationOutboxEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) > limit {
		events := s.events[:limit]
		s.events = s.events[limit:]
		return events, nil
	}
	events := s.events
	s.events = nil
	return events, nil
}

func (s *fakeOutboxStore) CompleteConversationOutboxEvent(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, id)
	return nil
}

func (s *fakeOutboxStore) FailConversationOutboxEvent(_ context.Context, id string, _ int, _ error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, id)
	return nil
}

func (s *fakeOutboxStore) RetryConversationOutboxEvent(_ context.Context, id string, delay time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retries[id] = delay
	return nil
}

func (s *fakeOutboxStore) ListStaleConversationTurns(_ context.Context, _ time.Duration, _ int) ([]workspace.ConversationMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stale, nil
}

func (s *fakeOutboxStore) retryCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.retries[id]; ok {
		return 1
	}
	return 0
}

func TestOutboxLoopDeliversConversationMessage(t *testing.T) {
	var sent testSink
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, &sent)
	store := &fakeOutboxStore{retries: map[string]time.Duration{}}
	payload, err := json.Marshal(conversationMessagePayload{Text: "ответ из offbox", Channel: "matrix", Address: "!r1:hs"})
	require.NoError(t, err)
	loop := &conversationOutboxLoop{
		store:  store,
		lookup: func(channel string) *channelInboundLoop { return newOutboxTransportLoop(t, server, channel) },
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	loop.deliver(context.Background(), workspace.ConversationOutboxEvent{ID: "ev-1", EventType: workspace.OutboxConversationMessage, Payload: payload})
	require.Equal(t, []string{"ev-1"}, store.completed)
	require.Equal(t, []string{"ответ из offbox"}, sent.all())
}

func TestOutboxLoopRunAnswerWaitsForLiveRun(t *testing.T) {
	var sent testSink
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, &sent)
	store := &fakeOutboxStore{retries: map[string]time.Duration{}}
	payload, err := json.Marshal(runAnswerPayload{RunID: "run-1", Project: "omni", Channel: "matrix", Address: "!r1:hs"})
	require.NoError(t, err)
	transport := newOutboxTransportLoop(t, server, "matrix")
	transport.resolveRun = func(context.Context, string, string) (workspace.Run, error) {
		return workspace.Run{ID: "run-1", Status: "running"}, nil
	}
	loop := &conversationOutboxLoop{
		store:  store,
		lookup: func(string) *channelInboundLoop { return transport },
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	loop.deliver(context.Background(), workspace.ConversationOutboxEvent{ID: "ev-live", EventType: workspace.OutboxRunAnswer, Payload: payload, CreatedAt: time.Now()})
	require.Empty(t, store.completed, "a live run must not complete its delivery")
	require.Empty(t, sent.all())
	require.Equal(t, 1, store.retryCount("ev-live"), "the event requeues for the next poll")
}

func TestOutboxLoopRunAnswerDeliversAndRecordsConversationResult(t *testing.T) {
	var sent testSink
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, &sent)
	convStore := newFakeConversationInboundStore()
	require.NoError(t, convStore.CreateConversation(context.Background(), &workspace.Conversation{ID: "conv-1", OwnerID: "u1"}))
	require.NoError(t, convStore.BindConversationSource(context.Background(), "", "matrix", "!r1:hs", "conv-1"))
	row := workspace.InboundMessage{ID: "row-1", Channel: "matrix", Room: "!r1:hs"}
	_, _ = convStore.InsertInboundMessage(context.Background(), &row)
	outbox := &fakeOutboxStore{retries: map[string]time.Duration{}}
	payload, err := json.Marshal(runAnswerPayload{
		RunID: "run-9", Project: "omni", Channel: "matrix", Address: "!r1:hs",
		InboundRowID: row.ID, MarkAnswered: true, ConversationID: "conv-1",
	})
	require.NoError(t, err)
	transport := newOutboxTransportLoop(t, server, "matrix")
	transport.store = convStore
	transport.resolveRun = func(context.Context, string, string) (workspace.Run, error) {
		return workspace.Run{ID: "run-9", Status: "completed", Answer: "Итог работы", AgentID: "agent-1", ProjectID: "omni"}, nil
	}
	loop := &conversationOutboxLoop{
		store:  outbox,
		lookup: func(string) *channelInboundLoop { return transport },
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	loop.deliver(context.Background(), workspace.ConversationOutboxEvent{ID: "ev-9", EventType: workspace.OutboxRunAnswer, Payload: payload})
	require.Equal(t, []string{"ev-9"}, outbox.completed)
	require.Len(t, sent.all(), 1)
	require.True(t, strings.HasPrefix(sent.item(0), "Итог работы"))
	// The talk keeps a durable agent-side record of the result, idempotent
	// by the run id, and the inbound row closes as answered.
	messages, err := convStore.ListConversationMessages(context.Background(), "conv-1", 0, 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "task_result", messages[0].Source)
	require.Equal(t, "run-9", messages[0].SourceMessageID)
	if latest, _ := convStore.lastUpdate(row.ID); latest.Status != workspace.InboundStatusAnswered {
		t.Fatalf("row not marked answered after run answer: %+v", latest)
	}
}

func TestOutboxLoopGivesUpOnStaleRunAnswer(t *testing.T) {
	var sent testSink
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, &sent)
	store := &fakeOutboxStore{retries: map[string]time.Duration{}}
	payload, err := json.Marshal(runAnswerPayload{RunID: "run-x", Project: "omni", Channel: "matrix", Address: "!r1:hs"})
	require.NoError(t, err)
	transport := newOutboxTransportLoop(t, server, "matrix")
	transport.resolveRun = func(context.Context, string, string) (workspace.Run, error) {
		return workspace.Run{ID: "run-x", Status: "running"}, nil
	}
	loop := &conversationOutboxLoop{
		store:  store,
		lookup: func(string) *channelInboundLoop { return transport },
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	loop.deliver(context.Background(), workspace.ConversationOutboxEvent{
		ID: "ev-old", EventType: workspace.OutboxRunAnswer, Payload: payload,
		CreatedAt: time.Now().Add(-3 * time.Hour),
	})
	require.Empty(t, store.completed)
	require.Empty(t, store.retries, "a 2h-old event must not requeue forever")
	require.NotEmpty(t, store.failures)
}

func TestOutboxLoopFailsEventWithoutTransport(t *testing.T) {
	store := &fakeOutboxStore{retries: map[string]time.Duration{}}
	payload, err := json.Marshal(conversationMessagePayload{Text: "x", Channel: "slack", Address: "y"})
	require.NoError(t, err)
	loop := &conversationOutboxLoop{
		store:  store,
		lookup: func(string) *channelInboundLoop { return nil },
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	loop.deliver(context.Background(), workspace.ConversationOutboxEvent{ID: "ev-2", EventType: workspace.OutboxConversationMessage, Payload: payload, CreatedAt: time.Now()})
	require.Empty(t, store.completed)
	require.Equal(t, []string{"ev-2"}, store.failures, "no transport: the event goes through the fail path, its backoff ladder keeps the backlog alive")
}

// newOutboxTransportLoop builds the transport half of a channel loop for
// outbox delivery tests: a real matrix driver aimed at the fake homeserver.
func newOutboxTransportLoop(t *testing.T, server *httptest.Server, channel string) *channelInboundLoop {
	t.Helper()
	if channel != "matrix" {
		t.Fatalf("test transport only supports matrix, got %q", channel)
	}
	driver := matrixTestDriver(t)
	return &channelInboundLoop{
		driver:     driver,
		poller:     driver.Poller(),
		httpClient: server.Client(),
		settings: func(context.Context) (agent.ChannelSettings, error) {
			return agent.ChannelSettings{Channels: map[string]map[string]string{
				"matrix": {"homeserver": server.URL, "access_token": "tok"},
			}}, nil
		},
		store:    &fakeInboundStore{},
		sourceID: "src",
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}
