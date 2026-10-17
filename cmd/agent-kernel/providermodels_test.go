package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// fakeProviderModelStore covers the sync's storage surface; the map is the
// provider table, updates record what the sync wrote.
type fakeProviderModelStore struct {
	mu        sync.Mutex
	providers map[string]workspace.Provider
	updates   []workspace.Provider
	getErr    error
}

func (f *fakeProviderModelStore) ListProviders(_ context.Context) ([]workspace.Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []workspace.Provider
	for _, p := range f.providers {
		result = append(result, p)
	}
	return result, nil
}

func (f *fakeProviderModelStore) GetProvider(_ context.Context, id string) (workspace.Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.providers[id]
	if !ok {
		return p, workspace.ErrNotFound
	}
	return p, nil
}

func (f *fakeProviderModelStore) UpdateProvider(_ context.Context, p workspace.Provider) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.providers[p.ID]; !ok {
		return workspace.ErrNotFound
	}
	f.providers[p.ID] = p
	f.updates = append(f.updates, p)
	return nil
}

// newProviderModelServer mounts the sync routes the way main wires them: real
// RBAC gate, fake store, fake key resolver.
func newProviderModelServer(t *testing.T, store *fakeProviderModelStore, keys providerModelKeyResolver) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\noperatortoken:operator:operator:*")
	require.NoError(t, err)
	sync := NewProviderModelSync(store, keys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	sync.RegisterRoutes(mux, gate)
	return httptest.NewServer(gate.Authenticate(mux))
}

// upstreamModels fakes an OpenAI-compatible provider: /models lists the
// catalog; the returned getter exposes the Authorization header the sync sent.
func upstreamModels(t *testing.T, status int, catalog string) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, catalog)
	}))
	t.Cleanup(server.Close)
	return server, func() string {
		mu.Lock()
		defer mu.Unlock()
		return auth
	}
}

func providerModelPost(t *testing.T, server *httptest.Server, path, token, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(raw)
}

func TestProviderModelRefreshPersistsDiscoveredCatalog(t *testing.T) {
	upstream, authOf := upstreamModels(t, 200, `{"data":[{"id":"z-ai-pro"},{"id":"z-ai-turbo"},{"id":"z-ai-pro"}]}`)
	store := &fakeProviderModelStore{providers: map[string]workspace.Provider{
		"prov-1": {ID: "prov-1", Name: "z.ai", BaseURL: upstream.URL + "/v1", APIKeyRef: "secret://z-ai"},
	}}
	server := newProviderModelServer(t, store, func(_ context.Context, ref string) (string, error) {
		require.Equal(t, "secret://z-ai", ref)
		return "test-key", nil
	})
	defer server.Close()

	status, body := providerModelPost(t, server, "/v1/workspace/providers/prov-1/refresh-models", "operatortoken", "")
	require.Equal(t, 200, status)
	var payload struct {
		Models []string `json:"models"`
		Count  int      `json:"count"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	require.Equal(t, []string{"z-ai-pro", "z-ai-turbo"}, payload.Models)
	require.Equal(t, 2, payload.Count)

	updated := store.providers["prov-1"]
	require.Equal(t, []string{"z-ai-pro", "z-ai-turbo"}, updated.Models)
	require.Equal(t, providerModelsDiscovered, updated.Labels[providerModelsSourceLabel])
	require.Equal(t, "Bearer test-key", authOf())
}

func TestProviderModelRefreshReaderForbidden(t *testing.T) {
	store := &fakeProviderModelStore{providers: map[string]workspace.Provider{
		"prov-1": {ID: "prov-1", BaseURL: "http://upstream.invalid/v1"},
	}}
	server := newProviderModelServer(t, store, func(context.Context, string) (string, error) { return "", nil })
	defer server.Close()

	status, _ := providerModelPost(t, server, "/v1/workspace/providers/prov-1/refresh-models", "readertoken", "")
	require.Equal(t, 403, status)
	require.Empty(t, store.updates)
}

func TestProviderModelRefreshUnknownProvider(t *testing.T) {
	store := &fakeProviderModelStore{providers: map[string]workspace.Provider{}}
	server := newProviderModelServer(t, store, func(context.Context, string) (string, error) { return "", nil })
	defer server.Close()

	status, _ := providerModelPost(t, server, "/v1/workspace/providers/missing/refresh-models", "operatortoken", "")
	require.Equal(t, 404, status)
}

func TestProviderModelRefreshUpstreamFailure(t *testing.T) {
	upstream, _ := upstreamModels(t, 500, `{"error":"boom"}`)
	store := &fakeProviderModelStore{providers: map[string]workspace.Provider{
		"prov-1": {ID: "prov-1", BaseURL: upstream.URL, Models: []string{"keep-me"}},
	}}
	server := newProviderModelServer(t, store, func(context.Context, string) (string, error) { return "", nil })
	defer server.Close()

	status, body := providerModelPost(t, server, "/v1/workspace/providers/prov-1/refresh-models", "operatortoken", "")
	require.Equal(t, 502, status)
	require.Contains(t, body, "HTTP 500")
	// A broken upstream must not wipe the stored catalog.
	require.Equal(t, []string{"keep-me"}, store.providers["prov-1"].Models)
	require.Empty(t, store.updates)
}

func TestProviderModelProbeDoesNotPersist(t *testing.T) {
	upstream, _ := upstreamModels(t, 200, `{"data":[{"id":"m1"},{"id":"m0"}]}`)
	store := &fakeProviderModelStore{providers: map[string]workspace.Provider{}}
	server := newProviderModelServer(t, store, func(context.Context, string) (string, error) { return "probe-key", nil })
	defer server.Close()

	status, body := providerModelPost(t, server, "/v1/workspace/providers/probe-models", "operatortoken",
		`{"base_url":"`+upstream.URL+`","api_key_ref":"secret://unsaved"}`)
	require.Equal(t, 200, status)
	require.Contains(t, body, `"models":["m0","m1"]`)
	require.Empty(t, store.updates)
}

func TestProviderModelProbeRequiresBaseURL(t *testing.T) {
	store := &fakeProviderModelStore{providers: map[string]workspace.Provider{}}
	server := newProviderModelServer(t, store, func(context.Context, string) (string, error) { return "", nil })
	defer server.Close()

	status, _ := providerModelPost(t, server, "/v1/workspace/providers/probe-models", "operatortoken", `{"base_url":"  "}`)
	require.Equal(t, 422, status)
}

func TestProviderModelProbeUnresolvedCredential(t *testing.T) {
	store := &fakeProviderModelStore{providers: map[string]workspace.Provider{}}
	server := newProviderModelServer(t, store, func(context.Context, string) (string, error) {
		return "", io.ErrClosedPipe
	})
	defer server.Close()

	status, body := providerModelPost(t, server, "/v1/workspace/providers/probe-models", "operatortoken",
		`{"base_url":"http://upstream.invalid"}`)
	require.Equal(t, 422, status)
	require.Contains(t, body, "credential")
}

func TestParseProviderModelsAcceptsBareArrayAndEnvelope(t *testing.T) {
	envelope, err := parseProviderModels([]byte(`{"data":[{"id":"b"},{"id":"a"},{"id":"b"},{"id":"  "}]}`))
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, envelope)

	bare, err := parseProviderModels([]byte(`[{"id":"x"}]`))
	require.NoError(t, err)
	require.Equal(t, []string{"x"}, bare)

	_, err = parseProviderModels([]byte(`{"data":[]}`))
	require.Error(t, err)
	_, err = parseProviderModels([]byte(`not json`))
	require.Error(t, err)
}

func TestProviderModelSyncSkipsManualCatalogs(t *testing.T) {
	upstream, _ := upstreamModels(t, 200, `{"data":[{"id":"auto"}]}`)
	store := &fakeProviderModelStore{providers: map[string]workspace.Provider{
		"auto": {ID: "auto", BaseURL: upstream.URL},
		"curated": {ID: "curated", BaseURL: upstream.URL, Models: []string{"only-mine"},
			Labels: map[string]string{providerModelsSourceLabel: providerModelsManual}},
	}}
	sync := NewProviderModelSync(store, func(context.Context, string) (string, error) { return "", nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	sync.refreshAll(context.Background())

	require.Equal(t, []string{"auto"}, store.providers["auto"].Models)
	require.Equal(t, providerModelsDiscovered, store.providers["auto"].Labels[providerModelsSourceLabel])
	// The curated row is untouched by the loop.
	require.Equal(t, []string{"only-mine"}, store.providers["curated"].Models)
	require.Equal(t, providerModelsManual, store.providers["curated"].Labels[providerModelsSourceLabel])
}

func TestProviderModelRefreshIntervalDefaultAndOverrides(t *testing.T) {
	require.Equal(t, 24*time.Hour, providerModelsRefreshInterval(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Setenv("KERNEL_PROVIDER_MODELS_REFRESH", "6h")
	require.Equal(t, 6*time.Hour, providerModelsRefreshInterval(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Setenv("KERNEL_PROVIDER_MODELS_REFRESH", "0")
	require.Equal(t, time.Duration(0), providerModelsRefreshInterval(slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Setenv("KERNEL_PROVIDER_MODELS_REFRESH", "nope")
	require.Equal(t, 24*time.Hour, providerModelsRefreshInterval(slog.New(slog.NewTextHandler(io.Discard, nil))))
}


