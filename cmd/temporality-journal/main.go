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

	// net/http/pprof registers its handlers on http.DefaultServeMux; the
	// debug listener below serves that mux under /debug/ — and only there.
	_ "net/http/pprof"
	"os"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/observation/httpapi"
	"github.com/temporality-project/temporality/observation/postgres"
	"github.com/temporality-project/temporality/workspace"
)

// BuildVersion is injected at link time by the release pipeline
// (-ldflags "-X main.buildVersion=v2026.1.0"); "dev" when built by hand.
var buildVersion = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	log.Info("temporality-journal starting", "version", buildVersion)
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
	// A tokenless instance still needs a live gate object: workspace-user
	// tokens loaded below must be able to switch authentication on without a
	// restart (fresh-instance bootstrap — the first created user locks the
	// anonymous admin out). NewGate returns nil for an empty configuration.
	if gate == nil {
		gate = &controlplane.Gate{}
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
		// Workspace user tokens must authorize on the journal exactly like on
		// the kernel (password login returns the user's bearer token, and the
		// debugger sends it to both /kernel-api and /api). The kernel owns
		// user mutations but cannot notify the journal, so the principals are
		// reloaded on a short interval — created, rotated and revoked tokens
		// apply here within the interval.
		refreshUserTokens := func() {
			reloadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			principals, err := ws.LoadTokenPrincipals(reloadCtx, log)
			if err != nil {
				log.Error("load workspace user tokens", "error", err)
				return
			}
			gate.SetDBTokens(principals)
			log.Info("loaded workspace user tokens", "count", len(principals))
		}
		// Kernel→journal auth: the kernel's auto-provisioned service principal
		// is excluded from the DB token set above (it must not switch the gate
		// on for fresh installs), so it is recognized as the loopback internal
		// credential instead — kernel requests carry it as TEMPORALITY_API_TOKEN
		// and pass the gate even after the first user enables authentication.
		// Re-resolved on every reload, not only at boot: on a first start the
		// kernel applies the workspace schema, so this row does not exist yet
		// and the boot-time mint fails — a once-only read then left every
		// kernel call 401ing until the next full restart.
		refreshInternalToken := func() {
			reloadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			serviceToken, err := ws.EnsureKernelServiceToken(reloadCtx)
			if err != nil {
				// Expected on a very first start (schema not migrated yet); the
				// next tick picks the row up once the kernel created it.
				log.Warn("resolve kernel service token for loopback auth", "error", err)
				return
			}
			if serviceToken == "" || serviceToken == gate.InternalToken() {
				return
			}
			gate.SetInternalToken(serviceToken)
			log.Info("kernel service token installed for loopback auth")
		}
		refreshUserTokens()
		refreshInternalToken()
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				refreshUserTokens()
				refreshInternalToken()
			}
		}()
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
			httpapi.WithTopicAliases(func(ctx context.Context, project string) (map[string]httpapi.AreaAlias, error) {
				raw, err := ws.AreaAliases(ctx, project)
				if err != nil {
					return nil, err
				}
				aliases := make(map[string]httpapi.AreaAlias, len(raw))
				for value, alias := range raw {
					aliases[value] = httpapi.AreaAlias{Canonical: alias.Canonical, Hidden: alias.Hidden}
				}
				return aliases, nil
			}),
			httpapi.WithHumanFactors(func(ctx context.Context) (observation.HumanFactors, error) {
				hab, err := ws.HumanFactors(ctx)
				if err != nil {
					return observation.HumanFactors{}, err
				}
				return observation.HumanFactors{ApprovalSeconds: hab.ApprovalSeconds, AnswerBaseSeconds: hab.AnswerBaseSeconds, ReadingWPM: hab.ReadingWPM}, nil
			}),
		)
	}
	apiServer := httpapi.NewServer(store, log, gate, opts...)
	server := &http.Server{
		Addr: address, Handler: apiServer,
		ReadHeaderTimeout: 5 * time.Second,
	}
	// Debug listener (JOURNAL_DEBUG_ADDR, e.g. ":6060", off by default):
	// net/http/pprof plus the per-route counters — the load-attribution pair.
	// Deliberately NOT the authenticated API: pprof must never be reachable
	// through the public port.
	if debugAddr := os.Getenv("JOURNAL_DEBUG_ADDR"); debugAddr != "" {
		debugMux := http.NewServeMux()
		debugMux.Handle("/debug/counters", apiServer.DebugCounters())
		debugMux.Handle("/debug/", http.DefaultServeMux) // net/http/pprof handlers
		debug := &http.Server{Addr: debugAddr, Handler: debugMux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			log.Info("journal debug listener", "address", debugAddr)
			if err := debug.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("debug serve", "error", err)
			}
		}()
	}
	log.Info("Temporality journal started", "address", address, "schema", "temporality.event/1")
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}
