package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/observation/memory"
)

// Run visibility (docs/activity-scoping.md, the operator model): a scoped
// principal sees only the runs they have a stake in — their own (delegation
// children inherit the initiating actor in event context), runs of triggers
// they configured, and their granted units' people. Admins and service
// principals are unrestricted. The resolver failing must fail CLOSED.

const runVisTokens = "admin-token-1234567890:boss:admin:*;op-token-12345678901:op:operator:*;svc-token-1234567890:svc:writer:*"

func runVisibilityTestServer(t *testing.T, resolver func(p controlplane.Principal) ([]string, error)) (http.Handler, *memory.Store) {
	t.Helper()
	gate, err := controlplane.NewGate(runVisTokens)
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	// Static tokens carry no UserID; run visibility keys on the workspace
	// user — exactly what DB user tokens provide in production. Register the
	// operator as a DB-token principal the way LoadTokenPrincipals does.
	gate.SetDBTokens(map[string]controlplane.Principal{
		"db-op-token-12345678": {Subject: "op", UserID: "user-op", Role: controlplane.RoleOperator, Projects: []string{"*"}},
	})
	store := memory.New()
	handler := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), gate,
		WithRunVisibility(func(ctx context.Context, p controlplane.Principal) ([]string, error) {
			if p.UserID != "user-op" {
				return nil, nil // admins and service subjects: unrestricted
			}
			return resolver(p)
		}),
	)
	return handler, store
}

func appendRunEvent(t *testing.T, store *memory.Store, eventID, runID, actorID, project string) {
	t.Helper()
	event := observation.Event{
		Schema: observation.Schema, EventID: eventID, OccurredAt: time.Now().UTC(),
		Source:  observation.Source{ID: "k", Integration: "k"},
		Context: observation.Context{Project: project, Run: runID, Actor: observation.Actor{ID: actorID, Type: "agent"}},
		Type:    "run.started", Data: map[string]any{"task_id": "t-" + runID},
	}
	if _, err := store.AppendObservation(context.Background(), event); err != nil {
		t.Fatalf("seed event: %v", err)
	}
}

func listEventsBody(t *testing.T, handler http.Handler, token string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/observations/events?limit=100", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestRunVisibilityScopesTheEventListing(t *testing.T) {
	// Alice (operator) started a root run and a delegated child (the child
	// inherits her actor); Bob started his own run; a system event carries no
	// run at all.
	handler, store := runVisibilityTestServer(t, func(p controlplane.Principal) ([]string, error) {
		return []string{"user-alice", "trigger-her-own"}, nil
	})
	appendRunEvent(t, store, "e1", "alice-root", "user-alice", "p1")
	appendRunEvent(t, store, "e2", "alice-root/delegate/01", "user-alice", "p1")
	appendRunEvent(t, store, "e3", "bob-root", "user-bob", "p1")
	appendRunEvent(t, store, "e4", "trig-run", "trigger-her-own", "p1")
	appendRunEvent(t, store, "e5", "trig-other", "trigger-someone-else", "p1")

	body := listEventsBody(t, handler, "db-op-token-12345678")
	events := body["events"].([]any)
	runs := map[string]bool{}
	for _, raw := range events {
		event := raw.(map[string]any)
		context := event["context"].(map[string]any)
		runs[context["run"].(string)] = true
	}
	if !runs["alice-root"] || !runs["alice-root/delegate/01"] {
		t.Fatalf("own run and its delegation tree must be visible: %v", runs)
	}
	if !runs["trig-run"] {
		t.Fatalf("a run of an owned trigger must be visible: %v", runs)
	}
	if runs["bob-root"] || runs["trig-other"] {
		t.Fatalf("someone else's run must be hidden: %v", runs)
	}

	// The admin and the service subject see everything (resolver said nil for
	// them; UserID is also empty for static tokens).
	for _, token := range []string{"admin-token-1234567890", "svc-token-1234567890"} {
		body := listEventsBody(t, handler, token)
		if got := len(body["events"].([]any)); got != 5 {
			t.Fatalf("%s must see all 5 events, got %d", token, got)
		}
	}
}

func TestRunVisibilityFailsClosedOnResolverError(t *testing.T) {
	handler, store := runVisibilityTestServer(t, func(p controlplane.Principal) ([]string, error) {
		return nil, errors.New("workspace unreachable")
	})
	appendRunEvent(t, store, "e1", "alice-root", "user-alice", "p1")
	body := listEventsBody(t, handler, "db-op-token-12345678")
	if events := body["events"].([]any); len(events) != 0 {
		t.Fatalf("a broken resolver must hide every run event, got %d", len(events))
	}
}

func TestRunVisibilityRowlessEventsPass(t *testing.T) {
	// Events without a run (system/lifecycle noise) stay visible to scoped
	// principals: the scope is about RUNS, not the whole journal.
	handler, store := runVisibilityTestServer(t, func(p controlplane.Principal) ([]string, error) {
		return []string{"user-alice"}, nil
	})
	appendRunEvent(t, store, "e1", "bob-root", "user-bob", "p1")
	event := observation.Event{
		Schema: observation.Schema, EventID: "sys-1", OccurredAt: time.Now().UTC(),
		Source:  observation.Source{ID: "k", Integration: "k"},
		Context: observation.Context{Project: "p1"},
		Type:    "knowledge.extraction.completed",
	}
	if _, err := store.AppendObservation(context.Background(), event); err != nil {
		t.Fatalf("seed system event: %v", err)
	}
	body := listEventsBody(t, handler, "db-op-token-12345678")
	events := body["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["type"] != "knowledge.extraction.completed" {
		t.Fatalf("run-less events must stay visible, got: %v", events)
	}
}
