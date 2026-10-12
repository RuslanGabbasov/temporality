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
