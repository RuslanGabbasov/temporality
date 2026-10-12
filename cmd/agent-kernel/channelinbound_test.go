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
	"github.com/temporality-project/temporality/workspace"
)

// fakeInboundStore records what the loop routed.
type fakeInboundStore struct {
	mu        sync.Mutex
	requests  []workspace.HumanRequest
	users     []workspace.User
	answered  map[string]string
	cancelled []string
	audits    []string
	cursor    workspace.ChannelCursor
	inbound   []workspace.InboundMessage
	updates   []inboundUpdateRecord
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
// the room message route.
func matrixIntakeHomeServer(t *testing.T, room string, events []any, sent *[]string) *httptest.Server {
	t.Helper()
	batch := 0
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
