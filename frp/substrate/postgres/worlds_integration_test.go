package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/protocol"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
	"github.com/temporality-project/temporality/frp/world"
)

func TestWorldStatePersistsAndVersions(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, migration := range []string{"../../../migrations/000001_event_store.up.sql", "../../../migrations/000015_worlds.up.sql"} {
		if err = store.Migrate(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	value := world.World{Protocol: protocol.Name, Version: protocol.Version, WorldID: "integration-world", StateVersion: 1, Resources: []world.Resource{{ID: "repo", Type: world.ResourceGitRepository, Path: "/workspace/repo"}}, Capabilities: []string{"filesystem.read", "git.read"}}
	value.ApplyDefaults()
	if err = value.Validate(); err != nil {
		t.Fatal(err)
	}
	event, err := world.StateEvent(value, newTestUUID(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorld(ctx, value, event); err != nil {
		t.Fatal(err)
	}
	// Stale versions are rejected.
	if err = store.SaveWorld(ctx, value, event); err == nil {
		t.Fatal("stale world state accepted")
	}
	bumped := value
	bumped.StateVersion = 2
	bumped.Capabilities = append(bumped.Capabilities, "http.read")
	bumped.Resources = append(bumped.Resources, world.Resource{ID: "api", Type: world.ResourceHTTPEndpoint, Endpoint: "https://api.example.com"})
	bumpedEvent, err := world.StateEvent(bumped, newTestUUID(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorld(ctx, bumped, bumpedEvent); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetWorld(ctx, "integration-world")
	if err != nil {
		t.Fatal(err)
	}
	if stored.StateVersion != 2 || len(stored.Resources) != 2 {
		t.Fatalf("unexpected stored world: %#v", stored)
	}
	if _, err = store.GetWorld(ctx, "missing"); !errors.Is(err, world.ErrWorldNotFound) {
		t.Fatalf("expected world not found, got %v", err)
	}
	listed, err := store.ListWorlds(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("unexpected world list: %#v %v", listed, err)
	}
}
