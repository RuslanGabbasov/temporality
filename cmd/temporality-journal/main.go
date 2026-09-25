// Command temporality-journal is the Temporality observation journal
// service: append-only event storage, knowledge projection and hint
// retrieval for the Agent Kernel and the Experience Timeline. It is the
// product extraction of the observation endpoints that used to live inside
// the FRP runtime server — see docs/product-architecture.md.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation/httpapi"
	"github.com/temporality-project/temporality/observation/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx := context.Background()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	store, err := postgres.Open(ctx, url)
	if err != nil {
		log.Error("open store", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		log.Error("migrate journal schema", "error", err)
		os.Exit(1)
	}

	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	gate, err := controlplane.NewGate(os.Getenv("JOURNAL_AUTH_TOKENS"))
	if err != nil {
		log.Error("parse JOURNAL_AUTH_TOKENS", "error", err)
		os.Exit(1)
	}
	if !gate.Enabled() {
		log.Warn("JOURNAL_AUTH_TOKENS is empty: authentication disabled; configure tokens before sharing this instance")
	} else {
		log.Info("journal authentication enabled", "tokens", gate.TokenCount())
	}
	server := &http.Server{
		Addr: address, Handler: httpapi.New(store, log, gate),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Info("Temporality journal started", "address", address, "schema", "temporality.event/1")
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}
