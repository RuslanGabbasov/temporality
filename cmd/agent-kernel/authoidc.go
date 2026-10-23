package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/workspace"
	"golang.org/x/oauth2"
)

// OIDC login, sessions and JIT provisioning — auth federation phase 1
// (docs/auth-federation.md §5-§6). Everything the browser needs is mounted
// OUTSIDE the bearer gate (see the root handler in main.go); the workspace
// config endpoints live behind the regular admin gate like every other
// workspace resource.
//
// The kernel speaks exactly one federation protocol (OIDC). LDAP, SAML and
// multi-IdP zoos are the broker's problem (§3.2) — here there is one issuer,
// one client, one redirect.

// authStore is the storage surface of the OIDC flow: config, sessions,
// one-time request state, the JIT user lookup and the audit sink. The real
// *workspace.Store satisfies it; tests fake it.
type authStore interface {
	GetAuthIdPConfig(ctx context.Context) (workspace.AuthIdPConfig, error)
	SaveAuthIdPConfig(ctx context.Context, cfg workspace.AuthIdPConfig) error
	CreateAuthSession(ctx context.Context, sess *workspace.AuthSession) error
	GetAuthSession(ctx context.Context, id string) (workspace.AuthSession, error)
	TouchAuthSession(ctx context.Context, id string, seen, expires time.Time) error
	DeleteAuthSession(ctx context.Context, id string) error
	CreateAuthRequest(ctx context.Context, req *workspace.AuthRequest) error
	TakeAuthRequest(ctx context.Context, state string) (workspace.AuthRequest, error)
	GetUserByExternalID(ctx context.Context, issuer, externalID string) (workspace.User, error)
	CreateUser(ctx context.Context, u *workspace.User) error
	RecordUserLogin(ctx context.Context, id, name, email string, at time.Time) error
	RecordAccessAudit(ctx context.Context, actor, action, entityKind, entityID string, details map[string]any) error
}

// authDependencies carries the wiring the handlers need beyond the store.
type authDependencies struct {
	store        authStore
	resolveKey   func(ctx context.Context, ref string) (string, error)
	log          *slog.Logger
	sessionTTL   time.Duration // sliding access TTL (default 12h)
	absoluteTTL  time.Duration // absolute cap, forces re-login (default 7d)
	cookieSecure bool
}

// oidcProviderCache memoizes discovery per issuer URL. Rebuilding on every
// request would add a discovery round-trip to each login; verifiers are keyed
// by (issuer, client_id) so a client-id rotation picks up a fresh verifier
// without rediscovering, and reset() drops everything on config save.
type oidcProviderCache struct {
	mu        sync.Mutex
	issuers   map[string]*oidc.Provider
	verifiers map[string]*oidc.IDTokenVerifier
}

func newOIDCProviderCache() *oidcProviderCache {
	return &oidcProviderCache{issuers: map[string]*oidc.Provider{}, verifiers: map[string]*oidc.IDTokenVerifier{}}
}

func (c *oidcProviderCache) get(ctx context.Context, issuer, clientID string) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.issuers[issuer]
	if !ok {
		var err error
		p, err = oidc.NewProvider(ctx, issuer)
		if err != nil {
			return nil, nil, fmt.Errorf("discover issuer %s: %w", issuer, err)
		}
		c.issuers[issuer] = p
	}
	vKey := issuer + "\x00" + clientID
	v, ok := c.verifiers[vKey]
	if !ok {
		v = p.Verifier(&oidc.Config{ClientID: clientID})
		c.verifiers[vKey] = v
	}
	return p, v, nil
}

// reset drops all cached discovery data — called after a config save, when
// the issuer or its client may have changed underneath the cache.
func (c *oidcProviderCache) reset() {
	c.mu.Lock()
	c.issuers = map[string]*oidc.Provider{}
	c.verifiers = map[string]*oidc.IDTokenVerifier{}
	c.mu.Unlock()
}

// publicOrigin derives the browser-facing origin of this request: proxy
// headers first (nginx passes Host and X-Forwarded-Proto), the config's
// redirect_base overrides everything for exotic deployments.
func publicOrigin(r *http.Request, cfg workspace.AuthIdPConfig) string {
	if base := strings.TrimSpace(cfg.RedirectBase); base != "" {
		return strings.TrimRight(base, "/")
	}
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}

// publicAPIPrefix returns the proxy prefix the browser addresses this kernel
// through ("/kernel-api" behind nginx; "" for direct access). nginx forwards
// the original URI untouched and the kernel strips the prefix itself — the
// strip helper records it in X-Forwarded-Prefix precisely for derivations
// like the OIDC redirect_uri.
func publicAPIPrefix(r *http.Request) string {
	if prefix := r.Header.Get("X-Forwarded-Prefix"); prefix != "" {
		return strings.TrimRight(prefix, "/")
	}
	return ""
}

// validateAuthIdPConfig rejects malformed configs before they are saved: a
// broken issuer would only surface at the next login, far from the change.
func validateAuthIdPConfig(cfg workspace.AuthIdPConfig) (workspace.AuthIdPConfig, error) {
	if cfg.DisplayName == "" {
		cfg.DisplayName = "SSO"
	}
	if cfg.Scopes == "" {
		cfg.Scopes = "openid profile email"
	}
	if cfg.UsernameClaim == "" {
		cfg.UsernameClaim = "preferred_username"
	}
	if cfg.EmailClaim == "" {
		cfg.EmailClaim = "email"
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if cfg.DefaultRole == "" {
		cfg.DefaultRole = "reader"
	}
	if !cfg.Enabled {
		return cfg, nil
	}
	issuer, err := url.Parse(cfg.Issuer)
	if err != nil || !issuer.IsAbs() || issuer.Host == "" {
		return cfg, errors.New("issuer must be an absolute URL (https://idp.example.com/realms/x)")
	}
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	if cfg.ClientID == "" {
		return cfg, errors.New("client_id is required when SSO is enabled")
	}
	if cfg.ClientSecretRef == "" {
		return cfg, errors.New("client_secret_ref is required when SSO is enabled (secret://… or an env var name)")
	}
	switch cfg.DefaultRole {
	case "reader", "writer", "operator", "admin", "deny":
	default:
		return cfg, errors.New("default_role must be one of reader, writer, operator, admin or deny")
	}
	return cfg, nil
}

// oauthClient assembles the OAuth2 client for the flow.
func oauthClient(cfg workspace.AuthIdPConfig, provider *oidc.Provider, secret string) oauth2.Config {
	return oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: secret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  "", // set by the caller per request
		Scopes:       strings.Fields(cfg.Scopes),
	}
}

// registerAuthRoutes mounts the public auth surface. The routes themselves
// are exempted from the bearer gate at the root handler (main.go); callers
// of the config endpoints go through gate.Allow(RoleAdmin).
func registerAuthRoutes(mux *http.ServeMux, deps authDependencies, gate *controlplane.Gate, providers *oidcProviderCache) {
	// GET /v1/auth/methods — what the login screen may offer. Public by
	// design: the whole point is to render the SSO button before any
	// credential exists (docs/auth-federation.md §9).
	mux.HandleFunc("GET /v1/auth/methods", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := deps.store.GetAuthIdPConfig(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{
			"sso": map[string]any{
				"enabled":      cfg.Enabled && cfg.Issuer != "",
				"display_name": cfg.DisplayName,
			},
			"local": true,
		})
	})

	// GET /v1/auth/oidc/start — 302 to the IdP authorization endpoint with
	// fresh state/nonce/PKCE. One-time values are stored server-side; the
	// cookie jar needs nothing.
	mux.HandleFunc("GET /v1/auth/oidc/start", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := deps.store.GetAuthIdPConfig(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !cfg.Enabled || cfg.Issuer == "" {
			http.Error(w, "sso is not configured", http.StatusServiceUnavailable)
			return
		}
		secret, err := deps.resolveKey(r.Context(), cfg.ClientSecretRef)
		if err != nil {
			writeError(w, 500, fmt.Errorf("resolve client secret: %w", err))
			return
		}
		provider, _, err := providers.get(r.Context(), cfg.Issuer, cfg.ClientID)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		state := generateToken()
		nonce := generateToken()
		verifier := oauth2.GenerateVerifier()
		redirectURI := publicOrigin(r, cfg) + publicAPIPrefix(r) + "/v1/auth/oidc/callback"
		if err := deps.store.CreateAuthRequest(r.Context(), &workspace.AuthRequest{
			State: state, Nonce: nonce, Verifier: verifier,
			RedirectURI: redirectURI, CreatedAt: time.Now().UTC(),
		}); err != nil {
			writeError(w, 500, err)
			return
		}
		client := oauthClient(cfg, provider, secret)
		client.RedirectURL = redirectURI
		http.Redirect(w, r, client.AuthCodeURL(state,
			oauth2.SetAuthURLParam("nonce", nonce),
			oauth2.S256ChallengeOption(verifier),
		), http.StatusFound)
	})

	// GET /v1/auth/oidc/callback — the IdP lands the browser here. Exchanges
	// the code, verifies the id_token (issuer, audience, nonce, signature via
	// JWKS), runs JIT and issues the session cookie.
	mux.HandleFunc("GET /v1/auth/oidc/callback", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := deps.store.GetAuthIdPConfig(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if !cfg.Enabled || cfg.Issuer == "" {
			http.Error(w, "sso is not configured", http.StatusServiceUnavailable)
			return
		}
		audit := func(action string, details map[string]any) {
			_ = deps.store.RecordAccessAudit(r.Context(), "oidc", action, "auth", cfg.Issuer, details)
		}
		fail := func(status int, public string) {
			audit(workspace.AuditAuthLoginFailed, map[string]any{"reason": public, "detail": true})
			deps.log.Warn("oidc login failed", "issuer", cfg.Issuer, "reason", public)
			http.Error(w, public, status)
		}
		if callbackErr := r.URL.Query().Get("error"); callbackErr != "" {
			fail(http.StatusBadGateway, "identity provider refused: "+callbackErr)
			return
		}
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		if code == "" || state == "" {
			fail(http.StatusBadRequest, "missing code or state")
			return
		}
		req, err := deps.store.TakeAuthRequest(r.Context(), state)
		if err != nil {
			// Single-use state: replay or garbage — indistinguishable by design.
			fail(http.StatusBadRequest, "unknown or expired login state")
			return
		}
		if time.Since(req.CreatedAt) > 15*time.Minute {
			fail(http.StatusBadRequest, "login state expired")
			return
		}
		secret, err := deps.resolveKey(r.Context(), cfg.ClientSecretRef)
		if err != nil {
			fail(http.StatusInternalServerError, "resolve client secret failed")
			return
		}
		provider, verifier, err := providers.get(r.Context(), cfg.Issuer, cfg.ClientID)
		if err != nil {
			fail(http.StatusBadGateway, "identity provider discovery failed")
			return
		}
		client := oauthClient(cfg, provider, secret)
		client.RedirectURL = req.RedirectURI
		tokens, err := client.Exchange(r.Context(), code, oauth2.SetAuthURLParam("code_verifier", req.Verifier))
		if err != nil {
			fail(http.StatusBadGateway, "code exchange failed")
			return
		}
		rawIDToken := tokens.Extra("id_token")
		raw, _ := rawIDToken.(string)
		if raw == "" {
			fail(http.StatusBadGateway, "identity provider returned no id_token")
			return
		}
		idToken, err := verifier.Verify(r.Context(), raw)
		if err != nil {
			fail(http.StatusUnauthorized, "id_token verification failed")
			return
		}
		// Claims are read by the CONFIGURED names (docs/auth-federation.md §6.2):
		// Keycloak ships preferred_username, Authentik custom paths, ADFS upn —
		// decode the payload once and pick fields per installation.
		var rawClaims map[string]any
		if err := idToken.Claims(&rawClaims); err != nil {
			fail(http.StatusUnauthorized, "id_token claims unreadable")
			return
		}
		claimString := func(name string) string {
			if v, ok := rawClaims[name]; ok {
				switch s := v.(type) {
				case string:
					return s
				case []any:
					if len(s) > 0 {
						if first, ok := s[0].(string); ok {
							return first
						}
					}
				}
			}
			return ""
		}
		sub, _ := rawClaims["sub"].(string)
		nonce, _ := rawClaims["nonce"].(string)
		username := claimString(cfg.UsernameClaim)
		if username == "" { // a reasonable fallback for exotic claim layouts
			username = claimString("preferred_username")
		}
		email := claimString(cfg.EmailClaim)
		if email == "" {
			email = claimString("email")
		}
		if nonce != req.Nonce {
			fail(http.StatusUnauthorized, "nonce mismatch")
			return
		}
		if sub == "" {
			fail(http.StatusUnauthorized, "id_token has no subject")
			return
		}
		user, err := jitProvision(r.Context(), deps, cfg, sub, username, email)
		if err != nil {
			fail(http.StatusForbidden, err.Error())
			return
		}
		if !user.Active {
			fail(http.StatusForbidden, "account is deactivated")
			return
		}
		// Session issue (§5.2): random 512-bit id, httpOnly cookie, sliding TTL
		// with an absolute cap enforced by the resolver.
		now := time.Now().UTC()
		expires := now.Add(deps.sessionTTL)
		sess := workspace.AuthSession{
			ID:        generateToken() + generateToken(), // 512-bit
			UserID:    user.ID,
			CreatedAt: now, LastSeen: now, ExpiresAt: expires,
			IP:        clientIP(r),
			UserAgent: r.UserAgent(),
		}
		if err := deps.store.CreateAuthSession(r.Context(), &sess); err != nil {
			fail(http.StatusInternalServerError, "create session failed")
			return
		}
		if err := deps.store.RecordUserLogin(r.Context(), user.ID, user.Name, user.Email, now); err != nil {
			deps.log.Warn("record login facts", "user", user.ID, "error", err)
		}
		audit(workspace.AuditAuthLoginSucceeded, map[string]any{
			"user_id": user.ID, "subject": user.Name, "method": "sso", "issuer": cfg.Issuer,
		})
		http.SetCookie(w, &http.Cookie{
			Name:     controlplane.SessionCookie,
			Value:    sess.ID,
			Path:     "/",
			HttpOnly: true,
			Secure:   deps.cookieSecure,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, publicOrigin(r, cfg)+"/", http.StatusFound)
	})

	// POST /v1/auth/logout — drops the session row and the cookie. Works
	// unauthenticated too (clearing a stale cookie is always safe).
	mux.HandleFunc("POST /v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(controlplane.SessionCookie); err == nil && cookie.Value != "" {
			sess, err := deps.store.GetAuthSession(r.Context(), cookie.Value)
			if err == nil {
				_ = deps.store.RecordAccessAudit(r.Context(), sess.UserID, workspace.AuditAuthLogout, "auth", sess.UserID, nil)
			}
			_ = deps.store.DeleteAuthSession(r.Context(), cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name: controlplane.SessionCookie, Value: "", Path: "/",
			MaxAge: -1, HttpOnly: true, Secure: deps.cookieSecure, SameSite: http.SameSiteLaxMode,
		})
		w.WriteHeader(http.StatusNoContent)
	})

	// Workspace config endpoints — admin-gated like the rest of the
	// workspace surface (docs/auth-federation.md §8).
	mux.HandleFunc("GET /v1/workspace/auth/config", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		cfg, err := deps.store.GetAuthIdPConfig(r.Context())
		if err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, cfg)
	})
	mux.HandleFunc("PUT /v1/workspace/auth/config", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var cfg workspace.AuthIdPConfig
		if err := decodeJSONBody(w, r, &cfg); err != nil {
			writeError(w, 400, err)
			return
		}
		cfg, err := validateAuthIdPConfig(cfg)
		if err != nil {
			writeError(w, 422, err)
			return
		}
		if err := deps.store.SaveAuthIdPConfig(r.Context(), cfg); err != nil {
			writeError(w, 500, err)
			return
		}
		// A saved config may change the issuer or client id — cached discovery
		// and verifiers are stale from this moment, so drop them.
		providers.reset()
		_ = deps.store.RecordAccessAudit(r.Context(), requestUserID(r), workspace.AuditAuthConfigUpdated, "auth", cfg.Issuer, map[string]any{
			"enabled": cfg.Enabled, "default_role": cfg.DefaultRole, "jit": cfg.JITEnabled,
		})
		writeJSON(w, 200, cfg)
	})
	// POST /v1/workspace/auth/config/test — discovery + JWKS reachability
	// probe without saving anything (docs/auth-federation.md §8).
	mux.HandleFunc("POST /v1/workspace/auth/config/test", func(w http.ResponseWriter, r *http.Request) {
		if !gate.Allow(w, r, controlplane.RoleAdmin) {
			return
		}
		var cfg workspace.AuthIdPConfig
		if err := decodeJSONBody(w, r, &cfg); err != nil {
			writeError(w, 400, err)
			return
		}
		if _, err := url.Parse(cfg.Issuer); err != nil || cfg.Issuer == "" {
			writeError(w, 422, errors.New("issuer is required"))
			return
		}
		issuer := strings.TrimRight(cfg.Issuer, "/")
		if _, _, err := providers.get(r.Context(), issuer, cfg.ClientID); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
}

// loginStore extends authStore with the methods the password paths need —
// kept separate so the fake in tests covers one surface.
type loginStore interface {
	authStore
	GetUser(ctx context.Context, id string) (workspace.User, error)
	GetUserByLogin(ctx context.Context, login string) (workspace.User, error)
	UpdateUserToken(ctx context.Context, id, token string) error
	SetUserPassword(ctx context.Context, id, passwordHash string) error
}

// registerPasswordLogin mounts POST /v1/auth/login — local credentials for
// the admin break-glass path and tokenless installs (docs/auth-federation.md
// §5.5). Extracted from main so tests can drive the break-glass verdict.
func registerPasswordLogin(mux *http.ServeMux, deps authDependencies, tokens loginStore, refreshTokens func()) {
	mux.HandleFunc("POST /v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Login    string `json:"login"`
			Password string `json:"password"`
		}
		if err := decodeJSONBody(w, r, &req); err != nil {
			writeError(w, 400, err)
			return
		}
		login := strings.TrimSpace(req.Login)
		if login == "" || req.Password == "" {
			writeError(w, 401, errors.New("invalid login or password"))
			return
		}
		user, err := tokens.GetUserByLogin(r.Context(), login)
		if err != nil {
			writeError(w, 401, errors.New("invalid login or password"))
			return
		}
		if !workspace.CheckPassword(user.PasswordHash, req.Password) {
			writeError(w, 401, errors.New("invalid login or password"))
			return
		}
		// Break-glass (docs/auth-federation.md §5.5): once SSO is live, local
		// passwords are the admin's emergency path only. Everyone else gets the
		// same generic 401 — the restriction is invisible from the outside. A
		// broken config row fails open rather than locking out password logins.
		if ssoCfg, cfgErr := deps.store.GetAuthIdPConfig(r.Context()); cfgErr != nil {
			deps.log.Warn("read idp config for login break-glass", "error", cfgErr)
		} else if ssoCfg.Enabled && user.Role != "admin" {
			writeError(w, 401, errors.New("invalid login or password"))
			return
		}
		token := user.Token
		if token == "" {
			// Password-capable users always leave with a bearer token: mint one
			// on first login so the gate accepts subsequent requests.
			token = generateToken()
			if err := tokens.UpdateUserToken(r.Context(), user.ID, token); err != nil {
				writeError(w, 500, err)
				return
			}
			if refreshTokens != nil {
				refreshTokens()
			}
		}
		writeJSON(w, 200, map[string]any{"token": token, "user_id": user.ID, "subject": user.Name})
	})

	// POST /v1/auth/password — a signed-in user changes their OWN password:
	// the current one is re-checked (a stolen session cannot lock the owner
	// out), the new one meets the same 8-character bar the admin path
	// enforces, and the bearer token survives — a password change is not a
	// logout. SSO users without a local password get a 409: setting the first
	// password is the admin's decision (docs/auth-federation.md §5.5 — local
	// credentials are the break-glass path, not a second identity).
	mux.HandleFunc("POST /v1/auth/password", func(w http.ResponseWriter, r *http.Request) {
		principal, ok := controlplane.FromContext(r.Context())
		if !ok || principal.UserID == "" {
			writeError(w, 401, errors.New("authentication required"))
			return
		}
		var req struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := decodeJSONBody(w, r, &req); err != nil {
			writeError(w, 400, err)
			return
		}
		user, err := tokens.GetUser(r.Context(), principal.UserID)
		if err != nil {
			writeError(w, 401, errors.New("authentication required"))
			return
		}
		if user.PasswordHash == "" {
			writeError(w, 409, errors.New("this account has no local password; ask an administrator to set one first"))
			return
		}
		if !workspace.CheckPassword(user.PasswordHash, req.CurrentPassword) {
			writeError(w, 401, errors.New("current password is incorrect"))
			return
		}
		if len(req.NewPassword) < 8 {
			writeError(w, 422, errors.New("password must be at least 8 characters"))
			return
		}
		hash, err := workspace.HashPassword(req.NewPassword)
		if err != nil {
			writeError(w, 500, err)
			return
		}
		if err := tokens.SetUserPassword(r.Context(), user.ID, hash); err != nil {
			writeError(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
}

// decodeJSONBody is a size-capped JSON decode shared by the config routes.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, into any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(into)
}

// jitProvision implements docs/auth-federation.md §6.1: find by
// (issuer, sub); create with the default role when JIT is on; refuse new
// unknown users with an actionable message otherwise. The deny role is the
// "login works, access does not" policy (§5.5) — surfaced as a login refusal
// with a clear reason, which is the same UX without RoleNone plumbing.
func jitProvision(ctx context.Context, deps authDependencies, cfg workspace.AuthIdPConfig, sub, username, email string) (workspace.User, error) {
	user, err := deps.store.GetUserByExternalID(ctx, cfg.Issuer, sub)
	now := time.Now().UTC()
	if errors.Is(err, workspace.ErrNotFound) {
		if !cfg.JITEnabled {
			return user, errors.New("no account for this identity and auto-provisioning is disabled; ask an administrator")
		}
		if cfg.DefaultRole == "deny" {
			return user, errors.New("no access policy for this account; ask an administrator")
		}
		name := username
		if name == "" {
			name = email
		}
		if name == "" {
			name = sub
		}
		user = workspace.User{
			ID:   "user-" + generateToken()[:16],
			Name: name, Email: email,
			Role:           cfg.DefaultRole,
			Active:         true,
			ExternalIssuer: cfg.Issuer,
			ExternalID:     sub,
			Sourced:        "federated",
			LastLoginAt:    &now,
		}
		if err := deps.store.CreateUser(ctx, &user); err != nil {
			return user, fmt.Errorf("provision account: %w", err)
		}
		_ = deps.store.RecordAccessAudit(ctx, "oidc", workspace.AuditAuthUserJITCreated, "user", user.ID, map[string]any{
			"issuer": cfg.Issuer, "subject": sub, "role": user.Role,
		})
		return user, nil
	}
	if err != nil {
		return user, err
	}
	// Known identity: IdP-owned fields follow the claims (§6.1).
	if (username != "" && username != user.Name) || (email != "" && email != user.Email) {
		if username != "" {
			user.Name = username
		}
		if email != "" {
			user.Email = email
		}
		if err := deps.store.RecordUserLogin(ctx, user.ID, user.Name, user.Email, now); err != nil {
			deps.log.Warn("sync identity fields", "user", user.ID, "error", err)
		}
	}
	return user, nil
}

// sessionPrincipalResolver wires the gate to the session store: expiry and
// sliding renewal happen here, controlplane only consumes the verdict
// (§5.3). Broken or expired sessions answer false — indistinguishable from
// missing.
func sessionPrincipalResolver(store authStore, principalFor func(ctx context.Context, userID string) (controlplane.Principal, bool), log *slog.Logger, ttl, absoluteTTL time.Duration) controlplane.SessionResolver {
	return func(ctx context.Context, sessionID string) (controlplane.Principal, bool) {
		sess, err := store.GetAuthSession(ctx, sessionID)
		if err != nil {
			return controlplane.Principal{}, false
		}
		now := time.Now().UTC()
		if now.After(sess.ExpiresAt) || now.Sub(sess.CreatedAt) > absoluteTTL {
			_ = store.DeleteAuthSession(ctx, sessionID)
			return controlplane.Principal{}, false
		}
		principal, ok := principalFor(ctx, sess.UserID)
		if !ok {
			// Deactivated or deleted user: sessions die with the account.
			_ = store.DeleteAuthSession(ctx, sessionID)
			return controlplane.Principal{}, false
		}
		_ = store.TouchAuthSession(ctx, sessionID, now, now.Add(ttl))
		return principal, true
	}
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		return strings.TrimSpace(parts[0])
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	return host
}
