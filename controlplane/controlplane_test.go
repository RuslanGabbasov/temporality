package controlplane

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newAuthedHandler(t *testing.T) (http.Handler, *Gate) {
	t.Helper()
	gate, err := NewGate("read-token-1234567890:alice:reader:lighthouse;write-token-1234567890:kernel:writer:lighthouse;op-token-1234567890:ruslan:operator:lighthouse,forge")
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	return gate.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ := FromContext(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"subject":"` + principal.Subject + `"}`))
	})), gate
}

func TestNewGateParseErrors(t *testing.T) {
	if gate, err := NewGate(""); gate != nil || err != nil {
		t.Fatalf("empty config must disable the gate, got %v %v", gate, err)
	}
	for _, raw := range []string{
		"only-token",
		"token:subject:nonsense:*",
		":subject:reader:*",
		"token::reader:*",
		"token:subject:reader:",
	} {
		if _, err := NewGate(raw); err == nil {
			t.Fatalf("expected parse error for %q", raw)
		}
	}
}

func TestAuthenticateEnforcement(t *testing.T) {
	handler, gate := newAuthedHandler(t)
	if !gate.Enabled() {
		t.Fatal("gate must be enabled")
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/observations/events", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", rec.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/observations/events", nil)
	request.Header.Set("Authorization", "Bearer read-token-1234567890")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"subject":"alice"}` {
		t.Fatalf("valid token = %d %s", rec.Code, rec.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/observations/events", nil)
	request.Header.Set("Authorization", "Bearer unknown-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token = %d, want 401", rec.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/observations/events", nil)
	request.Header.Set("Authorization", "read-token-1234567890")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-bearer scheme = %d, want 401", rec.Code)
	}
}

func TestAllowRoleAndProjectMatrix(t *testing.T) {
	_, gate := newAuthedHandler(t)

	// Mirrors real wiring: the middleware authenticates, the handler then
	// authorizes via Allow on the same request.
	allow := func(token string, role Role, projects ...string) bool {
		var allowed bool
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed = gate.Allow(w, r, role, projects...)
		})
		request := httptest.NewRequest(http.MethodPost, "/x", nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		gate.Authenticate(inner).ServeHTTP(httptest.NewRecorder(), request)
		return allowed
	}

	cases := []struct {
		token    string
		role     Role
		projects []string
		want     bool
	}{
		{"read-token-1234567890", RoleReader, []string{"lighthouse"}, true},
		{"read-token-1234567890", RoleReader, []string{"forge"}, false},
		{"read-token-1234567890", RoleReader, []string{""}, false},
		{"read-token-1234567890", RoleWriter, []string{"lighthouse"}, false},
		{"write-token-1234567890", RoleWriter, []string{"lighthouse"}, true},
		{"write-token-1234567890", RoleOperator, []string{"lighthouse"}, false},
		{"write-token-1234567890", RoleReader, []string{"lighthouse"}, true},
		{"op-token-1234567890", RoleOperator, []string{"forge"}, true},
		{"op-token-1234567890", RoleOperator, []string{"lighthouse", "forge"}, true},
		{"op-token-1234567890", RoleOperator, []string{"other"}, false},
	}
	for _, test := range cases {
		if got := allow(test.token, test.role, test.projects...); got != test.want {
			t.Fatalf("Allow(token=%s role=%s projects=%v) = %v, want %v", test.token, test.role, test.projects, got, test.want)
		}
	}
}

func TestDisabledGateAllowsEverything(t *testing.T) {
	gate, err := NewGate("   ")
	if err != nil || gate != nil {
		t.Fatalf("blank config must disable the gate, got %v %v", gate, err)
	}
	handler := gate.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/anything", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled gate = %d, want 200", rec.Code)
	}
	allowRec := httptest.NewRecorder()
	if !gate.Allow(allowRec, httptest.NewRequest(http.MethodPost, "/x", nil), RoleAdmin, "any-project") {
		t.Fatalf("disabled gate must authorize everything, got %d", allowRec.Code)
	}
}

func TestAdminWildcardProjects(t *testing.T) {
	gate, err := NewGate("admin-token-123456789:root:admin:*")
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	var allowed bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed = gate.Allow(w, r, RoleAdmin, "any-project", "")
	})
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	request.Header.Set("Authorization", "Bearer admin-token-123456789")
	gate.Authenticate(inner).ServeHTTP(httptest.NewRecorder(), request)
	if !allowed {
		t.Fatal("admin wildcard must pass unscoped queries")
	}
}
