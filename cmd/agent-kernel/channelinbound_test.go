package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// fakeInboundStore records what the loop routed.
type fakeInboundStore struct {
	mu         sync.Mutex
	requests   []workspace.HumanRequest
	deliveries []workspace.HumanRequestDelivery
	users      []workspace.User
	answered   map[string]string
	cancelled  []string
	threads    map[string]string
	audits     []string
	cursor     workspace.ChannelCursor
	inbound    []workspace.InboundMessage
	updates    []inboundUpdateRecord
	runs       map[string]workspace.Run
}

// inboundUpdateRecord is one observed UpdateInboundMessage call.
type inboundUpdateRecord struct {
	id     string
	update workspace.InboundMessageUpdate
}

func (s *fakeInboundStore) ListHumanRequests(context.Context, string, string, string, bool) ([]workspace.HumanRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, nil
}

func (s *fakeInboundStore) ListOpenHumanRequestDeliveries(context.Context) ([]workspace.HumanRequestDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deliveries, nil
}

func (s *fakeInboundStore) AnswerHumanRequest(_ context.Context, id, response, _ string) (workspace.HumanRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.answered == nil {
		s.answered = map[string]string{}
	}
	s.answered[id] = response
	return workspace.HumanRequest{ID: id}, nil
}

func (s *fakeInboundStore) CancelHumanRequest(_ context.Context, id, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelled = append(s.cancelled, id)
	return nil
}

// SetHumanRequestThreadRef mirrors the store contract: fill-when-empty.
func (s *fakeInboundStore) SetHumanRequestThreadRef(_ context.Context, id string, threadRef string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for i := range s.requests {
		if s.requests[i].ID != id {
			continue
		}
		found = true
		if s.requests[i].ThreadRef == "" {
			s.requests[i].ThreadRef = threadRef
		}
	}
	if !found {
		return workspace.ErrNotFound
	}
	return nil
}

func (s *fakeInboundStore) ListUsers(context.Context) ([]workspace.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.users, nil
}

func (s *fakeInboundStore) RecordAccessAudit(_ context.Context, _, action, _, _ string, _ map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, action)
	return nil
}

func (s *fakeInboundStore) ChannelCursor(context.Context, string) (workspace.ChannelCursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor, nil
}

func (s *fakeInboundStore) SaveChannelCursor(_ context.Context, _ string, cursor workspace.ChannelCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor = cursor
	return nil
}

func (s *fakeInboundStore) InsertInboundMessage(_ context.Context, m *workspace.InboundMessage) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.inbound {
		if existing.Channel == m.Channel && existing.MessageID == m.MessageID {
			return false, nil
		}
	}
	s.inbound = append(s.inbound, *m)
	return true, nil
}

func (s *fakeInboundStore) UpdateInboundMessage(_ context.Context, id string, u workspace.InboundMessageUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, inboundUpdateRecord{id: id, update: u})
	return nil
}

// GetRun backs the intake answer watcher: no runs configured means the watcher
// finds nothing and gives up quietly (tests without runs are not answering).
func (s *fakeInboundStore) GetRun(_ context.Context, id string) (workspace.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, ok := s.runs[id]; ok {
		return run, nil
	}
	return workspace.Run{}, workspace.ErrNotFound
}

// ListRoutedInboundMessages backs the follow-up-as-note lookup: newest first
// among the sender's rows that produced runs.
func (s *fakeInboundStore) ListRoutedInboundMessages(_ context.Context, senderUserID string, limit int) ([]workspace.InboundMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var routed []workspace.InboundMessage
	for i := len(s.inbound) - 1; i >= 0 && len(routed) < limit; i-- {
		row := s.inbound[i]
		if row.SenderUserID == senderUserID && row.TargetRunID != "" && row.Status == workspace.InboundStatusRouted {
			routed = append(routed, row)
		}
	}
	return routed, nil
}

func (s *fakeInboundStore) CountInboundMessagesSince(_ context.Context, sender string, since time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, m := range s.inbound {
		if m.SenderUserID == sender && !m.CreatedAt.Before(since) {
			count++
		}
	}
	return count, nil
}

// lastUpdate returns the newest recorded update for one inbound row.
func (s *fakeInboundStore) lastUpdate(id string) (workspace.InboundMessageUpdate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest workspace.InboundMessageUpdate
	found := false
	for _, record := range s.updates {
		if record.id == id {
			latest = record.update
			found = true
		}
	}
	return latest, found
}

// inboundRow returns the stored inbound message by message id.
func (s *fakeInboundStore) inboundRow(id string) (workspace.InboundMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.inbound {
		if row.MessageID == id {
			return row, true
		}
	}
	return workspace.InboundMessage{}, false
}

type recordedSignal struct {
	mu         sync.Mutex
	calls      []agent.Approval
	workflowID []string
	err        error
}

func (r *recordedSignal) record(_ context.Context, workflowID string, approval agent.Approval) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, approval)
	r.workflowID = append(r.workflowID, workflowID)
	return r.err
}

func (r *recordedSignal) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// matrixHomeServer fakes whoami + sync: the initial sync replays the bot's
// question and the human's answer in room !r1:hs, later syncs are empty. The
// since pointer records the cursor the loop sent.
func matrixHomeServer(t *testing.T, sinceParam *string) *httptest.Server {
	t.Helper()
	askedAt := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/account/whoami"):
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": "@bot:hs"})
		case strings.HasSuffix(r.URL.Path, "/sync"):
			if sinceParam != nil {
				*sinceParam = r.URL.Query().Get("since")
			}
			if r.URL.Query().Get("since") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"next_batch": "batch-1",
					"rooms": map[string]any{"join": map[string]any{
						"!r1:hs": map[string]any{"timeline": map[string]any{"events": []any{
							map[string]any{"type": "m.room.message", "sender": "@bot:hs", "origin_server_ts": askedAt.Add(-time.Minute).UnixMilli(), "content": map[string]any{"msgtype": "m.text", "body": "Temporality: an agent is waiting for your answer"}},
							map[string]any{"type": "m.room.message", "sender": "@human:hs", "origin_server_ts": askedAt.UnixMilli(), "content": map[string]any{"msgtype": "m.text", "body": "yes, deploy"}},
						}}},
					}},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"next_batch": "batch-2", "rooms": map[string]any{"join": map[string]any{}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// matrixTestDriver finds the registered matrix driver — the loop is generic,
// the scenarios stay matrix-shaped.
func matrixTestDriver(t *testing.T) agent.ChannelDriver {
	t.Helper()
	for _, driver := range agent.ChannelDrivers() {
		if driver.Type() == "matrix" {
			return driver
		}
	}
	t.Fatal("matrix driver not registered")
	return nil
}

func newInboundLoop(t *testing.T, homeServer *httptest.Server, store *fakeInboundStore, sig *recordedSignal) *channelInboundLoop {
	t.Helper()
	driver := matrixTestDriver(t)
	return &channelInboundLoop{
		driver:     driver,
		poller:     driver.Poller(),
		httpClient: homeServer.Client(),
		settings: func(context.Context) (agent.ChannelSettings, error) {
			return agent.ChannelSettings{Channels: map[string]map[string]string{
				"matrix": {"homeserver": homeServer.URL, "access_token": "tok"},
			}}, nil
		},
		store:    store,
		sourceID: "src",
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		signal:   sig.record,
	}
}

func TestChannelInboundCycleRoutesReplyIntoRun(t *testing.T) {
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "run/turn/02/call_x", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
			Channel: "matrix", Status: "delivered", Question: "Deploy?",
			CreatedAt: time.Now().Add(-5 * time.Minute),
		}},
		users: []workspace.User{{
			ID: "u1", Name: "Human", Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", Enabled: true}},
		}},
	}
	sig := &recordedSignal{}
	var since string
	loop := newInboundLoop(t, matrixHomeServer(t, &since), store, sig)

	delay, err := loop.cycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if delay != 0 {
		t.Fatalf("active cycle should loop immediately, delay %v", delay)
	}
	if sig.count() != 1 {
		t.Fatalf("expected one signal, got %d", sig.count())
	}
	approval := sig.calls[0]
	if approval.OperationID != "run/turn/02/call_x" || !approval.Approved || approval.Response != "yes, deploy" || approval.ActorID != "u1" {
		t.Fatalf("unexpected approval: %+v", approval)
	}
	if sig.workflowID[0] != agent.WorkflowID("src", "proj", "run") {
		t.Fatalf("workflow id %q", sig.workflowID[0])
	}
	if store.answered["run/turn/02/call_x"] != "yes, deploy" {
		t.Fatalf("row not answered: %+v", store.answered)
	}
	if len(store.audits) != 1 || store.audits[0] != workspace.AuditHumanAnswered {
		t.Fatalf("audit missing: %+v", store.audits)
	}
	if loop.cursor != "batch-1" || loop.selfID != "@bot:hs" {
		t.Fatalf("loop state: cursor=%q self=%q", loop.cursor, loop.selfID)
	}

	// Second cycle: incremental sync carries the cursor; nothing new arrives.
	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if since != "batch-1" {
		t.Fatalf("incremental sync must send the cursor, got since=%q", since)
	}
	if sig.count() != 1 {
		t.Fatal("reply re-routed")
	}
}

func TestChannelInboundCycleIdlesWhenUnconfigured(t *testing.T) {
	loop := &channelInboundLoop{
		driver:     matrixTestDriver(t),
		poller:     matrixTestDriver(t).Poller(),
		httpClient: http.DefaultClient,
		settings:   func(context.Context) (agent.ChannelSettings, error) { return agent.ChannelSettings{}, nil },
		store:      &fakeInboundStore{},
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		signal: func(context.Context, string, agent.Approval) error {
			t.Fatal("signal must not fire")
			return nil
		},
	}
	delay, err := loop.cycle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if delay != 30*time.Second {
		t.Fatalf("unconfigured matrix should idle 30s, got %v", delay)
	}
}

func TestChannelInboundSkipsWebDeliveredAsks(t *testing.T) {
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "op", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
			Channel: "web", Status: "delivered", CreatedAt: time.Now().Add(-time.Minute),
		}},
		users: []workspace.User{{ID: "u1", Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", Enabled: true}}}},
	}
	sig := &recordedSignal{}
	loop := newInboundLoop(t, matrixHomeServer(t, nil), store, sig)
	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sig.count() != 0 {
		t.Fatalf("web-delivered ask must not match room replies: %+v", sig.calls)
	}
}

func TestChannelInboundKeepsRowOpenWhenSignalFails(t *testing.T) {
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "op", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
			Channel: "matrix", Status: "delivered", CreatedAt: time.Now().Add(-time.Minute),
		}},
		users: []workspace.User{{ID: "u1", Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", Enabled: true}}}},
	}
	sig := &recordedSignal{err: fmt.Errorf("temporal unavailable")}
	loop := newInboundLoop(t, matrixHomeServer(t, nil), store, sig)
	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.answered) != 0 || len(store.cancelled) != 0 {
		t.Fatalf("row closed despite signal failure: %+v", store.answered)
	}
}

// TestChannelInboundTeamAskMatchesAnyMember: a request with org: recipient and
// delivery rows (docs/inbound-messages.md §8) is answerable from every
// member's chat — including a member who is not resolved_user. Exactly one
// answer routes (first wins, MatchChannelReplies pairs by request id).
func TestChannelInboundTeamAskMatchesAnyMember(t *testing.T) {
	askedAt := time.Now()
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "team-op", RunID: "run", ProjectID: "proj", Recipient: "org:core",
			ResolvedUser: "u-head", Channel: "multi", Status: "delivered", Question: "Approve?",
			CreatedAt: askedAt.Add(-time.Minute),
		}},
		deliveries: []workspace.HumanRequestDelivery{
			{RequestID: "team-op", UserID: "u-head", Channel: "matrix", Status: "delivered", CreatedAt: askedAt.Add(-time.Minute)},
			{RequestID: "team-op", UserID: "u-qa", Channel: "matrix", Status: "delivered", CreatedAt: askedAt.Add(-time.Minute)},
			{RequestID: "team-op", UserID: "u-web", Channel: "web", Status: "delivered", CreatedAt: askedAt.Add(-time.Minute)},
		},
		users: []workspace.User{
			{ID: "u-head", Name: "Head", Channels: []workspace.UserChannel{{Type: "matrix", Address: "!rhead:hs", Enabled: true}}},
			{ID: "u-qa", Name: "QA", Channels: []workspace.UserChannel{{Type: "matrix", Address: "!rqa:hs", Enabled: true}}},
			{ID: "u-web", Name: "WebOnly"},
		},
	}
	// Only the QA member replies, from their own room: the question never went
	// to resolved_user's chat in this scenario's sync, yet the team ask must
	// match — that is the fan-out contract.
	var sent []string
	server := matrixIntakeHomeServer(t, "!rqa:hs", []any{
		map[string]any{"type": "m.room.message", "sender": "@bot:hs", "origin_server_ts": askedAt.Add(-2 * time.Minute).UnixMilli(), "content": map[string]any{"msgtype": "m.text", "body": "Temporality: an agent is waiting"}},
		matrixTextEvent("$q1", "!rqa:hs", "@qa:hs", "approved by qa"),
	}, &sent)
	sig := &recordedSignal{}
	loop := newInboundLoop(t, server, store, sig)

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sig.count() != 1 {
		t.Fatalf("exactly one answer must route, got %d", sig.count())
	}
	approval := sig.calls[0]
	if approval.OperationID != "team-op" || approval.ActorID != "u-qa" || approval.Response != "approved by qa" {
		t.Fatalf("unexpected approval: %+v", approval)
	}
	if store.answered["team-op"] != "approved by qa" {
		t.Fatalf("row not answered: %+v", store.answered)
	}
}

// TestChannelInboundPinsThreadOnThreadedReply covers the thread persistence
// (docs/channels.md §9): an ask delivered with a pinned thread_ref matches a
// reply in that thread exactly, and a threaded reply to an ask without a
// ref pins the user's thread on the request row — the webhook and poll
// feeds both land here through routeMessages.
func TestChannelInboundPinsThreadOnThreadedReply(t *testing.T) {
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{
			{
				ID: "run/turn/01/ask_pinned", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
				Channel: "matrix", Status: "delivered", Question: "Pinned?", ThreadRef: "1697000000.000100",
				CreatedAt: time.Now().Add(-5 * time.Minute),
			},
			{
				ID: "run/turn/01/ask_free", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
				Channel: "matrix", Status: "delivered", Question: "Free?",
				CreatedAt: time.Now().Add(-4 * time.Minute),
			},
		},
		users: []workspace.User{{
			ID: "u1", Name: "Human", Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", Enabled: true}},
		}},
	}
	sig := &recordedSignal{}
	loop := newInboundLoop(t, matrixHomeServer(t, nil), store, sig)
	loop.routeMessages(context.Background(), []agent.InboundMessage{
		// Lands in the pinned ask's thread despite the other ask being older.
		{ID: "m1", Channel: "matrix", ChatID: "!r1:hs", Sender: "@human:hs", Text: "pinned answer",
			Timestamp: time.Now().Add(-time.Minute), ReplyTo: "1697000000.000100"},
		// No thread pointer: falls back to the oldest open ask and — being
		// threaded — pins that thread on it.
		{ID: "m2", Channel: "matrix", ChatID: "!r1:hs", Sender: "@human:hs", Text: "free answer",
			Timestamp: time.Now(), ReplyTo: "1697000000.000900"},
	}, nil)

	if sig.count() != 2 {
		t.Fatalf("expected two routed replies, got %d", sig.count())
	}
	if sig.calls[0].OperationID != "run/turn/01/ask_pinned" || sig.calls[0].Response != "pinned answer" {
		t.Fatalf("exact thread match broken: %+v", sig.calls[0])
	}
	if sig.calls[1].OperationID != "run/turn/01/ask_free" || sig.calls[1].Response != "free answer" {
		t.Fatalf("fallback match broken: %+v", sig.calls[1])
	}
	if store.requests[0].ThreadRef != "1697000000.000100" || store.requests[1].ThreadRef != "1697000000.000900" {
		t.Fatalf("thread refs not pinned: %+v", store.requests)
	}
}

func TestSafeFileName(t *testing.T) {
	cases := map[string]string{
		"report.md":                  "report.md",
		"Отчёт по тестам.zip":        "otchet-po-testam.zip",
		"../../etc/passwd":           "passwd",
		"weird name (final) v2.xlsx": "weird-name-final-v2.xlsx",
		"...":                        "attachment",
		"":                           "attachment",
	}
	for input, want := range cases {
		if got := safeFileName(input); got != want {
			t.Fatalf("safeFileName(%q) = %q, want %q", input, got, want)
		}
	}
}

// recordedIntake captures intake callback invocations.
type recordedIntake struct {
	mu       sync.Mutex
	messages []agent.InboundMessage
	senders  []string
	runID    string
	err      error
}

func (r *recordedIntake) record(_ context.Context, message agent.InboundMessage, senderUserID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message)
	r.senders = append(r.senders, senderUserID)
	return r.runID, r.err
}

func (r *recordedIntake) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

// recordedEmit captures intake.* events the loop journals.
type recordedEmit struct {
	mu      sync.Mutex
	types   []string
	senders []string
	rawData []map[string]any
}

func (r *recordedEmit) record(_ context.Context, eventType string, _ agent.InboundMessage, senderUserID string, data map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.types = append(r.types, eventType)
	r.senders = append(r.senders, senderUserID)
	// Copy: the enqueuer mutates the map after the callback returns.
	copyData := make(map[string]any, len(data))
	for key, value := range data {
		copyData[key] = value
	}
	r.rawData = append(r.rawData, copyData)
}

func (r *recordedEmit) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.types)
}

// matrixTextEvent builds one m.room.message sync event.
func matrixTextEvent(eventID, room, sender, body string) map[string]any {
	return map[string]any{
		"type": "m.room.message", "event_id": eventID, "sender": sender,
		"origin_server_ts": time.Now().UnixMilli(),
		"content":          map[string]any{"msgtype": "m.text", "body": body},
	}
}

// matrixIntakeHomeServer fakes whoami + a sync that replays the same room
// events on every poll (the redelivery model) and records notices sent through
// the room message route. The optional receipts sink records m.read receipt
// posts (the dialog's read mark).
func matrixIntakeHomeServer(t *testing.T, room string, events []any, sent *[]string, receipts ...*[]string) *httptest.Server {
	t.Helper()
	batch := 0
	var receiptSink *[]string
	if len(receipts) > 0 {
		receiptSink = receipts[0]
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/account/whoami"):
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": "@bot:hs"})
		case strings.HasSuffix(r.URL.Path, "/sync"):
			batch++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"next_batch": fmt.Sprintf("batch-%d", batch),
				"rooms": map[string]any{"join": map[string]any{
					room: map[string]any{"timeline": map[string]any{"events": events}},
				}},
			})
		case strings.Contains(r.URL.Path, "/receipt/"):
			if receiptSink != nil {
				*receiptSink = append(*receiptSink, r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case strings.Contains(r.URL.Path, "/send/"):
			var payload struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			*sent = append(*sent, payload.Body)
			_ = json.NewEncoder(w).Encode(map[string]any{"event_id": "$notice"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestChannelInboundIntakeRejectsUnknownSenderInRegisteredRoom: a registered
// room with a stranger's sender identity records the message as rejected,
// journals intake.rejected(unknown_sender) and answers with one polite notice
// pointing at the app. No run starts.
func TestChannelInboundIntakeRejectsUnknownSenderInRegisteredRoom(t *testing.T) {
	var sent []string
	server := matrixIntakeHomeServer(t, "!r1:hs", []any{
		matrixTextEvent("$m1", "!r1:hs", "@stranger:hs", "подготовь смету на разработку"),
	}, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
	}
	intake := &recordedIntake{}
	emit := &recordedEmit{}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.intake = func(context.Context, agent.InboundMessage, string) (string, error) {
		t.Fatal("intake must not start for an unknown sender")
		return "", nil
	}
	loop.emit = emit.record
	loop.intakeEnabled = true

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if intake.count() != 0 {
		t.Fatalf("intake called %d times", intake.count())
	}
	row, ok := store.inboundRow("$m1")
	if !ok {
		t.Fatal("message not recorded")
	}
	if row.Status != workspace.InboundStatusReceived {
		t.Fatalf("inserted status %q, want received", row.Status)
	}
	if latest, _ := store.lastUpdate(row.ID); latest.Status != workspace.InboundStatusRejected {
		t.Fatalf("row not rejected: %+v", latest)
	}
	if emit.count() != 1 || emit.types[0] != "intake.rejected" || emit.rawData[0]["reason"] != "unknown_sender" {
		t.Fatalf("unexpected events: %v %+v", emit.types, emit.rawData)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "Temporality") {
		t.Fatalf("expected one rejection notice, got %v", sent)
	}
}

// TestChannelInboundIntakeRejectsUnregisteredRoomSilently: a message in a room
// no user channel is bound to is recorded as rejected with reason
// unregistered_room — and never answered, so the bot does not spam every room
// it sits in.
func TestChannelInboundIntakeRejectsUnregisteredRoomSilently(t *testing.T) {
	var sent []string
	server := matrixIntakeHomeServer(t, "!r2:hs", []any{
		matrixTextEvent("$m2", "!r2:hs", "@human:hs", "сделай отчёт по проекту"),
	}, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
	}
	emit := &recordedEmit{}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.intake = func(context.Context, agent.InboundMessage, string) (string, error) {
		t.Fatal("intake must not start in an unregistered room")
		return "", nil
	}
	loop.emit = emit.record
	loop.intakeEnabled = true

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, ok := store.inboundRow("$m2")
	if !ok {
		t.Fatal("message not recorded")
	}
	if latest, _ := store.lastUpdate(row.ID); latest.Status != workspace.InboundStatusRejected {
		t.Fatalf("row not rejected: %+v", latest)
	}
	if emit.count() != 1 || emit.rawData[0]["reason"] != "unregistered_room" {
		t.Fatalf("unexpected events: %v %+v", emit.types, emit.rawData)
	}
	if len(sent) != 0 {
		t.Fatalf("unregistered room must stay silent, sent %v", sent)
	}
}

// TestChannelInboundIntakeStartsRunForKnownSender: a message authored by the
// registered sender identity of the room owner identifies the user, journals
// intake.received, starts the intake run and records its id on the row.
func TestChannelInboundIntakeStartsRunForKnownSender(t *testing.T) {
	var sent []string
	server := matrixIntakeHomeServer(t, "!r1:hs", []any{
		matrixTextEvent("$m3", "!r1:hs", "@human:hs", "Подготовь проект документа «Смета на разработку»"),
	}, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
	}
	intake := &recordedIntake{runID: "intake-m3"}
	emit := &recordedEmit{}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.intake = intake.record
	loop.emit = emit.record
	loop.intakeEnabled = true

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if intake.count() != 1 || intake.messages[0].ID != "$m3" || intake.senders[0] != "u1" {
		t.Fatalf("intake calls: %v senders %v", intake.messages, intake.senders)
	}
	row, ok := store.inboundRow("$m3")
	if !ok {
		t.Fatal("message not recorded")
	}
	if latest, _ := store.lastUpdate(row.ID); latest.IntakeRunID != "intake-m3" {
		t.Fatalf("intake run id not recorded: %+v", latest)
	}
	if emit.count() != 1 || emit.types[0] != "intake.received" || emit.senders[0] != "u1" {
		t.Fatalf("unexpected events: %v %v", emit.types, emit.senders)
	}
	if len(sent) != 0 {
		t.Fatalf("known sender must not get a rejection notice, sent %v", sent)
	}
}

// TestChannelInboundIntakeMarksMessageRead: a message accepted into the
// intake pipeline posts an m.read receipt for the sender's event (the
// dialog's read mark arrives before the answer, not with it).
func TestChannelInboundIntakeMarksMessageRead(t *testing.T) {
	var sent, receipts []string
	server := matrixIntakeHomeServer(t, "!r1:hs", []any{
		matrixTextEvent("$m12", "!r1:hs", "@human:hs", "Подготовь смету"),
	}, &sent, &receipts)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
	}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.intake = (&recordedIntake{runID: "intake-m12"}).record
	loop.intakeEnabled = true

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || !strings.Contains(receipts[0], "/receipt/m.read/") || !strings.Contains(receipts[0], "!r1:hs") {
		t.Fatalf("read receipt not posted: %v", receipts)
	}
	// The event id survives the path escape ($ is path-safe in Go).
	if !strings.Contains(receipts[0], "$m12") {
		t.Fatalf("receipt lost the event id: %v", receipts)
	}
}

// TestChannelInboundIntakeDeduplicatesRedelivery: the fake homeserver replays
// the same event on every poll (the crash-redelivery model); only the first
// delivery starts an intake run.
func TestChannelInboundIntakeDeduplicatesRedelivery(t *testing.T) {
	var sent []string
	server := matrixIntakeHomeServer(t, "!r1:hs", []any{
		matrixTextEvent("$m4", "!r1:hs", "@human:hs", "отчёт ещё раз"),
	}, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
	}
	intake := &recordedIntake{runID: "intake-m4"}
	emit := &recordedEmit{}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.intake = intake.record
	loop.emit = emit.record
	loop.intakeEnabled = true

	for i := 0; i < 2; i++ {
		if _, err := loop.cycle(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if intake.count() != 1 {
		t.Fatalf("redelivery must not start a second intake run, got %d", intake.count())
	}
	if emit.count() != 1 {
		t.Fatalf("redelivery must not journal twice, got %d events", emit.count())
	}
	if rows := len(store.inbound); rows != 1 {
		t.Fatalf("expected one recorded row, got %d", rows)
	}
}

// TestChannelWatcherDeliversResolvedAnswer: the stored run row stays
// "started" forever — the AgentRun workflow does not write its result back —
// so the watcher must trust the Temporal resolver. The answer is delivered
// and the row is marked answered even though the raw row never reached a
// terminal status (the production bug: every intake verdict silently expired
// in the 30-minute give-up loop).
func TestChannelWatcherDeliversResolvedAnswer(t *testing.T) {
	var sent []string
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
	}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.resolveRun = func(_ context.Context, project, runID string) (workspace.Run, error) {
		if project != "intake" {
			t.Errorf("resolver project = %q, want intake", project)
		}
		if runID != "intake-m9" {
			t.Errorf("resolver run = %q", runID)
		}
		return workspace.Run{ID: runID, Status: "completed", Answer: "Ответ из базы знаний"}, nil
	}
	row := &workspace.InboundMessage{
		ID: workspace.InboundMessageID("matrix", "$m9"), Channel: "matrix", MessageID: "$m9",
		SenderRaw: "@human:hs", Room: "!r1:hs", SenderUserID: "u1",
		Status: workspace.InboundStatusClassified, IntakeRunID: "intake-m9",
	}
	loop.watchRunAnswer(context.Background(), row, "intake-m9", true)
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "Ответ из базы знаний") {
		t.Fatalf("answer not delivered: %v", sent)
	}
	if latest, _ := store.lastUpdate(row.ID); latest.Status != workspace.InboundStatusAnswered {
		t.Fatalf("row not marked answered: %+v", latest)
	}
}

// TestChannelWatcherTargetRunResolvesByRowProject: the routed target run
// lives in the resolved project — the watcher passes row.ProjectID to the
// resolver (the workflow id needs it) and keeps the row's routed status (the
// answered mark stays for intake verdicts).
func TestChannelWatcherTargetRunResolvesByRowProject(t *testing.T) {
	var sent []string
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
	}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.resolveRun = func(_ context.Context, project, runID string) (workspace.Run, error) {
		if project != "omni" {
			t.Errorf("resolver project = %q, want omni", project)
		}
		if runID != "target-1" {
			t.Errorf("resolver run = %q", runID)
		}
		// A non-completed result status with an answer is still terminal for
		// delivery (turn_limit, timeout…).
		return workspace.Run{ID: runID, Status: "turn_limit", Answer: "Целевой ответ"}, nil
	}
	row := workspace.InboundMessage{
		ID: workspace.InboundMessageID("matrix", "$m10"), Channel: "matrix", MessageID: "$m10",
		SenderRaw: "@human:hs", Room: "!r1:hs", SenderUserID: "u1",
		Status: workspace.InboundStatusRouted, ProjectID: "omni", TargetRunID: "target-1",
	}
	loop.WatchTargetRun(row, "target-1")
	waitFor(t, 2*time.Second, func() bool { return len(sent) == 1 })
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "Целевой ответ") {
		t.Fatalf("target answer not delivered: %v", sent)
	}
	if latest, _ := store.lastUpdate(row.ID); latest.Status == workspace.InboundStatusAnswered {
		t.Fatal("routed row must keep its status")
	}
}

// TestChannelInboundFollowUpSteersRunningRun: a message written while the
// sender's routed run is still live becomes a user note in that run — not a
// competing intake classification. The chat hears a confirmation, the row is
// attached to the target run, and no new intake run starts.
func TestChannelInboundFollowUpSteersRunningRun(t *testing.T) {
	var sent []string
	server := matrixIntakeHomeServer(t, "!r1:hs", []any{
		matrixTextEvent("$m14", "!r1:hs", "@human:hs", "и ещё проверь кэш"),
	}, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
		inbound: []workspace.InboundMessage{{
			ID: workspace.InboundMessageID("matrix", "$old"), Channel: "matrix", MessageID: "$old",
			SenderUserID: "u1", Room: "!r1:hs", Status: workspace.InboundStatusRouted,
			ProjectID: "omni", TargetRunID: "target-9",
		}},
	}
	intake := &recordedIntake{runID: "intake-m14"}
	notes := make(chan agent.UserNote, 4)
	var notedWorkflow string
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.intake = intake.record
	loop.intakeEnabled = true
	loop.resolveRun = func(_ context.Context, project, runID string) (workspace.Run, error) {
		if project != "omni" || runID != "target-9" {
			t.Errorf("resolve %s/%s", project, runID)
		}
		return workspace.Run{ID: runID, Status: "running"}, nil
	}
	loop.note = func(_ context.Context, workflowID string, note agent.UserNote) error {
		notedWorkflow = workflowID
		notes <- note
		return nil
	}

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case note := <-notes:
		if note.Text != "и ещё проверь кэш" || note.ActorID != "u1" {
			t.Fatalf("unexpected note: %+v", note)
		}
	default:
		t.Fatal("user note not signaled")
	}
	if notedWorkflow != agent.WorkflowID("src", "omni", "target-9") {
		t.Fatalf("note routed to the wrong workflow: %q", notedWorkflow)
	}
	if intake.count() != 0 {
		t.Fatalf("a live run must not spawn a classification run, got %d", intake.count())
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "Добавил к текущей задаче") {
		t.Fatalf("confirmation not delivered: %v", sent)
	}
	// The message row is attached to the target run (the audit trail).
	row, ok := store.inboundRow("$m14")
	if !ok {
		t.Fatal("message not recorded")
	}
	latest, found := store.lastUpdate(row.ID)
	if !found || latest.TargetRunID != "target-9" || latest.ProjectID != "omni" || latest.Status != workspace.InboundStatusRouted {
		t.Fatalf("note row not attached: %+v found=%v", latest, found)
	}
}

// TestChannelInboundFollowUpFallsBackToClassification: with no live routed
// run the same message goes through the intake pipeline as before — a dead
// run (or a failed note signal) must not eat the message.
func TestChannelInboundFollowUpFallsBackToClassification(t *testing.T) {
	var sent []string
	server := matrixIntakeHomeServer(t, "!r1:hs", []any{
		matrixTextEvent("$m15", "!r1:hs", "@human:hs", "новая задача: смета"),
	}, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
		inbound: []workspace.InboundMessage{{
			ID: workspace.InboundMessageID("matrix", "$old"), Channel: "matrix", MessageID: "$old",
			SenderUserID: "u1", Room: "!r1:hs", Status: workspace.InboundStatusRouted,
			ProjectID: "omni", TargetRunID: "target-done",
		}},
	}
	intake := &recordedIntake{runID: "intake-m15"}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.intake = intake.record
	loop.intakeEnabled = true
	loop.resolveRun = func(context.Context, string, string) (workspace.Run, error) {
		return workspace.Run{ID: "target-done", Status: "completed"}, nil
	}
	loop.note = func(context.Context, string, agent.UserNote) error {
		t.Fatal("a finished run must not be noted")
		return nil
	}

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if intake.count() != 1 {
		t.Fatalf("message must fall back to classification, got %d intake runs", intake.count())
	}
	if len(sent) != 0 {
		t.Fatalf("no confirmation expected: %v", sent)
	}
}

// waitFor polls the condition with a deadline; used for the watcher goroutine.
func waitFor(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestChannelWatcherAnnouncesRunProgress: the watcher tails the run's journal
// events and voices phase changes in the chat — an opening line right away,
// then one line per distinct tool phase, paced, without the reply hint. The
// final answer stays the watcher's last word.
func TestChannelWatcherAnnouncesRunProgress(t *testing.T) {
	var sent []string
	server := matrixIntakeHomeServer(t, "!r1:hs", nil, &sent)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
	}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	// The run stays live for the whole test: the watcher must keep polling and
	// announcing while the workflow is still running.
	loop.resolveRun = func(context.Context, string, string) (workspace.Run, error) {
		return workspace.Run{ID: "intake-m13", Status: "running"}, nil
	}
	loop.journal = func(_ context.Context, runID string) ([]observation.Event, error) {
		if runID != "intake-m13" {
			t.Errorf("journal run = %q", runID)
		}
		return []observation.Event{
			{EventID: "src/event/000001", Type: "turn.started"},
			{EventID: "src/event/000002", Type: "tool.started", Data: map[string]any{"tool": "knowledge_explore"}},
			{EventID: "src/event/000003", Type: "model.completed"},
			// Turn 2: a short interim line — voiced when turn 3 starts.
			{EventID: "src/event/000004", Type: "turn.started"},
			{EventID: "src/event/000005", Type: "model.text_delta", Data: map[string]any{"turn": 2, "text": "Смотрю таблицу цен в проекте"}},
			{EventID: "src/event/000006", Type: "turn.completed"},
			// Turn 3: a long text — the answer-in-progress, never voiced.
			{EventID: "src/event/000007", Type: "turn.started"},
			{EventID: "src/event/000008", Type: "model.text_delta", Data: map[string]any{"turn": 3, "text": strings.Repeat("длинный ответ ", 40)}},
			{EventID: "src/event/000009", Type: "turn.completed"},
		}, nil
	}
	row := &workspace.InboundMessage{
		ID: workspace.InboundMessageID("matrix", "$m13"), Channel: "matrix", MessageID: "$m13",
		SenderRaw: "@human:hs", Room: "!r1:hs", SenderUserID: "u1",
		Status: workspace.InboundStatusClassified, IntakeRunID: "intake-m13",
		// The user writes in Russian — every canned line follows the dialog
		// language detected from the message body.
		Body: "Что ты знаешь про Directum Omni?",
	}
	// The watcher keeps polling — run it in the background and cut it short.
	ctx, cancel := context.WithCancel(context.Background())
	watchDone := make(chan struct{})
	go func() {
		loop.watchRunAnswer(ctx, row, "intake-m13", true)
		close(watchDone)
	}()
	// The opening line, the knowledge_explore phase and the interim quote
	// land within seconds (poll every 5s, pacing window 10s).
	waitFor(t, 17*time.Second, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(sent) >= 3
	})
	cancel()
	<-watchDone

	if len(sent) == 0 || !strings.HasPrefix(sent[0], "Принято — работаю над вашим сообщением") {
		t.Fatalf("opening progress line missing: %v", sent)
	}
	joined := strings.Join(sent, "\n")
	if !strings.Contains(joined, "Ищу в базе знаний проекта") {
		t.Fatalf("phase line missing: %v", sent)
	}
	if !strings.Contains(joined, "💬 «Смотрю таблицу цен в проекте»") {
		t.Fatalf("interim model line missing: %v", sent)
	}
	if strings.Contains(joined, "длинный ответ") {
		t.Fatalf("long turn text must not be voiced: %v", sent)
	}
}

// TestChannelInboundIntakeRateLimitsFloodingSender: the per-sender cap
// (docs/inbound-messages.md §12 phase 4) rejects the message that crosses the
// hourly limit — with the audit trail and a room notice — instead of spawning
// another intake run. The pre-populated rows stand for messages already routed
// in the past hour; the loop under test carries ratePerHour = 2.
func TestChannelInboundIntakeRateLimitsFloodingSender(t *testing.T) {
	var sent []string
	server := matrixIntakeHomeServer(t, "!r1:hs", []any{
		matrixTextEvent("$m5", "!r1:hs", "@human:hs", "и ещё одна задача"),
	}, &sent)
	recent := time.Now().UTC().Add(-time.Minute)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
		inbound: []workspace.InboundMessage{
			{ID: "inbound/matrix/$r1", Channel: "matrix", MessageID: "$r1", SenderUserID: "u1", CreatedAt: recent},
			{ID: "inbound/matrix/$r2", Channel: "matrix", MessageID: "$r2", SenderUserID: "u1", CreatedAt: recent},
			{ID: "inbound/matrix/$r3", Channel: "matrix", MessageID: "$r3", SenderUserID: "u1", CreatedAt: recent},
		},
	}
	intake := &recordedIntake{runID: "intake-m5"}
	emit := &recordedEmit{}
	loop := newInboundLoop(t, server, store, &recordedSignal{})
	loop.intake = func(context.Context, agent.InboundMessage, string) (string, error) {
		t.Fatal("intake must not start for a rate-limited sender")
		return "", nil
	}
	loop.emit = emit.record
	loop.intakeEnabled = true
	loop.ratePerHour = 2

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	row, ok := store.inboundRow("$m5")
	if !ok {
		t.Fatal("message not recorded")
	}
	if latest, _ := store.lastUpdate(row.ID); latest.Status != workspace.InboundStatusRejected {
		t.Fatalf("row not rejected: %+v", latest)
	}
	if emit.count() != 1 || emit.types[0] != "intake.rejected" || emit.rawData[0]["reason"] != "rate_limited" {
		t.Fatalf("unexpected events: %v %+v", emit.types, emit.rawData)
	}
	if len(store.audits) != 1 || store.audits[0] != workspace.AuditIntakeRejected {
		t.Fatalf("expected one rejection audit, got %v", store.audits)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "лимит сообщений") {
		t.Fatalf("expected one rate-limit notice, got %v", sent)
	}
	if intake.count() != 0 {
		t.Fatalf("intake must not run, got %d calls", intake.count())
	}
}

// matrixFileHomeServer fakes whoami + sync with one m.file reply and serves the
// media download on both the v1 and v3 routes.
func matrixFileHomeServer(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	askedAt := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/account/whoami"):
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": "@bot:hs"})
		case strings.HasSuffix(r.URL.Path, "/sync"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"next_batch": "batch-1",
				"rooms": map[string]any{"join": map[string]any{
					"!r1:hs": map[string]any{"timeline": map[string]any{"events": []any{
						map[string]any{"type": "m.room.message", "sender": "@human:hs", "origin_server_ts": askedAt.UnixMilli(), "content": map[string]any{"msgtype": "m.file", "body": "вот экспорт", "filename": "Навыки QA.zip", "url": "mxc://hs/media1"}},
					}}},
				}},
			})
		case strings.Contains(r.URL.Path, "/media/download/") || strings.Contains(r.URL.Path, "/download/"):
			if r.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(payload))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestChannelInboundRoutesFileReplyIntoInbox(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KERNEL_SANDBOX_ROOT", root)
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "run/turn/01/call_f", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
			Channel: "matrix", Status: "delivered", CreatedAt: time.Now().Add(-time.Minute),
		}},
		users: []workspace.User{{ID: "u1", Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", Enabled: true}}}},
	}
	sig := &recordedSignal{}
	loop := newInboundLoop(t, matrixFileHomeServer(t, "ZIPDATA"), store, sig)

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sig.count() != 1 {
		t.Fatalf("expected one signal, got %d", sig.count())
	}
	saved := filepath.Join(root, "projects", "proj", "inbox", "navyki-qa.zip")
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("attachment not saved to inbox: %v", err)
	}
	want := "[Attached file: /workspace/inbox/navyki-qa.zip] вот экспорт"
	if sig.calls[0].Response != want {
		t.Fatalf("response %q, want %q", sig.calls[0].Response, want)
	}
	if store.answered["run/turn/01/call_f"] != want {
		t.Fatalf("row not answered with file note: %+v", store.answered)
	}
}

// TestChannelInboundIntakeSavesInitiativeAttachments: a file-bearing initiative
// message from an identified sender lands its attachments in the staging dir
// and the saved paths are recorded on the row (docs/inbound-messages.md §12
// phase 3). A stranger's files are never fetched.
func TestChannelInboundIntakeSavesInitiativeAttachments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KERNEL_SANDBOX_ROOT", root)
	askedAt := time.Now()
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/account/whoami"):
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": "@bot:hs"})
		case strings.HasSuffix(r.URL.Path, "/sync"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"next_batch": "batch-1",
				"rooms": map[string]any{"join": map[string]any{
					"!r1:hs": map[string]any{"timeline": map[string]any{"events": []any{
						map[string]any{"type": "m.room.message", "event_id": "$m9", "sender": "@human:hs", "origin_server_ts": askedAt.UnixMilli(), "content": map[string]any{"msgtype": "m.file", "body": "вот шаблон", "filename": "Смета.xlsx", "url": "mxc://hs/media2"}},
					}}},
				}},
			})
		case strings.Contains(r.URL.Path, "/download/"):
			_, _ = w.Write([]byte("XLSXDATA"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fileServer.Close)
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "matrix", Address: "!r1:hs", SenderID: "@human:hs", Enabled: true}},
		}},
	}
	intake := &recordedIntake{runID: "intake-m9"}
	loop := newInboundLoop(t, fileServer, store, &recordedSignal{})
	loop.intake = intake.record
	loop.emit = func(context.Context, string, agent.InboundMessage, string, map[string]any) {}
	loop.intakeEnabled = true

	if _, err := loop.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The staged file exists under the message-scoped key and the row recorded it.
	var latest workspace.InboundMessageUpdate
	var found bool
	for _, record := range store.updates {
		if record.update.Attachments != nil {
			latest, found = record.update, true
		}
	}
	if !found {
		t.Fatal("attachments update not recorded")
	}
	if len(latest.Attachments) != 1 || !strings.HasSuffix(latest.Attachments[0], "/smeta.xlsx") {
		t.Fatalf("attachments = %v", latest.Attachments)
	}
	payload, err := os.ReadFile(latest.Attachments[0])
	if err != nil || string(payload) != "XLSXDATA" {
		t.Fatalf("staged file wrong: %v %q", err, payload)
	}
	if !strings.Contains(latest.Attachments[0], filepath.Join("inbound")) {
		t.Fatalf("staging path must live under the inbound dir: %q", latest.Attachments[0])
	}
}
