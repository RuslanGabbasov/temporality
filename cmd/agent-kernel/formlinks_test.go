package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The hosted form page is reached through whatever prefix fronts the kernel —
// nginx forwards /kernel-api intact (the deployment URL is
// https://<ui>/kernel-api/forms/<token>), the vite dev proxy the same, direct
// :8090 access none. Its script must therefore derive the API base from the
// page's own location instead of hard-coding root-relative /forms/... — a
// root-relative fetch behind the prefix lands on some other service's routes
// (in the field: a bearer-token 401 from the SPA infra) and the form shows
// «a valid bearer token is required» despite the link carrying the secret.
func TestFormLinkPageFetchesUnderItsOwnPrefix(t *testing.T) {
	page := formLinkPageHTML("tok")

	if !strings.Contains(page, "location.pathname.replace") {
		t.Fatal("the page must derive its API base from location.pathname")
	}
	// No literal root-relative fetches: every forms call goes through BASE.
	if match := regexp.MustCompile(`fetch\('/forms/`).FindAllStringIndex(page, -1); len(match) != 0 {
		t.Fatalf("found %d root-relative form fetch(es), they must ride the derived BASE", len(match))
	}
	for _, want := range []string{
		"BASE + '/forms/' + encodeURIComponent(TOKEN) + '/data'",
		"BASE + '/forms/' + encodeURIComponent(TOKEN) + '/answer'",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}

	// The BASE expression itself: simulate it against the paths the page is
	// actually served under (same regex semantics as the JS replace).
	stripForms := func(path string) string {
		return regexp.MustCompile(`/forms/[^/]*$`).ReplaceAllString(path, "")
	}
	for path, wantBase := range map[string]string{
		"/kernel-api/forms/tok": "/kernel-api", // nginx deployment
		"/forms/tok":            "",            // direct :8090 access
	} {
		if got := stripForms(path); got != wantBase {
			t.Errorf("base derivation for %q: got %q, want %q", path, got, wantBase)
		}
	}

	// And the page still resolves through the mux under the proxy prefix: the
	// strip in kernelRootHandler hands /kernel-api/forms/tok to the route that
	// RegisterFormLinkRoutes mounts (nil store is fine — the page route does
	// not touch it).
	mux := http.NewServeMux()
	RegisterFormLinkRoutes(mux, nil, nil, nil, silentWarner{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stripKernelAPIProxyPrefix(r)
		mux.ServeHTTP(w, r)
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://ui/kernel-api/forms/tok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("page under proxy prefix: status %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "location.pathname.replace") {
		t.Fatal("served page must carry the prefix-aware script")
	}
}

type silentWarner struct{}

func (silentWarner) Warn(string, ...any) {}
