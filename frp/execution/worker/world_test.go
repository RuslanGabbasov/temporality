package worker_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/execution/worker"
	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/memory"
	"github.com/temporality-project/temporality/frp/world"
)

func worldFixture(t *testing.T, root string) (world.World, affordance.Definition) {
	t.Helper()
	value := world.World{Protocol: "frp", Version: "0.3", WorldID: "workspace-main", StateVersion: 3, Resources: []world.Resource{{ID: "workspace", Type: world.ResourceFilesystem, Path: root}}, Capabilities: []string{"filesystem.read"}, Limits: world.Limits{MaxReadBytes: 4096, MaxEntries: 100, TimeoutSec: 10}}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range world.StandardReadDefinitions() {
		if candidate.ID == world.AffordanceInspectWorkspace {
			return value, candidate
		}
	}
	t.Fatal("inspect_workspace definition missing")
	return value, affordance.Definition{}
}

func standardDefinition(t *testing.T, id string) affordance.Definition {
	t.Helper()
	for _, candidate := range world.StandardReadDefinitions() {
		if candidate.ID == id {
			return candidate
		}
	}
	t.Fatalf("definition %s missing", id)
	return affordance.Definition{}
}

func createWorldExecution(t *testing.T, store *memory.Store, bound world.World, definition affordance.Definition, episodeID, executionID, requestID string, arguments map[string]any, now time.Time) {
	t.Helper()
	request := affordance.Request{Protocol: "frp", Version: "0.3", RequestID: requestID, EpisodeID: episodeID, AffordanceID: definition.ID, Arguments: arguments}
	requested := fixtureEvent("er-"+executionID, affordance.EventRequested, now, episodeID, "request_id", requestID)
	created := fixtureEvent("ec-"+executionID, execution.EventExecutionCreated, now, episodeID, "execution_id", executionID)
	value := execution.Execution{Protocol: "frp", Version: "0.3", ExecutionID: executionID, RequestID: requestID, EpisodeID: episodeID, AffordanceID: definition.ID, WorldID: bound.WorldID, WorldVersion: bound.StateVersion, Status: execution.StatusCreated, CreatedEventID: created.EventID, IntentPersistedAt: now}
	if err := store.CreateExecution(context.Background(), definition, request, value, requested, created); err != nil {
		t.Fatal(err)
	}
}

func fixtureEvent(id, kind string, at time.Time, episodeID, key, value string) protocol.Event {
	return protocol.Event{Protocol: "frp", Version: "0.3", EventID: id, TransactionTime: at, ValidTime: at, EpisodeID: episodeID, Type: kind, Payload: map[string]any{key: value}, Provenance: map[string]any{}}
}

func newTestWorker(store *memory.Store, adapter worker.Adapter, prefix string, clock func() time.Time) worker.Worker {
	n := 0
	return worker.Worker{Store: store, Workflows: world.StandardWorkflows(), Adapter: adapter, NewID: func() string { n++; return fmt.Sprintf("%s-event-%d", prefix, n) }, Now: clock}
}

// payloadInt reads a numeric payload field surviving the JSON round-trip
// (memory store clones produce float64 numbers).
func payloadInt(payload map[string]any, key string) (int, bool) {
	switch value := payload[key].(type) {
	case int:
		return value, true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func TestWorkerRecordsWorldObservations(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	bound, definition := worldFixture(t, t.TempDir())
	stateEvent, err := world.StateEvent(bound, "world-event-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorld(ctx, bound, stateEvent); err != nil {
		t.Fatal(err)
	}
	createWorldExecution(t, store, bound, definition, "episode-obs", "x-obs", "r-obs", map[string]any{"path": "."}, now)
	w := newTestWorker(store, world.NewAdapter(bound), "obs", func() time.Time { now = now.Add(time.Second); return now })
	if _, err = w.RunOnce(ctx, "episode-obs"); err != nil {
		t.Fatal(err)
	}
	final, err := store.GetExecution(ctx, "x-obs")
	if err != nil || final.Status != execution.StatusCompleted {
		t.Fatalf("unexpected execution: %#v %v", final, err)
	}
	events, err := store.List(ctx, substrate.EventFilter{EpisodeID: "episode-obs"})
	if err != nil {
		t.Fatal(err)
	}
	observations := 0
	for _, event := range events {
		if event.Type != world.EventWorldObservation {
			continue
		}
		observations++
		if event.Payload["world_id"] != bound.WorldID {
			t.Fatalf("observation missing world binding: %#v", event.Payload)
		}
		if version, ok := payloadInt(event.Payload, "world_version"); !ok || version != bound.StateVersion {
			t.Fatalf("observation missing world binding: %#v", event.Payload)
		}
		if event.Payload["execution_id"] != "x-obs" {
			t.Fatalf("observation missing execution linkage: %#v", event.Payload)
		}
		observationType, _ := event.Payload["observation_type"].(string)
		if observationType != world.ObservationStat && observationType != world.ObservationDirectoryListing {
			t.Fatalf("unexpected observation type: %q", observationType)
		}
	}
	if observations != 2 {
		t.Fatalf("expected 2 observations, got %d", observations)
	}
}

func TestWorkerDeniesUngrantedWorldCapability(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	bound, definition := worldFixture(t, t.TempDir())
	bound.Capabilities = []string{"git.read"} // filesystem.read revoked
	if err := bound.Validate(); err != nil {
		t.Fatal(err)
	}
	stateEvent, err := world.StateEvent(bound, "world-event-deny", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorld(ctx, bound, stateEvent); err != nil {
		t.Fatal(err)
	}
	createWorldExecution(t, store, bound, definition, "episode-deny", "x-deny", "r-deny", map[string]any{"path": "."}, now)
	w := newTestWorker(store, world.NewAdapter(bound), "deny", func() time.Time { now = now.Add(time.Second); return now })
	if _, err = w.RunOnce(ctx, "episode-deny"); err != nil {
		t.Fatal(err)
	}
	final, err := store.GetExecution(ctx, "x-deny")
	if err != nil || final.Status != execution.StatusFailed {
		t.Fatalf("denied capability did not fail execution: %#v %v", final, err)
	}
	if final.Error == nil || final.Error.Class != execution.ErrorPermissionDenied {
		t.Fatalf("unexpected failure class: %#v", final.Error)
	}
}

func TestWorkerWorldVersionDivergenceRecorded(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "summary.txt"), []byte("observed"), 0o644); err != nil {
		t.Fatal(err)
	}
	bound, _ := worldFixture(t, root)
	readFile := standardDefinition(t, world.AffordanceReadFile)
	stateEvent, err := world.StateEvent(bound, "world-event-v1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorld(ctx, bound, stateEvent); err != nil {
		t.Fatal(err)
	}
	createWorldExecution(t, store, bound, readFile, "episode-div", "x-div", "r-div", map[string]any{"path": "notes/summary.txt"}, now)
	// The world moves forward after the intent was persisted.
	bumped := bound
	bumped.StateVersion = bound.StateVersion + 1
	bumpedEvent, err := world.StateEvent(bumped, "world-event-v2", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorld(ctx, bumped, bumpedEvent); err != nil {
		t.Fatal(err)
	}
	w := newTestWorker(store, world.NewAdapter(bumped), "div", func() time.Time { now = now.Add(time.Second); return now })
	if _, err = w.RunOnce(ctx, "episode-div"); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(ctx, substrate.EventFilter{EpisodeID: "episode-div"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type != world.EventWorldObservation {
			continue
		}
		intent, intentOK := payloadInt(event.Payload, "intent_world_version")
		current, currentOK := payloadInt(event.Payload, "world_version")
		if intentOK && currentOK && intent == bound.StateVersion && current == bumped.StateVersion {
			found = true
		}
	}
	if !found {
		t.Fatal("world divergence was not recorded in observations")
	}
}
