package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/temporality-project/temporality/observation/postgres"
)

const (
	retentionIntervalEnv     = "TEMPORALITY_JOURNAL_RETENTION_INTERVAL"
	retentionAgeEnv          = "TEMPORALITY_JOURNAL_RETENTION_AGE"
	defaultRetentionInterval = 6 * time.Hour
	defaultRetentionAge      = 90 * 24 * time.Hour
	retentionOff             = "off"
)

// retentionConfig is the parsed TEMPORALITY_JOURNAL_RETENTION_* configuration.
type retentionConfig struct {
	Interval time.Duration // sweep period
	Age      time.Duration // events older than this are deleted
	Enabled  bool
}

// retentionConfigFromEnv parses the retention configuration; pure so the env
// rules are unit-testable. INTERVAL: "off" disables the sweeper; empty, "0",
// negative or unparsable values fall back to the 6h default — a typo must not
// silently change the schedule. AGE: "off" disables; everything else
// unparsable (including "0", which would wipe the whole journal every tick)
// falls back to the 90d default.
func retentionConfigFromEnv(getenv func(string) string) retentionConfig {
	cfg := retentionConfig{Interval: defaultRetentionInterval, Age: defaultRetentionAge, Enabled: true}
	if value := getenv(retentionIntervalEnv); value != "" {
		if value == retentionOff {
			cfg.Enabled = false
		} else if d, err := time.ParseDuration(value); err == nil && d > 0 {
			cfg.Interval = d
		}
	}
	if value := getenv(retentionAgeEnv); value != "" {
		if value == retentionOff {
			cfg.Enabled = false
		} else if d, err := time.ParseDuration(value); err == nil && d > 0 {
			cfg.Age = d
		}
	}
	return cfg
}

// runRetentionSweeper deletes expired observation events every cfg.Interval,
// starting with one sweep immediately — a restart must not wait a full
// interval to trim a backlog. Errors are logged and retried on the next tick;
// RetentionSweep itself honors context cancellation between batches.
func runRetentionSweeper(log *slog.Logger, store *postgres.Store, cfg retentionConfig) {
	go func() {
		sweep := func() {
			// Plain Background: the journal has no graceful shutdown to
			// observe; the sweep stays bounded by the batch loop's ctx check.
			deleted, err := store.RetentionSweep(context.Background(), cfg.Age, 0)
			if err != nil {
				log.Warn("observation retention sweep failed", "deleted", deleted, "age", cfg.Age, "error", err)
				return
			}
			log.Info("observation retention sweep", "deleted", deleted, "age", cfg.Age)
		}
		sweep()
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for range ticker.C {
			sweep()
		}
	}()
}
