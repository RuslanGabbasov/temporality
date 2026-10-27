package postgres

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestParseNotifyPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"empty means broadcast", "", ""},
		{"whitespace means broadcast", "   ", ""},
		{"bare project id", "proj-alpha", "proj-alpha"},
		{"project id is trimmed", "  proj-alpha  ", "proj-alpha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseNotifyPayload(tc.payload); got != tc.want {
				t.Fatalf("parseNotifyPayload(%q) = %q, want %q", tc.payload, got, tc.want)
			}
		})
	}
}

func TestCoalesceSignal(t *testing.T) {
	ch := make(chan struct{}, 1)
	coalesceSignal(ch)
	coalesceSignal(ch) // a pending signal dedups the second one, non-blocking

	select {
	case <-ch:
	default:
		t.Fatal("first signal must be delivered")
	}
	select {
	case <-ch:
		t.Fatal("second signal must have been coalesced away")
	default:
	}
}

func TestCoalesceSignalDoesNotBlockOnFullChannel(t *testing.T) {
	ch := make(chan struct{}) // unbuffered, no reader
	done := make(chan struct{})
	go func() {
		coalesceSignal(ch)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coalesceSignal must never block, even without a reader")
	}
}

// Live LISTEN/NOTIFY round trip — a manual check, it needs a real
// PostgreSQL: run with OBSERVATION_TEST_DATABASE_URL pointing at the same
// database the journal uses. Everything else in this file is pure and runs
// in the ordinary `go test ./observation/...`.
func TestSubscribeEventsLivePostgres(t *testing.T) {
	url := os.Getenv("OBSERVATION_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("manual check: set OBSERVATION_TEST_DATABASE_URL to run the LISTEN/NOTIFY round trip")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	projects, unsubscribe, err := store.SubscribeEventProjects(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	notifyFromHelperConn(ctx, t, store, "proj-live")
	select {
	case project := <-projects:
		if project != "proj-live" {
			t.Fatalf("payload project = %q, want proj-live", project)
		}
	case <-ctx.Done():
		t.Fatal("no notification within 15s")
	}

	// Empty payload — the broadcast form the kernel wakes every project on.
	notifyFromHelperConn(ctx, t, store, "")
	select {
	case project := <-projects:
		if project != "" {
			t.Fatalf("broadcast payload must deliver an empty project, got %q", project)
		}
	case <-ctx.Done():
		t.Fatal("no broadcast notification within 15s")
	}

	unsubscribe()
	if _, open := <-projects; open {
		t.Fatal("channel must be closed after unsubscribe")
	}
}

// notifyFromHelperConn fires a NOTIFY from an independent pooled connection,
// the same way a foreign ingest process would.
func notifyFromHelperConn(ctx context.Context, t *testing.T, store *Store, payload string) {
	t.Helper()
	conn, err := store.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire helper connection: %v", err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SELECT pg_notify('observation_events_changed', $1)", payload); err != nil {
		t.Fatalf("notify: %v", err)
	}
}
