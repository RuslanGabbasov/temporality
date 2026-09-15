package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/memory"
	"github.com/temporality-project/temporality/frp/world"
)

func fixtureWorld(t *testing.T, root string) world.World {
	t.Helper()
	value := world.World{
		WorldID:      "test-world",
		StateVersion: 1,
		Resources:    []world.Resource{{ID: "workspace", Type: world.ResourceFilesystem, Path: root}},
		Capabilities: []string{"filesystem.read", "git.read"},
	}
	value.ApplyDefaults()
	if err := value.Validate(); err != nil {
		t.Fatalf("world validate: %v", err)
	}
	return value
}

func testClock() time.Time { return time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC) }

func idProvider(prefix string) func() string {
	var counter int64
	return func() string {
		return fmt.Sprintf("%s-%06d", prefix, atomic.AddInt64(&counter, 1))
	}
}

func write(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunnerWalkAndExtract covers the full M13 flow against a real directory:
// observations become world.observation events with ingestion provenance and
// extractors create candidate claims citing those events as evidence.
func TestRunnerWalkAndExtract(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/demo\n\ngo 1.23\n\nrequire github.com/x/y v1.2.3\n")
	write(t, root, "README.md", "# Demo Project\n\nSome prose.\n")
	write(t, root, "package.json", `{"name":"demo","version":"1.0.0","dependencies":{"react":"^18.0.0"}}`)
	if err := os.MkdirAll(filepath.Join(root, "cmd", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, filepath.Join("cmd", "demo", "main.go"), "package main\n")
	write(t, root, filepath.Join("cmd", "demo", "package.json"), `{"name":"nested"}`)

	store := memory.New()
	runner := &Runner{World: fixtureWorld(t, root), Events: store, Claims: store, NewID: idProvider("id"), Now: testClock}
	request := Request{WorldID: "test-world", ResourceID: "workspace", EpisodeID: "ep-1", Depth: 2}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ResourceType != world.ResourceFilesystem {
		t.Fatalf("resource type = %q", result.ResourceType)
	}
	// Expected observations: stat root, list root, read go.mod, read README.md,
	// read package.json, list cmd, list cmd/demo, read cmd/demo/package.json.
	if len(result.Observations) != 8 {
		t.Fatalf("observations = %d, want 8: %+v", len(result.Observations), result.Observations)
	}
	if result.Truncated {
		t.Fatal("run reported truncation unexpectedly")
	}
	propositions := map[string]ClaimRecord{}
	for _, claim := range result.Claims {
		propositions[claim.Proposition] = claim
	}
	expected := []string{
		`Workspace resource "workspace" contains 4 top-level entries`,
		`Go module "example.com/demo" is declared at go.mod`,
		`Go module "example.com/demo" targets Go 1.23`,
		`Project documentation at README.md is titled "Demo Project"`,
		`Project at package.json is npm package "demo" version 1.0.0`,
		`Npm package "demo" at package.json declares 1 direct dependencies`,
		`Project at cmd/demo/package.json is npm package "nested"`,
	}
	for _, want := range expected {
		claim, ok := propositions[want]
		if !ok {
			t.Fatalf("missing proposition %q; have %+v", want, keys(propositions))
		}
		if claim.Confidence <= 0 || claim.Confidence >= 1 {
			t.Fatalf("confidence for %q must stay in (0,1), got %v", want, claim.Confidence)
		}
		if len(claim.Evidence) == 0 {
			t.Fatalf("claim %q has no evidence", want)
		}
	}
	// Every claim must cite real observation events committed by this run.
	committed := map[string]struct{}{}
	for _, observation := range result.Observations {
		committed[observation.EventID] = struct{}{}
	}
	for _, claim := range result.Claims {
		for _, id := range claim.Evidence {
			if _, ok := committed[id]; !ok {
				t.Fatalf("claim %q cites unknown evidence %q", claim.Proposition, id)
			}
		}
	}
	// Observation events carry ingestion provenance and the episode scope.
	events, err := store.List(context.Background(), substrate.EventFilter{EpisodeID: "ep-1"})
	if err != nil {
		t.Fatal(err)
	}
	observationEvents := 0
	for _, event := range events {
		if event.Type != world.EventWorldObservation {
			continue
		}
		observationEvents++
		if event.Provenance["source"] != "ingestion" {
			t.Fatalf("observation provenance = %v", event.Provenance)
		}
		if event.Payload["world_id"] != "test-world" || event.Payload["world_version"] != 1 {
			t.Fatalf("observation payload world = %v", event.Payload)
		}
		if event.Payload["source_resource_id"] != "workspace" {
			t.Fatalf("observation payload resource id = %v", event.Payload)
		}
	}
	if observationEvents != len(result.Observations) {
		t.Fatalf("event log holds %d observations, result reports %d", observationEvents, len(result.Observations))
	}
	// Claims were committed with evidence and read back through the store.
	for _, claim := range result.Claims {
		evidence, err := store.ListClaimEvidence(context.Background(), claim.ClaimID)
		if err != nil {
			t.Fatalf("list evidence: %v", err)
		}
		if len(evidence) != len(claim.Evidence) {
			t.Fatalf("stored evidence = %v, want %v", evidence, claim.Evidence)
		}
	}
}

func keys(values map[string]ClaimRecord) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

// TestRunnerBudgetTruncation verifies the event budget stops the walk and is
// reported instead of silently dropping parts of the workspace.
func TestRunnerBudgetTruncation(t *testing.T) {
	root := t.TempDir()
	write(t, root, "README.md", "# Small\n")
	for _, name := range []string{"a", "b", "c", "d"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store := memory.New()
	runner := &Runner{World: fixtureWorld(t, root), Events: store, Claims: store, NewID: idProvider("id"), Now: testClock}
	result, err := runner.Run(context.Background(), Request{WorldID: "test-world", ResourceID: "workspace", MaxEvents: 3, Depth: 1})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.Truncated {
		t.Fatal("expected truncated run")
	}
	if len(result.Observations) > 3 {
		t.Fatalf("observations = %d, budget was 3", len(result.Observations))
	}
}

// TestRunnerUnknownResource verifies requests outside declared resources fail
// before any observation is committed.
func TestRunnerUnknownResource(t *testing.T) {
	store := memory.New()
	runner := &Runner{World: fixtureWorld(t, t.TempDir()), Events: store, Claims: store, NewID: idProvider("id"), Now: testClock}
	_, err := runner.Run(context.Background(), Request{WorldID: "test-world", ResourceID: "nope"})
	if err == nil || !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

// TestRunnerFileResource verifies a resource pointing at a single file reads
// its content instead of walking.
func TestRunnerFileResource(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "go.mod")
	if err := os.WriteFile(path, []byte("module example.com/single\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	value := fixtureWorld(t, root)
	value.Resources = []world.Resource{{ID: "single", Type: world.ResourceFilesystem, Path: path}}
	store := memory.New()
	runner := &Runner{World: value, Events: store, Claims: store, NewID: idProvider("id"), Now: testClock}
	result, err := runner.Run(context.Background(), Request{WorldID: "test-world", ResourceID: "single"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Observations) != 2 {
		t.Fatalf("observations = %d, want stat+read", len(result.Observations))
	}
	if len(result.Claims) != 2 {
		t.Fatalf("claims = %+v", result.Claims)
	}
	for _, claim := range result.Claims {
		if !strings.Contains(claim.Proposition, "example.com/single") {
			t.Fatalf("unexpected claim %q", claim.Proposition)
		}
	}
}
