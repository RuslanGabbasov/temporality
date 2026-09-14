package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/temporality-project/temporality/frp/execution"
	"github.com/temporality-project/temporality/frp/execution/worker"
	"github.com/temporality-project/temporality/frp/planner"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	episode := os.Getenv("EPISODE_ID")
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	store, err := postgres.Open(ctx, url)
	if err != nil {
		log.Error("open store", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	adapter := worker.SafeAdapter{}
	plannerRunner := &worker.DurablePlannerRunner{Store: store, Planner: &planner.ArgumentAdapter{}, Adapter: adapter, Now: time.Now}
	runner := worker.Worker{Store: store, Workflows: map[string]execution.DeterministicWorkflow{"inspect_environment": worker.InspectEnvironmentWorkflow{}}, Adapter: adapter, NewID: newID, Now: time.Now, PlannerRunner: plannerRunner}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	log.Info("Temporality executor started")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processed, runErr := runner.RunOnce(ctx, episode)
			if runErr != nil {
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
