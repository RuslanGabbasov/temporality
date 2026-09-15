package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/temporality-project/temporality/frp/execution/worker"
	"github.com/temporality-project/temporality/frp/planner"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
	"github.com/temporality-project/temporality/frp/world"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	episode := os.Getenv("EPISODE_ID")
	worldID := os.Getenv("WORLD_ID")
	// M16 lease fencing: identifies this executor so claims, reclaims after
	// crashes, and terminal transitions can be fenced between replicas.
	executorID := os.Getenv("EXECUTOR_ID")
	if executorID == "" {
		host, hostErr := os.Hostname()
		if hostErr != nil {
			host = "localhost"
		}
		executorID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	leaseTTL := worker.DefaultLeaseTTL
	if raw := os.Getenv("EXECUTOR_LEASE_SECONDS"); raw != "" {
		if seconds, parseErr := strconv.Atoi(raw); parseErr == nil && seconds > 0 {
			leaseTTL = time.Duration(seconds) * time.Second
		} else {
			log.Error("EXECUTOR_LEASE_SECONDS must be a positive integer", "value", raw)
			os.Exit(1)
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		log.Error("open store", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	var adapter worker.Adapter
	if worldID != "" {
		bound, worldErr := store.GetWorld(ctx, worldID)
		if worldErr != nil {
			log.Error("load world", "world_id", worldID, "error", worldErr)
			os.Exit(1)
		}
		adapter = world.NewAdapter(bound)
		log.Info("world adapter bound", "world_id", bound.WorldID, "world_version", bound.StateVersion)
	} else {
		adapter = worker.SafeAdapter{}
	}
	plannerRunner := &worker.DurablePlannerRunner{Store: store, Planner: &planner.ArgumentAdapter{}, Adapter: adapter, Now: time.Now}
	workflows := world.StandardWorkflows()
	runner := worker.Worker{Store: store, Workflows: workflows, Adapter: adapter, NewID: newID, Now: time.Now, PlannerRunner: plannerRunner, ExecutorID: executorID, LeaseTTL: leaseTTL}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	log.Info("Temporality executor started", "world_bound", worldID != "", "workflows", len(workflows), "executor_id", executorID, "lease_ttl", leaseTTL.String())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processed, runErr := runner.RunOnce(ctx, episode)
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				log.Error("worker cycle", "error", runErr)
			} else if processed > 0 {
				log.Info("executions processed", "count", processed)
			}
		}
	}
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
