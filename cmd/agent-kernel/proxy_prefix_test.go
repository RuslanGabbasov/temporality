package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// stripKernelAPIProxyPrefix must remove the /kernel-api prefix nginx forwards
// intact (proxy_pass without a URI part), while preserving the escaping of
// run ids that contain encoded slashes — ServeMux wildcard matching runs on
// the escaped path, and PathValue must come back fully decoded.
func TestStripKernelAPIProxyPrefix(t *testing.T) {
	mustRequest := func(raw string) *http.Request {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://kernel"+raw, nil)
		return r
	}

	t.Run("strips prefix keeping encoded slashes", func(t *testing.T) {
		r := mustRequest("/kernel-api/v1/agent/runs/untitled-1%2Fplan%2F01-dev/nudge")
		stripKernelAPIProxyPrefix(r)
		if want := "/v1/agent/runs/untitled-1/plan/01-dev/nudge"; r.URL.Path != want {
			t.Fatalf("Path: got %q, want %q", r.URL.Path, want)
		}
		if want := "/v1/agent/runs/untitled-1%2Fplan%2F01-dev/nudge"; r.URL.EscapedPath() != want {
			t.Fatalf("EscapedPath: got %q, want %q", r.URL.EscapedPath(), want)
		}
	})

	t.Run("bare prefix becomes root", func(t *testing.T) {
		r := mustRequest("/kernel-api")
		stripKernelAPIProxyPrefix(r)
		if r.URL.Path != "/" {
			t.Fatalf("Path: got %q, want \"/\"", r.URL.Path)
		}
		if r.URL.RawPath != "" {
			t.Fatalf("RawPath: got %q, want empty", r.URL.RawPath)
		}
	})

	t.Run("no prefix is untouched", func(t *testing.T) {
		r := mustRequest("/v1/agent/runs/untitled-1%2Fplan%2F01-dev/nudge")
		stripKernelAPIProxyPrefix(r)
		if want := "/v1/agent/runs/untitled-1/plan/01-dev/nudge"; r.URL.Path != want {
			t.Fatalf("Path: got %q, want %q", r.URL.Path, want)
		}
	})

	t.Run("similar prefix is not stripped", func(t *testing.T) {
		r := mustRequest("/kernel-api-v2/healthz")
		stripKernelAPIProxyPrefix(r)
		if r.URL.Path != "/kernel-api-v2/healthz" {
			t.Fatalf("Path: got %q, want unchanged", r.URL.Path)
		}
	})

	t.Run("mux resolves run id with slashes decoded", func(t *testing.T) {
		var gotRunID string
		mux := http.NewServeMux()
		mux.HandleFunc("POST /v1/agent/runs/{runID}/nudge", func(w http.ResponseWriter, r *http.Request) {
			gotRunID = r.PathValue("runID")
			w.WriteHeader(http.StatusAccepted)
		})
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stripKernelAPIProxyPrefix(r)
			mux.ServeHTTP(w, r)
		})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, mustRequest("/kernel-api/v1/agent/runs/untitled-1%2Fplan%2F01-dev/nudge"))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status: got %d, want %d", rec.Code, http.StatusAccepted)
		}
		if want := "untitled-1/plan/01-dev"; gotRunID != want {
			t.Fatalf("PathValue(runID): got %q, want %q", gotRunID, want)
		}
	})
}
