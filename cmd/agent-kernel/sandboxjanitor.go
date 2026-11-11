package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/temporality-project/temporality/kernel/sandbox"
	"github.com/temporality-project/temporality/workspace"
)

// Run-aware sandbox janitor (the third strike on sandbox leaks). The existing
// reapers are deliberately conservative — an unfinished background-op marker
// or any extra process in `docker top` keeps a container — because a LIVE
// run can resume into its keep-alive container after a kernel restart. The
// arbiter they all lack is the run itself: when the workspace says the run is
// TERMINAL, no workflow will ever re-attach, so abandoned op markers and
// leftover processes are not "work" anymore — they are the leak. This janitor
// force-removes containers of terminal runs after a grace period; rows that
// do not exist are left alone (delegated child runs carry no workspace_run
// row and are still very much alive).
type sandboxJanitor struct {
	// sandbox lists live run containers and force-removes a key's containers.
	// *sandbox.Swappable satisfies it; tests substitute a recorder.
	sandbox sandboxJanitorSandbox
	// runs resolves a run row by id; terminal verdicts come from the
	// workspace, not from the container.
	run      func(ctx context.Context, runID string) (workspace.Run, error)
	grace    time.Duration // quiet time after the run's last update before force removal
	interval time.Duration
	log      *slog.Logger
	// removed counts force-removed containers across passes (tests).
	removed func(key, name string)
}

// sandboxJanitorSandbox is the janitor's view of the runner.
type sandboxJanitorSandbox interface {
	ListScratchContainers(ctx context.Context) ([]sandbox.ScratchContainerInfo, error)
	ForceRemoveRunContainers(key string)
}

func newSandboxJanitor(sandbox sandboxJanitorSandbox, run func(ctx context.Context, runID string) (workspace.Run, error), log *slog.Logger) *sandboxJanitor {
	grace := 30 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("KERNEL_SANDBOX_TERMINAL_GRACE")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			grace = parsed
		} else {
			slog.Warn("KERNEL_SANDBOX_TERMINAL_GRACE is not a valid duration, using 30m", "value", raw)
		}
	}
	return &sandboxJanitor{
		sandbox:  sandbox,
		run:      run,
		grace:    grace,
		interval: 10 * time.Minute,
		log:      log,
		removed:  func(string, string) {},
	}
}

// sweep runs one janitor pass: list containers → group by scratch key →
// resolve each run once → force-remove the containers of terminal runs past
// the grace. Errors are per-container best-effort: a listing failure ends
// the pass, everything else logs and moves on.
func (j *sandboxJanitor) sweep(ctx context.Context) {
	containers, err := j.sandbox.ListScratchContainers(ctx)
	if err != nil {
		j.log.Warn("sandbox janitor list", "error", err)
		return
	}
	byKey := map[string][]string{}
	for _, c := range containers {
		if strings.TrimSpace(c.Key) == "" {
			continue // unlabelled/foreign container — the orphan reaper's domain
		}
		byKey[c.Key] = append(byKey[c.Key], c.Name)
	}
	for key, names := range byKey {
		project, runID, ok := splitScratchKey(key)
		if !ok {
			continue
		}
		row, err := j.run(ctx, runID)
		if err != nil {
			continue // no row (delegated children!) or a lookup failure: never a verdict
		}
		if liveRunStatus(row.Status) {
			continue // the run owns its container
		}
		if !time.Now().UTC().After(row.UpdatedAt.UTC().Add(j.grace)) {
			continue // the run just finished: give the tail (answer delivery, presentations) its quiet minute
		}
		for _, name := range names {
			j.removed(key, name)
		}
		j.log.Info("sandbox janitor removed terminal-run containers", "project", project, "run", runID, "status", row.Status, "containers", len(names))
		j.sandbox.ForceRemoveRunContainers(key)
	}
}

// splitScratchKey parses the "<project>/<run>" scratch key. A run id carries
// its own slashes (run/delegate/01), so the project is only ever the FIRST
// segment and everything after it is the run.
func splitScratchKey(key string) (project, runID string, ok bool) {
	project, runID, found := strings.Cut(key, "/")
	if !found || project == "" || runID == "" {
		return "", "", false
	}
	return project, runID, true
}

// run starts the janitor loop until the context is cancelled.
func (j *sandboxJanitor) runLoop(ctx context.Context) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.sweep(ctx)
		}
	}
}
