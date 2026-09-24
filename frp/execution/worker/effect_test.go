package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/execution/worker"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/memory"
	"github.com/temporality-project/temporality/frp/world"
)

// writeFixture builds a world that grants filesystem.write over root plus the
// write_file affordance definition.
func writeFixture(t *testing.T, root string) (world.World, affordance.Definition) {
	t.Helper()
	value := world.World{
		Protocol: "frp", Version: "0.3", WorldID: "workspace-write", StateVersion: 5,
		Resources:    []world.Resource{{ID: "workspace", Type: world.ResourceFilesystem, Path: root}},
		Capabilities: []string{"filesystem.read", "filesystem.write"},
		Limits:       world.Limits{MaxReadBytes: 4096, MaxEntries: 100, TimeoutSec: 10},
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range world.StandardWriteDefinitions() {
		if candidate.ID == world.AffordanceWriteFile {
			return value, candidate
		}
	}
	t.Fatal("write_file definition missing")
	return value, affordance.Definition{}
}

func resolvedTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestWorkerCommitsWorldEffects(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	root := resolvedTestRoot(t)
	bound, definition := writeFixture(t, root)
	stateEvent, err := world.StateEvent(bound, "world-event-write", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorld(ctx, bound, stateEvent); err != nil {
		t.Fatal(err)
	}
	createWorldExecution(t, store, bound, definition, "episode-effect", "x-effect", "r-effect", map[string]any{"path": "agent.txt", "content": "written by agent"}, now)
	w := newTestWorker(store, world.NewAdapter(bound), "effect", func() time.Time { now = now.Add(time.Second); return now })
	if _, err = w.RunOnce(ctx, "episode-effect"); err != nil {
		t.Fatal(err)
	}
	final, err := store.GetExecution(ctx, "x-effect")
	if err != nil || final.Status != execution.StatusCompleted {
		t.Fatalf("unexpected execution: %#v %v", final, err)
	}
	raw, readErr := os.ReadFile(filepath.Join(root, "agent.txt"))
	if readErr != nil || string(raw) != "written by agent" {
		t.Fatalf("effect did not reach the world: %q %v", raw, readErr)
	}
	events, err := store.List(ctx, substrate.EventFilter{EpisodeID: "episode-effect"})
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	for _, event := range events {
		if event.Type != world.EventWorldEffect {
			continue
		}
		effects++
		if event.Payload["world_id"] != bound.WorldID || event.Payload["execution_id"] != "x-effect" {
			t.Fatalf("effect missing linkage: %#v", event.Payload)
		}
		if event.Payload["effect_type"] != world.EffectFileWritten {
			t.Fatalf("unexpected effect type: %#v", event.Payload)
		}
		payload, ok := event.Payload["payload"].(map[string]any)
		if !ok || payload["path"] != filepath.Join(root, "agent.txt") {
			t.Fatalf("effect payload missing path: %#v", event.Payload["payload"])
		}
	}
	if effects != 1 {
		t.Fatalf("expected 1 world.effect event, got %d", effects)
	}
}

func TestWorkerRejectsEffectWithoutEffectingAdapter(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	root := resolvedTestRoot(t)
	bound, definition := writeFixture(t, root)
	stateEvent, err := world.StateEvent(bound, "world-event-safeadapter", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorld(ctx, bound, stateEvent); err != nil {
		t.Fatal(err)
	}
	createWorldExecution(t, store, bound, definition, "episode-safe", "x-safe", "r-safe", map[string]any{"path": "agent.txt", "content": "x"}, now)
	// SafeAdapter implements neither Observe nor Effect, so the write step
	// cannot be routed to a world surface.
	w := newTestWorker(store, worker.SafeAdapter{}, "safe", func() time.Time { now = now.Add(time.Second); return now })
	if _, err = w.RunOnce(ctx, "episode-safe"); err != nil {
		t.Fatal(err)
	}
	final, err := store.GetExecution(ctx, "x-safe")
	if err != nil || final.Status != execution.StatusFailed {
		t.Fatalf("unexpected execution: %#v %v", final, err)
	}
	if final.Error == nil || final.Error.Class != execution.ErrorPermissionDenied {
		t.Fatalf("unexpected failure: %#v", final.Error)
	}
	if _, statErr := os.Stat(filepath.Join(root, "agent.txt")); !os.IsNotExist(statErr) {
		t.Fatal("effect leaked into the world despite adapter refusal")
	}
}

func TestWorkerFailsUngrantedWriteCapability(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	root := resolvedTestRoot(t)
	bound, definition := writeFixture(t, root)
	bound.Capabilities = []string{"filesystem.read"} // write revoked after intent shape known
	if err := bound.Validate(); err != nil {
		t.Fatal(err)
	}
	stateEvent, err := world.StateEvent(bound, "world-event-nowrite", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorld(ctx, bound, stateEvent); err != nil {
		t.Fatal(err)
	}
	createWorldExecution(t, store, bound, definition, "episode-nowrite", "x-nowrite", "r-nowrite", map[string]any{"path": "agent.txt", "content": "x"}, now)
	w := newTestWorker(store, world.NewAdapter(bound), "nowrite", func() time.Time { now = now.Add(time.Second); return now })
	if _, err = w.RunOnce(ctx, "episode-nowrite"); err != nil {
		t.Fatal(err)
	}
	final, err := store.GetExecution(ctx, "x-nowrite")
	if err != nil || final.Status != execution.StatusFailed {
		t.Fatalf("unexpected execution: %#v %v", final, err)
	}
	if final.Error == nil || final.Error.Class != execution.ErrorPermissionDenied {
		t.Fatalf("unexpected failure: %#v", final.Error)
	}
	if _, statErr := os.Stat(filepath.Join(root, "agent.txt")); !os.IsNotExist(statErr) {
		t.Fatal("write happened despite revoked capability")
	}
}

// TestWorkerCommitsMixedObservationAndEffect proves a semantic affordance
// (update_configuration) commits a read observation and a write effect within
// one atomic execution: both events carry the same execution linkage and the
// terminal transition persists them together.
func TestWorkerCommitsMixedObservationAndEffect(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	root := resolvedTestRoot(t)
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("debug: true\nreplicas: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bound := world.World{
		Protocol: "frp", Version: "0.3", WorldID: "workspace-semantic", StateVersion: 3,
		Resources:    []world.Resource{{ID: "workspace", Type: world.ResourceFilesystem, Path: root}},
		Capabilities: []string{"filesystem.read", "filesystem.write"},
		Limits:       world.Limits{MaxReadBytes: 4096, MaxEntries: 100, TimeoutSec: 10},
	}
	if err := bound.Validate(); err != nil {
		t.Fatal(err)
	}
	var definition affordance.Definition
	for _, candidate := range world.StandardSemanticDefinitions() {
		if candidate.ID == world.AffordanceUpdateConfiguration {
			definition = candidate
		}
	}
	if definition.ID == "" {
		t.Fatal("update_configuration definition missing")
	}
	stateEvent, err := world.StateEvent(bound, "world-event-mixed", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorld(ctx, bound, stateEvent); err != nil {
		t.Fatal(err)
	}
	createWorldExecution(t, store, bound, definition, "episode-mixed", "x-mixed", "r-mixed", map[string]any{
		"path": "config.yaml", "find": "debug: true", "replace": "debug: false",
	}, now)
	w := newTestWorker(store, world.NewAdapter(bound), "mixed", func() time.Time { now = now.Add(time.Second); return now })
	if _, err = w.RunOnce(ctx, "episode-mixed"); err != nil {
		t.Fatal(err)
	}
	final, err := store.GetExecution(ctx, "x-mixed")
	if err != nil || final.Status != execution.StatusCompleted {
		t.Fatalf("unexpected execution: %#v %v", final, err)
	}
	raw, readErr := os.ReadFile(filepath.Join(root, "config.yaml"))
	if readErr != nil || string(raw) != "debug: false\nreplicas: 2\n" {
		t.Fatalf("configuration not patched: %q %v", raw, readErr)
	}
	events, err := store.List(ctx, substrate.EventFilter{EpisodeID: "episode-mixed"})
	if err != nil {
		t.Fatal(err)
	}
	observations, effects := 0, 0
	for _, event := range events {
		if event.Type == world.EventWorldObservation {
			observations++
			if event.Payload["observation_type"] != world.ObservationFileContent {
				t.Fatalf("unexpected observation: %#v", event.Payload)
			}
			if event.Payload["execution_id"] != "x-mixed" {
				t.Fatalf("observation missing execution linkage: %#v", event.Payload)
			}
		}
		if event.Type == world.EventWorldEffect {
			effects++
			if event.Payload["effect_type"] != world.EffectFilePatched {
				t.Fatalf("unexpected effect: %#v", event.Payload)
			}
			if event.Payload["execution_id"] != "x-mixed" {
				t.Fatalf("effect missing execution linkage: %#v", event.Payload)
			}
		}
	}
	if observations != 1 || effects != 1 {
		t.Fatalf("expected 1 observation + 1 effect, got %d + %d", observations, effects)
	}
}
