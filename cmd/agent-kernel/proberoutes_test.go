package main

// The probe endpoints sat behind the bearer gate on the REAL server: the boot
// listener answered /livez publicly, but after the socket swap the same URL
// needed a token — the compose healthcheck (wget, no auth) got 401 forever
// and the container read as dead. kernelRootHandler exempts /livez and
// /healthz; this pins that, along with everything the exemption must NOT
// cover.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
)

func probeTestHandler(t *testing.T) http.Handler {
	t.Helper()
	gate, err := controlplane.NewGate("writertoken:writer:writer:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /v1/workspace/runs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	authed := gate.Authenticate(mux)
	return kernelRootHandler(mux, authed)
}

func get(t *testing.T, handler http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestProbeEndpointsAnswerWithoutToken(t *testing.T) {
	handler := probeTestHandler(t)

	for _, path := range []string{"/livez", "/healthz"} {
		res := get(t, handler, path, "")
		require.Equal(t, http.StatusOK, res.Code, "%s must answer without a token — the healthcheck carries none", path)
	}
	// The nginx proxy prefix strips before the exemption matches: the browser
	// and compose probes may arrive in either shape.
	require.Equal(t, http.StatusOK, get(t, handler, "/kernel-api/livez", "").Code)
}

func TestProbeExemptionStaysNarrow(t *testing.T) {
	handler := probeTestHandler(t)

	// Everything else stays gated: an anonymous caller gets 401.
	require.Equal(t, http.StatusUnauthorized, get(t, handler, "/v1/workspace/runs", "").Code)
	// Probes are GET-only: the exemption is method-scoped, so POST /livez
	// falls through to the gate (which answers before routing ever sees it).
	require.Equal(t, http.StatusUnauthorized, postLivez(t, handler))
}

func postLivez(t *testing.T, handler http.Handler) int {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/livez", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code
}

func TestProbeEndpointsWithTokenStillWork(t *testing.T) {
	handler := probeTestHandler(t)
	require.Equal(t, http.StatusOK, get(t, handler, "/v1/workspace/runs", "writertoken").Code)
}
