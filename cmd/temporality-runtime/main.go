package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/temporality-project/temporality/frp/runtime/httpapi"
	"github.com/temporality-project/temporality/frp/substrate"
	"github.com/temporality-project/temporality/frp/substrate/memory"
	"github.com/temporality-project/temporality/frp/substrate/postgres"
)

type closer interface{ Close() }

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx := context.Background()
	store, closeStore, err := openStore(ctx, log)
	if err != nil {
		log.Error("initialize store", "error", err)
		os.Exit(1)
	}
	defer closeStore()

	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	server := &http.Server{Addr: address, Handler: httpapi.New(store, log), ReadHeaderTimeout: 5 * time.Second}
	log.Info("Temporality runtime started", "address", address, "protocol", "frp", "version", "0.3")
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}

func openStore(ctx context.Context, log *slog.Logger) (substrate.EventStore, func(), error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Warn("DATABASE_URL is not set; using ephemeral memory store")
		return memory.New(), func() {}, nil
	}
	store, err := postgres.Open(ctx, url)
	if err != nil {
		return nil, nil, err
	}
	if err = store.Migrate(ctx, "migrations/000001_event_store.up.sql"); err != nil {
		store.Close()
		return nil, nil, err
	}
	return store, store.Close, nil
}
