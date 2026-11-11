package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/sandbox"
	"github.com/temporality-project/temporality/workspace"
)

// janitorSandbox records force-removals; listing is scripted per pass.
type janitorSandbox struct {
	list    []sandbox.ScratchContainerInfo
	forced  []string
	removed []string
}

func (s *janitorSandbox) ListScratchContainers(context.Context) ([]sandbox.ScratchContainerInfo, error) {
	return s.list, nil
}

func (s *janitorSandbox) ForceRemoveRunContainers(key string) {
	s.forced = append(s.forced, key)
}

// TestSandboxJanitorRemovesOnlyTerminalRuns pins the third-strike contract:
// a terminal run's containers go (unfinished op markers and leftover
// processes included), live runs and row-less delegated children keep theirs,
// and the grace window protects a just-finished run's tail.
func TestSandboxJanitorRemovesOnlyTerminalRuns(t *testing.T) {
	old := time.Now().UTC().Add(-2 * time.Hour)
	fresh := time.Now().UTC().Add(-5 * time.Minute)
	runs := map[string]workspace.Run{
		"run-done":     {RunID: "run-done", Status: "completed", UpdatedAt: old},
		"run-fresh":    {RunID: "run-fresh", Status: "completed", UpdatedAt: fresh},
		"run-live":     {RunID: "run-live", Status: "running", UpdatedAt: old},
		"run-failed":   {RunID: "run-failed", Status: "failed", UpdatedAt: old},
		"run-delegate": {RunID: "run-delegate", Status: "started", UpdatedAt: old},
	}
	rec := &janitorSandbox{list: []sandbox.ScratchContainerInfo{
		{Key: "proj/run-done", Name: "c-done"},
		{Key: "proj/run-done", Name: "c-done-stray"}, // a labelled stray shares the key
		{Key: "proj/run-fresh", Name: "c-fresh"},
		{Key: "proj/run-live", Name: "c-live"},
		{Key: "proj/run-delegate", Name: "c-delegate"}, // delegated child: no row
		{Key: "", Name: "c-unlabelled"},                // foreign/ancient: the orphan reaper's domain
	}}
	removedNames := []string{}
	j := &sandboxJanitor{
		sandbox: rec,
		run: func(_ context.Context, runID string) (workspace.Run, error) {
			row, ok := runs[runID]
			if !ok {
				return workspace.Run{}, workspace.ErrNotFound // delegated child: no row
			}
			return row, nil
		},
		grace:    30 * time.Minute,
		interval: time.Hour,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		removed: func(key, name string) {
			removedNames = append(removedNames, name)
		},
	}

	j.sweep(context.Background())

	if len(rec.forced) != 1 || rec.forced[0] != "proj/run-done" {
		t.Fatalf("force-removed = %v, want only proj/run-done", rec.forced)
	}
	if len(removedNames) != 2 || removedNames[0] != "c-done" || removedNames[1] != "c-done-stray" {
		t.Fatalf("removed names = %v, want c-done and c-done-stray", removedNames)
	}
}

// TestSplitScratchKey: the run id carries slashes of its own
// (run/delegate/01) — only the FIRST segment is the project.
func TestSplitScratchKey(t *testing.T) {
	project, runID, ok := splitScratchKey("proj/run/delegate/01")
	if !ok || project != "proj" || runID != "run/delegate/01" {
		t.Fatalf("split = %q/%q ok=%v", project, runID, ok)
	}
	if _, _, ok := splitScratchKey("noseparator"); ok {
		t.Fatal("a key without a separator must not split")
	}
	if _, _, ok := splitScratchKey("/run"); ok {
		t.Fatal("an empty project must not split")
	}
}
