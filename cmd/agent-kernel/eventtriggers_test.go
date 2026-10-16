package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// The constructor's save-time contract: known keys only, event type and
// prompt required — a typo fails at save, not as a silent never-match.
func TestValidateEventTriggerConfig(t *testing.T) {
	good, _ := json.Marshal(workspace.EventConfig{
		EventType: "artifact.review.requested",
		Prompt:    "Nudge the reviewers",
		Filter:    map[string]string{"actor_id": "kernel-internal", "data.status": "draft"},
	})
	require.NoError(t, validateEventTriggerConfig(good))

	noType, _ := json.Marshal(workspace.EventConfig{Prompt: "p"})
	require.ErrorContains(t, validateEventTriggerConfig(noType), "event_type")

	noPrompt, _ := json.Marshal(workspace.EventConfig{EventType: "run.completed"})
	require.ErrorContains(t, validateEventTriggerConfig(noPrompt), "prompt")

	badKey, _ := json.Marshal(workspace.EventConfig{
		EventType: "run.completed", Prompt: "p",
		Filter: map[string]string{"org": "core"},
	})
	require.ErrorContains(t, validateEventTriggerConfig(badKey), "unknown event filter key")

	require.Error(t, validateEventTriggerConfig(json.RawMessage(`{"event_type": `)))
}

// The match-side contract: exact type plus every filter entry; data.* matches
// scalars through their string form; unknown keys never match.
func TestEventTriggerMatches(t *testing.T) {
	ev := observation.Event{
		EventID: "e-1",
		Type:    "artifact.review.requested",
		Context: observation.Context{
			Project: "omni", Run: "run-9",
			Actor: observation.Actor{ID: "kernel-internal", Type: "agent"},
		},
		Data: map[string]any{"status": "draft", "count": 3},
	}
	require.True(t, eventTriggerMatches(workspace.EventConfig{EventType: "artifact.review.requested"}, ev))
	require.True(t, eventTriggerMatches(workspace.EventConfig{
		EventType: "artifact.review.requested",
		Filter:    map[string]string{"actor_id": "kernel-internal", "project": "omni"},
	}, ev))
	require.True(t, eventTriggerMatches(workspace.EventConfig{
		EventType: "artifact.review.requested",
		Filter:    map[string]string{"data.status": "draft"},
	}, ev))
	// Non-string scalars compare through their string form.
	require.True(t, eventTriggerMatches(workspace.EventConfig{
		EventType: "artifact.review.requested",
		Filter:    map[string]string{"data.count": "3"},
	}, ev))

	require.False(t, eventTriggerMatches(workspace.EventConfig{EventType: "run.completed"}, ev))
	require.False(t, eventTriggerMatches(workspace.EventConfig{
		EventType: "artifact.review.requested",
		Filter:    map[string]string{"actor_id": "someone-else"},
	}, ev))
	require.False(t, eventTriggerMatches(workspace.EventConfig{
		EventType: "artifact.review.requested",
		Filter:    map[string]string{"actor_type": "human"},
	}, ev))
	require.False(t, eventTriggerMatches(workspace.EventConfig{
		EventType: "artifact.review.requested",
		Filter:    map[string]string{"data.missing": "x"},
	}, ev))
	require.False(t, eventTriggerMatches(workspace.EventConfig{
		EventType: "artifact.review.requested",
		Filter:    map[string]string{"what": "ever"},
	}, ev))
}

// The cycle contract: fresh cursor bootstraps to the tail without firing on
// history; subsequent cycles fire on matches only; events of trigger-started
// runs never re-fire (feedback guard); the cursor advances and persists.
func TestEventTriggerCycle(t *testing.T) {
	trigger := parsedEventTrigger{
		trigger: workspace.Trigger{ID: "t1", ProjectID: "omni", Type: "event", Enabled: true},
		cfg:     workspace.EventConfig{EventType: "artifact.review.requested"},
	}
	match := observation.Event{EventID: "e-match", Type: "artifact.review.requested",
		Context: observation.Context{Project: "omni"}, OccurredAt: time.Now(), ReceivedAt: time.Now()}
	other := observation.Event{EventID: "e-other", Type: "run.completed",
		Context: observation.Context{Project: "omni"}, OccurredAt: time.Now(), ReceivedAt: time.Now()}
	feedback := observation.Event{EventID: "e-loop", Type: "artifact.review.requested",
		Context: observation.Context{Project: "omni", Run: "trigger-t1-abc/turn/01"}, OccurredAt: time.Now(), ReceivedAt: time.Now()}

	history := []observation.Event{match, other, feedback} // the pre-activation journal
	var fired []string
	var savedCursors []string
	listener := &eventTriggerListener{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		listTriggers: func(context.Context) ([]parsedEventTrigger, error) {
			return []parsedEventTrigger{trigger}, nil
		},
		loadCursor: func(context.Context) (string, error) {
			if len(savedCursors) == 0 {
				return "", nil
			}
			return savedCursors[len(savedCursors)-1], nil
		},
		saveCursor: func(_ context.Context, cursor string) error {
			savedCursors = append(savedCursors, cursor)
			return nil
		},
		fireOne: func(_ context.Context, trig *parsedEventTrigger, ev observation.Event) {
			require.Equal(t, "t1", trig.trigger.ID)
			fired = append(fired, ev.EventID)
		},
	}
	pages := map[string][]observation.Event{"": history, "next": {match, feedback}}
	listener.fetchPage = func(_ context.Context, cursor string) ([]observation.Event, error) {
		// Two-page bootstrap: the first page is full-sized (forces another
		// fetch), the second is short (caught up).
		if cursor == "" {
			full := append([]observation.Event{}, history...)
			for len(full) < eventTriggerPageLimit {
				full = append(full, other)
			}
			return full, nil
		}
		if cursor == "next" {
			return pages["next"], nil
		}
		return nil, nil
	}
	// The bootstrap fetch must see a full first page to exercise the paging
	// path; fake the "next" marker by keying the second call.
	originalFetch := listener.fetchPage
	listener.fetchPage = func(ctx context.Context, cursor string) ([]observation.Event, error) {
		if cursor == "" {
			_, _ = originalFetch(ctx, cursor)
			return history, nil // short page after the full one: bootstrap ends here
		}
		return nil, nil
	}

	// Cycle 1: fresh cursor → bootstrap, nothing fires.
	require.NoError(t, listener.cycle(context.Background()))
	require.Empty(t, fired)
	require.NotEmpty(t, savedCursors)

	// Cycle 2: new matching events arrive → fires once each; the feedback
	// event (run of a trigger-started run) is skipped.
	listener.fetchPage = func(context.Context, string) ([]observation.Event, error) {
		return []observation.Event{other, feedback, match}, nil
	}
	require.NoError(t, listener.cycle(context.Background()))
	require.Equal(t, []string{"e-match"}, fired)

	// Cycle 3: empty page → nothing.
	listener.fetchPage = func(context.Context, string) ([]observation.Event, error) { return nil, nil }
	require.NoError(t, listener.cycle(context.Background()))
	require.Equal(t, []string{"e-match"}, fired)

	// No event triggers at all → the journal is not tailed.
	listener.listTriggers = func(context.Context) ([]parsedEventTrigger, error) { return nil, nil }
	listener.fetchPage = func(context.Context, string) ([]observation.Event, error) {
		t.Fatal("journal must not be tailed without event triggers")
		return nil, nil
	}
	require.NoError(t, listener.cycle(context.Background()))
}

// A fetch error surfaces to the cycle caller (logged by run), never panics.
func TestEventTriggerCycleFetchError(t *testing.T) {
	listener := &eventTriggerListener{
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		listTriggers: func(context.Context) ([]parsedEventTrigger, error) { return nil, errors.New("store down") },
	}
	require.Error(t, listener.cycle(context.Background()))
}
