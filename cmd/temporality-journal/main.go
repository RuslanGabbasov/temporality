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
	"github.com/temporality-project/temporality/workspace"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx := context.Background()
	if err := controlplane.LoadFileSecrets("DATABASE_URL", "JOURNAL_AUTH_TOKENS"); err != nil {
		log.Error("load file secrets", "error", err)
		os.Exit(1)
	}

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
	// Shared-knowledge visibility needs the org bindings stored by the
	// kernel. Both services share DATABASE_URL; when the workspace schema is
	// unavailable the journal degrades to project + organization visibility.
	var opts []httpapi.Option
	if ws, err := workspace.Open(ctx, url); err != nil {
		log.Warn("open workspace store; org-unit knowledge visibility disabled", "error", err)
	} else {
		defer ws.Close()
		opts = append(opts,
			httpapi.WithProjectUnits(func(ctx context.Context, project string) ([]string, error) {
				links, err := ws.ProjectOrgUnits(ctx, project)
				if err != nil {
					return nil, err
				}
				units, err := ws.ListOrgUnits(ctx)
				if err != nil {
					return nil, err
				}
				byID := make(map[string]workspace.OrgUnit, len(units))
				for _, u := range units {
					byID[u.ID] = u
				}
				chain := make([]string, 0, len(links)*2)
				seen := make(map[string]bool, len(links)*2)
				for _, id := range links {
					unit, ok := byID[id]
					if !ok {
						continue
					}
					for _, ancestor := range unit.Ancestors() {
						if !seen[ancestor] {
							seen[ancestor] = true
							chain = append(chain, ancestor)
						}
					}
				}
				return chain, nil
			}),
			httpapi.WithUnitExists(func(ctx context.Context, unitID string) bool {
				_, err := ws.GetOrgUnit(ctx, unitID)
				return err == nil
			}),
		)
	}
	server := &http.Server{
		Addr: address, Handler: httpapi.New(store, log, gate, opts...),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Info("Temporality journal started", "address", address, "schema", "temporality.event/1")
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}
