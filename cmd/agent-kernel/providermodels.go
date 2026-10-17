package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// Provider model discovery: workspace_provider.models mirrors the provider's
// own catalog (GET {base_url}/models — the OpenAI-compatible listing adult
// harnesses read). The catalog syncs in the background (on start, on a
// schedule and right after a provider is created or edited), a manual refresh
// endpoint covers "do it now", and a probe endpoint previews the catalog for
// a form that is not saved yet.

// providerModelStore is the storage surface the catalog sync needs; the real
// workspace store satisfies it and tests bring fakes.
type providerModelStore interface {
	ListProviders(ctx context.Context) ([]workspace.Provider, error)
	GetProvider(ctx context.Context, id string) (workspace.Provider, error)
	UpdateProvider(ctx context.Context, p workspace.Provider) error
}

// providerModelKeyResolver turns an api_key_ref into a bearer token (an empty
// ref resolves to an empty key — keyless providers are legitimate).
type providerModelKeyResolver func(ctx context.Context, ref string) (string, error)

// Catalog ownership lives in the provider labels so it round-trips through
// the API and the UI: "discovered" rows mirror the upstream catalog and the
// background loop keeps them in sync; "manual" rows were curated by an
// operator and the loop must not touch them.
const (
	providerModelsSourceLabel = "models_source"
	providerModelsManual      = "manual"
	providerModelsDiscovered  = "discovered"
)

func markProviderModelsSource(labels map[string]string, source string) map[string]string {
	if labels == nil {
		labels = map[string]string{}
	}
	labels[providerModelsSourceLabel] = source
	return labels
}

type ProviderModelSync struct {
	store  providerModelStore
	keys   providerModelKeyResolver
	client *http.Client
	log    *slog.Logger

	// mu guards inflight: at most one refresh per provider at a time.
	mu       sync.Mutex
	inflight map[string]struct{}
}

func NewProviderModelSync(store providerModelStore, keys providerModelKeyResolver, log *slog.Logger) *ProviderModelSync {
	return &ProviderModelSync{
		store:    store,
		keys:     keys,
		client:   &http.Client{Timeout: 15 * time.Second},
		log:      log,
		inflight: make(map[string]struct{}),
	}
}

// RegisterRoutes mounts the manual sync endpoints next to the provider CRUD.
// Both take the operator role: they can trigger outbound calls that use the
// provider credential and mutate the stored catalog — the same right as
// editing the provider row itself.
func (s *ProviderModelSync) RegisterRoutes(mux *http.ServeMux, gate *controlplane.Gate) {
	mux.HandleFunc("POST /v1/workspace/providers/{providerID}/refresh-models", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		models, err := s.Refresh(r.Context(), r.PathValue("providerID"))
		if err != nil {
			s.writeSyncError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"models": models, "count": len(models)})
	})

	// Probe previews the catalog of a not-yet-saved provider (the form's
	// base_url/key) without touching the store — "what would we discover".
	mux.HandleFunc("POST /v1/workspace/providers/probe-models", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleOperator) {
			return
		}
		var req struct {
			BaseURL   string `json:"base_url"`
			APIKeyRef string `json:"api_key_ref"`
		}
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, 400, err)
			return
		}
		if strings.TrimSpace(req.BaseURL) == "" {
			writeError(w, 422, errors.New("base_url is required"))
			return
		}
		models, err := s.Probe(r.Context(), req.BaseURL, req.APIKeyRef)
		if err != nil {
			s.writeSyncError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"models": models, "count": len(models)})
	})
}

// writeSyncError maps sync failures to statuses a UI can act on: a missing
// provider row is 404, an unresolvable credential is 422 (fix the form), and
// anything the upstream said is 502.
func (s *ProviderModelSync) writeSyncError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		writeError(w, 404, err)
	case errors.Is(err, errCredential):
		writeError(w, 422, err)
	default:
		writeError(w, 502, err)
	}
}

// errCredential marks a key that could not be resolved from its reference.
var errCredential = errors.New("provider credential")

// Refresh fetches the catalog for a stored provider and replaces its Models:
// the provider is the source of truth, so a model dropped upstream must leave
// routing and pickers too. An empty catalog is treated as an error — a wiped
// list is far more likely a broken endpoint than a provider that lost every
// model at once.
func (s *ProviderModelSync) Refresh(ctx context.Context, providerID string) ([]string, error) {
	provider, err := s.store.GetProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	models, err := s.discover(ctx, provider.BaseURL, provider.APIKeyRef)
	if err != nil {
		return nil, err
	}
	// Re-read right before writing so only Models (and the ownership label)
	// differ from what the user last saved; the fetch above takes seconds and
	// a concurrent edit must not be silently reverted.
	current, err := s.store.GetProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	modelsChanged := !slices.Equal(current.Models, models)
	labelChanged := current.Labels[providerModelsSourceLabel] != providerModelsDiscovered
	if !modelsChanged && !labelChanged {
		return models, nil
	}
	current.Models = models
	current.Labels = markProviderModelsSource(current.Labels, providerModelsDiscovered)
	if err := s.store.UpdateProvider(ctx, current); err != nil {
		return nil, err
	}
	s.log.Info("provider models refreshed", "provider", providerID, "count", len(models))
	return models, nil
}



// Probe resolves the credential and fetches the catalog without persisting.
func (s *ProviderModelSync) Probe(ctx context.Context, baseURL, apiKeyRef string) ([]string, error) {
	return s.discover(ctx, baseURL, apiKeyRef)
}

func (s *ProviderModelSync) discover(ctx context.Context, baseURL, apiKeyRef string) ([]string, error) {
	key, err := s.keys(ctx, apiKeyRef)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errCredential, err)
	}
	return fetchProviderModels(ctx, s.client, baseURL, key)
}

// RefreshAsync fire-and-forgets a refresh for one provider; a sync already in
// flight for it is skipped so hooks fired close together (save + loop tick)
// do not stampede the upstream.
func (s *ProviderModelSync) RefreshAsync(ctx context.Context, providerID string) {
	if !s.begin(providerID) {
		return
	}
	go s.refreshGuarded(ctx, providerID)
}

func (s *ProviderModelSync) begin(providerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.inflight[providerID]; busy {
		return false
	}
	s.inflight[providerID] = struct{}{}
	return true
}

func (s *ProviderModelSync) end(providerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, providerID)
}

// Loop syncs every provider on start and then on the schedule; interval <= 0
// disables the loop (manual refresh stays available).
func (s *ProviderModelSync) Loop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	s.refreshAll(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshAll(ctx)
		}
	}
}

func (s *ProviderModelSync) refreshAll(ctx context.Context) {
	providers, err := s.store.ListProviders(ctx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.log.Warn("list providers for model sync", "error", err)
		}
		return
	}
	for _, provider := range providers {
		if ctx.Err() != nil {
			return
		}
		if provider.Labels[providerModelsSourceLabel] == providerModelsManual {
			continue
		}
		// The loop is synchronous (it already runs on its own goroutine) and
		// honors the inflight guard, so a manual refresh started from an HTTP
		// handler at the same moment is skipped instead of racing.
		if !s.begin(provider.ID) {
			continue
		}
		s.refreshGuarded(ctx, provider.ID)
	}
}

// refreshGuarded runs one refresh under the inflight guard; the caller must
// have acquired it via begin.
func (s *ProviderModelSync) refreshGuarded(ctx context.Context, providerID string) {
	defer s.end(providerID)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := s.Refresh(ctx, providerID); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Warn("provider model refresh failed", "provider", providerID, "error", err)
	}
}

// providerModelsRefreshInterval reads KERNEL_PROVIDER_MODELS_REFRESH (a Go
// duration; default 24h, "0" disables the loop — manual refresh still works).
func providerModelsRefreshInterval(log *slog.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv("KERNEL_PROVIDER_MODELS_REFRESH"))
	if raw == "" {
		return 24 * time.Hour
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval < 0 {
		log.Warn("invalid KERNEL_PROVIDER_MODELS_REFRESH, using 24h", "value", raw)
		return 24 * time.Hour
	}
	return interval
}

// fetchProviderModels reads the OpenAI-compatible catalog endpoint. Every
// adult harness (and provider) speaks it: OpenAI, Anthropic, OpenRouter,
// OpenAI-compatible gateways (z.ai, xiaomi), local ollama/lmstudio. A bare
// top-level array is accepted too — several gateways return it without the
// {"data": …} envelope.
func fetchProviderModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s: %s", endpoint, errorSnippet(resp.StatusCode, body))
	}
	models, err := parseProviderModels(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", endpoint, err)
	}
	return models, nil
}

// errorSnippet turns an upstream failure into one diagnosable line: status
// plus a short body excerpt (rate limits and auth failures live there).
func errorSnippet(status int, body []byte) string {
	snippet := strings.Join(strings.Fields(string(body)), " ")
	if len(snippet) > 200 {
		snippet = snippet[:200] + "…"
	}
	if snippet == "" {
		return fmt.Sprintf("HTTP %d (no body)", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, snippet)
}

type providerModelEntry struct {
	ID string `json:"id"`
}

func parseProviderModels(body []byte) ([]string, error) {
	var envelope struct {
		Data []providerModelEntry `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Data) > 0 {
		return normalizeModelIDs(envelope.Data), nil
	}
	var list []providerModelEntry
	if err := json.Unmarshal(body, &list); err == nil && len(list) > 0 {
		return normalizeModelIDs(list), nil
	}
	return nil, errors.New("no models listed (expected {\"data\":[{\"id\":…}]} or [{\"id\":…}])")
}

func normalizeModelIDs(entries []providerModelEntry) []string {
	seen := make(map[string]struct{}, len(entries))
	models := make([]string, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	slices.Sort(models)
	return models
}
