package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
)

// The OIDC flow against a fake IdP and a fake store — the real verifier,
// discovery, PKCE exchange and session plumbing all run for real, only the
// database and the IdP are fakes (docs/auth-federation.md §5-§6).

// fakeAuthStore satisfies authStore + loginStore without a database.
type fakeAuthStore struct {
	mu sync.Mutex

	cfg    workspace.AuthIdPConfig
	cfgErr error

	requests map[string]workspace.AuthRequest // state -> in-flight request
	sessions map[string]workspace.AuthSession
	users    []workspace.User
	audits   []storedAudit // reuses the eval-gate test sink shape

	createUserCalls int
	loginRecorded   []string
	tokenUpdates    []string
}

func newFakeAuthStore(cfg workspace.AuthIdPConfig) *fakeAuthStore {
	return &fakeAuthStore{
		cfg:      cfg,
		requests: map[string]workspace.AuthRequest{},
		sessions: map[string]workspace.AuthSession{},
	}
}

func (f *fakeAuthStore) GetAuthIdPConfig(context.Context) (workspace.AuthIdPConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg, f.cfgErr
}

func (f *fakeAuthStore) SaveAuthIdPConfig(_ context.Context, cfg workspace.AuthIdPConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
	return nil
}

func (f *fakeAuthStore) CreateAuthRequest(_ context.Context, req *workspace.AuthRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests[req.State] = *req
	return nil
}

func (f *fakeAuthStore) TakeAuthRequest(_ context.Context, state string) (workspace.AuthRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req, ok := f.requests[state]
	if !ok {
		return workspace.AuthRequest{}, workspace.ErrNotFound
	}
	delete(f.requests, state)
	return req, nil
}

func (f *fakeAuthStore) CreateAuthSession(_ context.Context, sess *workspace.AuthSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[sess.ID] = *sess
	return nil
}

func (f *fakeAuthStore) GetAuthSession(_ context.Context, id string) (workspace.AuthSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[id]
	if !ok {
		return workspace.AuthSession{}, workspace.ErrNotFound
	}
	return sess, nil
}

func (f *fakeAuthStore) TouchAuthSession(_ context.Context, id string, seen, expires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[id]
	if !ok {
		return workspace.ErrNotFound
	}
	sess.LastSeen, sess.ExpiresAt = seen, expires
	f.sessions[id] = sess
	return nil
}

func (f *fakeAuthStore) DeleteAuthSession(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
	return nil
}

func (f *fakeAuthStore) GetUserByExternalID(_ context.Context, issuer, externalID string) (workspace.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.ExternalIssuer == issuer && u.ExternalID == externalID {
			return u, nil
		}
	}
	return workspace.User{}, workspace.ErrNotFound
}

func (f *fakeAuthStore) CreateUser(_ context.Context, u *workspace.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createUserCalls++
	f.users = append(f.users, *u)
	return nil
}

func (f *fakeAuthStore) RecordUserLogin(_ context.Context, id, _, _ string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loginRecorded = append(f.loginRecorded, id)
	return nil
}

func (f *fakeAuthStore) RecordAccessAudit(_ context.Context, actor, action, entityKind, entityID string, details map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, storedAudit{actor: actor, action: action, entityKind: entityKind, entityID: entityID, details: details})
	return nil
}

func (f *fakeAuthStore) GetUserByLogin(_ context.Context, login string) (workspace.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.Name == login || u.Email == login {
			return u, nil
		}
	}
	return workspace.User{}, workspace.ErrNotFound
}

func (f *fakeAuthStore) UpdateUserToken(_ context.Context, id, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenUpdates = append(f.tokenUpdates, id+":"+token)
	return nil
}

func (f *fakeAuthStore) GetUser(_ context.Context, id string) (workspace.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return workspace.User{}, workspace.ErrNotFound
}

func (f *fakeAuthStore) SetUserPassword(_ context.Context, id, passwordHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].PasswordHash = passwordHash
			return nil
		}
	}
	return workspace.ErrNotFound
}

func (f *fakeAuthStore) userByExternal(issuer, sub string) (workspace.User, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.ExternalIssuer == issuer && u.ExternalID == sub {
			return u, true
		}
	}
	return workspace.User{}, false
}

func (f *fakeAuthStore) auditActions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	actions := make([]string, 0, len(f.audits))
	for _, a := range f.audits {
		actions = append(actions, a.action)
	}
	return actions
}

// fakeIdP is a minimal OIDC provider: discovery, JWKS and a token endpoint
// that mints RS256-signed id_tokens with whatever claims the test pinned.
type fakeIdP struct {
	server *httptest.Server
	issuer string
	key    *rsa.PrivateKey

	mu    sync.Mutex
	nonce string
	sub   string
	extra map[string]any // additional claims (username/email/…)
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	idp := &fakeIdP{key: key, sub: "jdoe-42", extra: map[string]any{
		"preferred_username": "jdoe",
		"email":              "jdoe@example.com",
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{
			"issuer":                 idp.issuer,
			"authorization_endpoint": idp.issuer + "/auth",
			"token_endpoint":         idp.issuer + "/token",
			"jwks_uri":               idp.issuer + "/jwks.json",
		})
	})
	mux.HandleFunc("GET /jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		jwk := jose.JSONWebKey{Key: &idp.key.PublicKey, KeyID: "test-key", Algorithm: "RS256", Use: "sig"}
		raw, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		idp.mu.Lock()
		claims := map[string]any{
			"iss": idp.issuer,
			"aud": "client",
			"sub": idp.sub,
			"exp": time.Now().Add(time.Hour).Unix(),
			"iat": time.Now().Unix(),
		}
		if idp.nonce != "" {
			claims["nonce"] = idp.nonce
		}
		for k, v := range idp.extra {
			claims[k] = v
		}
		idp.mu.Unlock()
		payload, err := json.Marshal(claims)
		require.NoError(t, err)
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: idp.key},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"))
		require.NoError(t, err)
		object, err := signer.Sign(payload)
		require.NoError(t, err)
		signed, err := object.CompactSerialize()
		require.NoError(t, err)
		writeJSON(w, 200, map[string]any{
			"access_token": "at",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     signed,
		})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	idp.issuer = idp.server.URL
	return idp
}

// newAuthTestServer mounts the auth surface exactly like main: routes, gate,
// session resolver and the root exemptions the browser flow depends on.
func newAuthTestServer(t *testing.T, store *fakeAuthStore) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("admintoken:admin:admin:*\nreadertoken:reader:reader:*")
	require.NoError(t, err)
	deps := authDependencies{
		store:       store,
		resolveKey:  func(context.Context, string) (string, error) { return "s3cret", nil },
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessionTTL:  time.Hour,
		absoluteTTL: 24 * time.Hour,
	}
	mux := http.NewServeMux()
	registerAuthRoutes(mux, deps, gate, newOIDCProviderCache())
	registerPasswordLogin(mux, deps, store, func() {})
	gate.SetSessionResolver(sessionPrincipalResolver(store, func(_ context.Context, userID string) (controlplane.Principal, bool) {
		store.mu.Lock()
		defer store.mu.Unlock()
		for _, u := range store.users {
			if u.ID == userID {
				role, rErr := controlplane.ParseRole(u.Role)
				if rErr != nil {
					return controlplane.Principal{}, false
				}
				return controlplane.Principal{Subject: u.Name, Role: role, Projects: []string{"*"}, UserID: u.ID}, true
			}
		}
		return controlplane.Principal{}, false
	}, deps.log, deps.sessionTTL, deps.absoluteTTL))
	// A gated probe endpoint: proves the cookie carries a live principal.
	mux.HandleFunc("GET /v1/agent/whoami", func(w http.ResponseWriter, r *http.Request) {
		principal, _ := controlplane.FromContext(r.Context())
		writeJSON(w, 200, map[string]any{"subject": principal.Subject, "role": principal.Role.String(), "auth_enabled": true})
	})
	authed := gate.Authenticate(mux)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login" {
			mux.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/auth/methods" {
			mux.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/auth/oidc/") {
			mux.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/logout" {
			mux.ServeHTTP(w, r)
			return
		}
		authed.ServeHTTP(w, r)
	})
	server := httptest.NewServer(root)
	t.Cleanup(server.Close)
	return server
}

// noRedirect stops at the 302s the flow emits so the test can inspect them.
func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func authGet(t *testing.T, server *httptest.Server, path string, cookie string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	require.NoError(t, err)
	if cookie != "" {
		request.Header.Set("Cookie", controlplane.SessionCookie+"="+cookie)
	}
	response, err := noRedirect().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

// startLogin drives GET /v1/auth/oidc/start and returns the one-time state.
func startLogin(t *testing.T, server *httptest.Server, store *fakeAuthStore) string {
	t.Helper()
	response := authGet(t, server, "/v1/auth/oidc/start", "")
	require.Equal(t, 302, response.StatusCode, "start must redirect to the IdP")
	location, err := url.Parse(response.Header.Get("Location"))
	require.NoError(t, err)
	require.NotEmpty(t, location.Query().Get("state"), "authz URL carries the state")
	state := location.Query().Get("state")
	store.mu.Lock()
	_, exists := store.requests[state]
	store.mu.Unlock()
	require.True(t, exists, "state is stored server-side")
	return state
}

func ssoConfig(idp *fakeIdP) workspace.AuthIdPConfig {
	return workspace.AuthIdPConfig{
		Enabled:         true,
		Issuer:          idp.issuer,
		ClientID:        "client",
		ClientSecretRef: "secret://oidc/client",
		DisplayName:     "Corp SSO",
		JITEnabled:      true,
		DefaultRole:     "reader",
	}
}

// The full authorization-code flow: start → IdP → callback issues a session
// cookie that passes the gate; the unknown subject lands as a JIT user with
// the configured role and every step leaves an audit trail.
func TestOIDCFlowIssuesSessionAndJITUser(t *testing.T) {
	idp := newFakeIdP(t)
	store := newFakeAuthStore(ssoConfig(idp))
	server := newAuthTestServer(t, store)

	state := startLogin(t, server, store)
	idp.setNonceFor(t, store, state)

	response := authGet(t, server, "/v1/auth/oidc/callback?code=abc&state="+url.QueryEscape(state), "")
	require.Equal(t, 302, response.StatusCode)
	require.Equal(t, server.URL+"/", response.Header.Get("Location"), "lands on the SPA root")

	rawCookie := response.Header.Get("Set-Cookie")
	require.Contains(t, rawCookie, controlplane.SessionCookie+"=")
	require.Contains(t, rawCookie, "HttpOnly", "the session id is script-invisible")
	sessionID := strings.Split(strings.Split(rawCookie, ";")[0], "=")[1]

	user, ok := store.userByExternal(idp.issuer, "jdoe-42")
	require.True(t, ok, "JIT created the user")
	require.Equal(t, "reader", user.Role)
	require.Equal(t, "federated", user.Sourced)
	require.Equal(t, "jdoe", user.Name)

	store.mu.Lock()
	sess, sessOK := store.sessions[sessionID]
	store.mu.Unlock()
	require.True(t, sessOK, "session row exists")
	require.Equal(t, user.ID, sess.UserID)

	probe := authGet(t, server, "/v1/agent/whoami", sessionID)
	require.Equal(t, 200, probe.StatusCode)
	body, err := io.ReadAll(probe.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"subject":"jdoe"`, "cookie authenticates through the gate")
	require.Contains(t, store.auditActions(), workspace.AuditAuthLoginSucceeded)
	require.Contains(t, store.auditActions(), workspace.AuditAuthUserJITCreated)
}

// setNonce aligns the fake IdP with the nonce stored for this state.
func (idp *fakeIdP) setNonceFor(t *testing.T, store *fakeAuthStore, state string) {
	t.Helper()
	store.mu.Lock()
	req := store.requests[state]
	store.mu.Unlock()
	idp.mu.Lock()
	idp.nonce = req.Nonce
	idp.mu.Unlock()
}

// A second login of the same subject reuses the provisioned account.
func TestOIDCSecondLoginDoesNotDuplicateUser(t *testing.T) {
	idp := newFakeIdP(t)
	store := newFakeAuthStore(ssoConfig(idp))
	server := newAuthTestServer(t, store)

	for i := 0; i < 2; i++ {
		state := startLogin(t, server, store)
		idp.setNonceFor(t, store, state)
		response := authGet(t, server, "/v1/auth/oidc/callback?code=abc&state="+url.QueryEscape(state), "")
		require.Equal(t, 302, response.StatusCode)
	}
	store.mu.Lock()
	created := store.createUserCalls
	users := len(store.users)
	store.mu.Unlock()
	require.Equal(t, 1, created, "one identity, one account")
	require.Equal(t, 1, users)
}

// default_role=deny is "login works, access does not" (§5.5) — the subject
// is known but the installation has no policy for it.
func TestOIDCDenyRoleRefusesLogin(t *testing.T) {
	idp := newFakeIdP(t)
	cfg := ssoConfig(idp)
	cfg.DefaultRole = "deny"
	store := newFakeAuthStore(cfg)
	server := newAuthTestServer(t, store)

	state := startLogin(t, server, store)
	idp.setNonceFor(t, store, state)
	response := authGet(t, server, "/v1/auth/oidc/callback?code=abc&state="+url.QueryEscape(state), "")
	require.Equal(t, 403, response.StatusCode)
}

// JIT disabled: unknown identities are refused with an actionable message.
func TestOIDCJITDisabledRefusesUnknownUser(t *testing.T) {
	idp := newFakeIdP(t)
	cfg := ssoConfig(idp)
	cfg.JITEnabled = false
	store := newFakeAuthStore(cfg)
	server := newAuthTestServer(t, store)

	state := startLogin(t, server, store)
	idp.setNonceFor(t, store, state)
	response := authGet(t, server, "/v1/auth/oidc/callback?code=abc&state="+url.QueryEscape(state), "")
	require.Equal(t, 403, response.StatusCode)
	body, _ := io.ReadAll(response.Body)
	require.Contains(t, string(body), "auto-provisioning")
}

// A replayed or unknown state is single-use by construction: TakeAuthRequest
// deletes the row, so the second callback cannot proceed.
func TestOIDCStateIsSingleUse(t *testing.T) {
	idp := newFakeIdP(t)
	store := newFakeAuthStore(ssoConfig(idp))
	server := newAuthTestServer(t, store)

	state := startLogin(t, server, store)
	idp.setNonceFor(t, store, state)
	callback := "/v1/auth/oidc/callback?code=abc&state=" + url.QueryEscape(state)
	first := authGet(t, server, callback, "")
	require.Equal(t, 302, first.StatusCode)
	second := authGet(t, server, callback, "")
	require.Equal(t, 400, second.StatusCode, "replayed state is rejected")
}

// The nonce binds the id_token to this flow: a token minted for another
// nonce must not authenticate (§5.1).
func TestOIDCNonceMismatchRefuses(t *testing.T) {
	idp := newFakeIdP(t)
	store := newFakeAuthStore(ssoConfig(idp))
	server := newAuthTestServer(t, store)

	state := startLogin(t, server, store)
	idp.mu.Lock()
	idp.nonce = "not-the-stored-nonce"
	idp.mu.Unlock()

	response := authGet(t, server, "/v1/auth/oidc/callback?code=abc&state="+url.QueryEscape(state), "")
	require.Equal(t, 401, response.StatusCode)
}

// Claim names follow the config (§6.2): username/email come from the
// configured claims, not hardcoded JSON fields.
func TestOIDCClaimsUseConfiguredNames(t *testing.T) {
	idp := newFakeIdP(t)
	cfg := ssoConfig(idp)
	cfg.UsernameClaim = "upn"
	cfg.EmailClaim = "mail"
	idp.mu.Lock()
	idp.extra = map[string]any{"upn": "jdoe", "mail": "jdoe@example.com"}
	idp.mu.Unlock()
	store := newFakeAuthStore(cfg)
	server := newAuthTestServer(t, store)

	state := startLogin(t, server, store)
	idp.setNonceFor(t, store, state)
	response := authGet(t, server, "/v1/auth/oidc/callback?code=abc&state="+url.QueryEscape(state), "")
	require.Equal(t, 302, response.StatusCode)
	user, ok := store.userByExternal(idp.issuer, "jdoe-42")
	require.True(t, ok)
	require.Equal(t, "jdoe", user.Name)
	require.Equal(t, "jdoe@example.com", user.Email)
}

// The login screen decoration endpoint answers what the UI may offer.
func TestAuthMethodsReportsSSO(t *testing.T) {
	idp := newFakeIdP(t)
	store := newFakeAuthStore(ssoConfig(idp))
	server := newAuthTestServer(t, store)

	response := authGet(t, server, "/v1/auth/methods", "")
	require.Equal(t, 200, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"enabled":true`)
	require.Contains(t, string(body), "Corp SSO")
}

// PUT config validation: an enabled config must carry the client id.
func TestAuthConfigValidationRequiresClientID(t *testing.T) {
	idp := newFakeIdP(t)
	store := newFakeAuthStore(workspace.AuthIdPConfig{})
	server := newAuthTestServer(t, store)

	cfg := ssoConfig(idp)
	cfg.ClientID = ""
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPut, server.URL+"/v1/workspace/auth/config", strings.NewReader(string(raw)))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer admintoken")
	request.Header.Set("Content-Type", "application/json")
	response, err := noRedirect().Do(request)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, 422, response.StatusCode)
}

// Break-glass (§5.5): with SSO live, local passwords work for admins only —
// and the refusal is the generic 401, indistinguishable from a wrong password.
func TestPasswordLoginBreakGlass(t *testing.T) {
	idp := newFakeIdP(t)
	hash, err := workspace.HashPassword("hunter2")
	require.NoError(t, err)

	loginRequest := func(server *httptest.Server, login, password string) int {
		body, err := json.Marshal(map[string]string{"login": login, "password": password})
		require.NoError(t, err)
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/auth/login", strings.NewReader(string(body)))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		response, err := noRedirect().Do(request)
		require.NoError(t, err)
		_ = response.Body.Close()
		return response.StatusCode
	}

	writer := workspace.User{ID: "u-writer", Name: "bob", Role: "writer", Active: true, PasswordHash: hash}
	admin := workspace.User{ID: "u-admin", Name: "root", Role: "admin", Active: true, PasswordHash: hash}

	// SSO off: password login is the primary path for everyone.
	off := newFakeAuthStore(workspace.AuthIdPConfig{})
	off.users = []workspace.User{writer, admin}
	serverOff := newAuthTestServer(t, off)
	require.Equal(t, 200, loginRequest(serverOff, "bob", "hunter2"))

	// SSO on: non-admins are pushed to the IdP, the admin keeps the key.
	on := newFakeAuthStore(ssoConfig(idp))
	on.users = []workspace.User{writer, admin}
	serverOn := newAuthTestServer(t, on)
	require.Equal(t, 401, loginRequest(serverOn, "bob", "hunter2"))
	require.Equal(t, 200, loginRequest(serverOn, "root", "hunter2"))
	// Wrong password stays a 401 for the admin too — no oracle.
	require.Equal(t, 401, loginRequest(serverOn, "root", "wrong"))
}

// Self-service password change: the signed-in user re-checks the current
// password, meets the 8-character bar, and the hash rotates — while the
// wrong current password and a short new one are refused.
func TestPasswordChangeSelfService(t *testing.T) {
	hash, err := workspace.HashPassword("hunter2")
	require.NoError(t, err)
	store := newFakeAuthStore(workspace.AuthIdPConfig{})
	store.users = []workspace.User{
		{ID: "u-admin", Name: "root", Role: "admin", Active: true, Token: "roottoken", PasswordHash: hash},
		{ID: "u-sso", Name: "jane", Role: "writer", Active: true, Token: "janetoken", ExternalIssuer: "https://idp", ExternalID: "jane"},
	}
	server := newAuthTestServer(t, store)

	// A session for the password user (the resolver maps it to a principal
	// with the user id — the same path the browser flow takes).
	now := time.Now().UTC()
	require.NoError(t, store.CreateAuthSession(context.Background(), &workspace.AuthSession{
		ID: "sess-pw", UserID: "u-admin", CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(time.Hour),
	}))
	require.NoError(t, store.CreateAuthSession(context.Background(), &workspace.AuthSession{
		ID: "sess-sso", UserID: "u-sso", CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(time.Hour),
	}))

	change := func(cookie, current, next string) (int, string) {
		body, err := json.Marshal(map[string]string{"current_password": current, "new_password": next})
		require.NoError(t, err)
		request, rErr := http.NewRequest(http.MethodPost, server.URL+"/v1/auth/password", strings.NewReader(string(body)))
		require.NoError(t, rErr)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Cookie", controlplane.SessionCookie+"="+cookie)
		response, rErr := http.DefaultClient.Do(request)
		require.NoError(t, rErr)
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(raw)
	}

	// Wrong current password → 401, hash untouched.
	status, _ := change("sess-pw", "wrong", "newpassword1")
	require.Equal(t, 401, status)
	admin, _ := store.GetUser(context.Background(), "u-admin")
	require.True(t, workspace.CheckPassword(admin.PasswordHash, "hunter2"))

	// Short new password → 422.
	status, _ = change("sess-pw", "hunter2", "short")
	require.Equal(t, 422, status)

	// Happy path: the hash rotates, and the new password logs the user in.
	status, _ = change("sess-pw", "hunter2", "newpassword1")
	require.Equal(t, 200, status)
	admin, _ = store.GetUser(context.Background(), "u-admin")
	require.True(t, workspace.CheckPassword(admin.PasswordHash, "newpassword1"))
	require.True(t, workspace.CheckPassword(admin.PasswordHash, "hunter2") == false)

	// An SSO user without a local password sets their first one: the
	// principal is already authenticated, there is no current password to
	// re-check — the 8-character bar is the only gate.
	// Short first password → 422, hash still absent.
	status, _ = change("sess-sso", "", "short")
	require.Equal(t, 422, status)

	// First set succeeds.
	status, _ = change("sess-sso", "", "newpassword1")
	require.Equal(t, 200, status)
	sso, _ := store.GetUser(context.Background(), "u-sso")
	require.True(t, workspace.CheckPassword(sso.PasswordHash, "newpassword1"))

	// Now that a local password exists, an empty current one is refused —
	// the re-check applies again.
	status, _ = change("sess-sso", "", "newpassword2")
	require.Equal(t, 401, status)
}

// Logout drops the session row and kills the cookie.
func TestLogoutDropsSession(t *testing.T) {
	idp := newFakeIdP(t)
	store := newFakeAuthStore(ssoConfig(idp))
	server := newAuthTestServer(t, store)

	state := startLogin(t, server, store)
	idp.setNonceFor(t, store, state)
	response := authGet(t, server, "/v1/auth/oidc/callback?code=abc&state="+url.QueryEscape(state), "")
	require.Equal(t, 302, response.StatusCode)
	sessionID := strings.Split(strings.Split(response.Header.Get("Set-Cookie"), ";")[0], "=")[1]

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/auth/logout", nil)
	require.NoError(t, err)
	request.Header.Set("Cookie", controlplane.SessionCookie+"="+sessionID)
	logout, err := noRedirect().Do(request)
	require.NoError(t, err)
	_ = logout.Body.Close()
	require.Equal(t, 204, logout.StatusCode)
	require.Contains(t, logout.Header.Get("Set-Cookie"), "Max-Age=0")

	store.mu.Lock()
	_, exists := store.sessions[sessionID]
	store.mu.Unlock()
	require.False(t, exists, "session row is gone")

	probe := authGet(t, server, "/v1/agent/whoami", sessionID)
	require.Equal(t, 401, probe.StatusCode, "the dead cookie no longer authenticates")
}

// The resolver owns the TTL semantics (§5.2): expired sessions die, live
// ones slide forward within the absolute cap.
func TestSessionResolverExpiry(t *testing.T) {
	store := newFakeAuthStore(workspace.AuthIdPConfig{})
	now := time.Now().UTC()
	store.sessions["live"] = workspace.AuthSession{ID: "live", UserID: "u1", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	store.sessions["slid-out"] = workspace.AuthSession{ID: "slid-out", UserID: "u1", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(time.Minute)}
	store.users = []workspace.User{{ID: "u1", Name: "jdoe", Role: "reader"}}
	store.sessions["expired"] = workspace.AuthSession{ID: "expired", UserID: "u1", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Minute)}

	resolve := sessionPrincipalResolver(store, func(_ context.Context, userID string) (controlplane.Principal, bool) {
		return controlplane.Principal{Subject: userID, Role: controlplane.RoleReader}, true
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 90*time.Minute)

	p, ok := resolve(context.Background(), "live")
	require.True(t, ok)
	require.Equal(t, "u1", p.Subject)

	_, ok = resolve(context.Background(), "expired")
	require.False(t, ok, "expired session is refused")
	store.mu.Lock()
	_, stillThere := store.sessions["expired"]
	store.mu.Unlock()
	require.False(t, stillThere, "expired session is deleted on refusal")

	_, ok = resolve(context.Background(), "slid-out")
	require.False(t, ok, "absolute cap forces re-login")
}
