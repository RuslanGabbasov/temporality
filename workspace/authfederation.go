package workspace

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Auth federation, phase 1 (docs/auth-federation.md §5-§7): the IdP config,
// server-side sessions for the browser and the one-time OIDC request state.
// The kernel owns all of it; the broker/IdP side is pure configuration.

// AuthIdPConfig is the single IdP connection of the installation. ClientID
// and ClientSecretRef follow the provider-registry pattern: the secret lives
// in the secrets store (secret://… or an env var name) and resolves at the
// execution boundary, never in this row.
type AuthIdPConfig struct {
	Enabled         bool      `json:"enabled"`
	Issuer          string    `json:"issuer"`
	ClientID        string    `json:"client_id"`
	ClientSecretRef string    `json:"client_secret_ref"`
	DisplayName     string    `json:"display_name"`
	Scopes          string    `json:"scopes"`
	UsernameClaim   string    `json:"username_claim"`
	EmailClaim      string    `json:"email_claim"`
	GroupsClaim     string    `json:"groups_claim"`
	DefaultRole     string    `json:"default_role"` // reader | writer | operator | admin | deny
	JITEnabled      bool      `json:"jit_enabled"`
	RedirectBase    string    `json:"redirect_base"` // optional public origin override (https://stand.example.com)
	UpdatedAt       time.Time `json:"updated_at"`
}

// AuthSession is one browser session issued by the OIDC callback. The cookie
// carries only the random id; expiry, sliding renewal and revocation live
// here (docs/auth-federation.md §5.2).
type AuthSession struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	ExpiresAt time.Time `json:"expires_at"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
}

// AuthRequest is the one-time state/nonce/PKCE tuple of an in-flight
// authorization-code flow (docs/auth-federation.md §5.1). Consumed on read.
type AuthRequest struct {
	State       string
	Nonce       string
	Verifier    string
	RedirectURI string
	CreatedAt   time.Time
}

// GetAuthIdPConfig returns the saved config; a missing row is the zero
// config (SSO disabled), not an error.
func (s *Store) GetAuthIdPConfig(ctx context.Context) (AuthIdPConfig, error) {
	var cfg AuthIdPConfig
	err := s.pool.QueryRow(ctx,
		`SELECT enabled, issuer, client_id, client_secret_ref, display_name, scopes, username_claim, email_claim, groups_claim, default_role, jit_enabled, redirect_base, updated_at
		 FROM auth_idp_config WHERE id = 1`).
		Scan(&cfg.Enabled, &cfg.Issuer, &cfg.ClientID, &cfg.ClientSecretRef, &cfg.DisplayName, &cfg.Scopes,
			&cfg.UsernameClaim, &cfg.EmailClaim, &cfg.GroupsClaim, &cfg.DefaultRole, &cfg.JITEnabled, &cfg.RedirectBase, &cfg.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthIdPConfig{}, nil
	}
	if err != nil {
		return AuthIdPConfig{}, err
	}
	return cfg, nil
}

// SaveAuthIdPConfig upserts the single config row.
func (s *Store) SaveAuthIdPConfig(ctx context.Context, cfg AuthIdPConfig) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO auth_idp_config (id, enabled, issuer, client_id, client_secret_ref, display_name, scopes, username_claim, email_claim, groups_claim, default_role, jit_enabled, redirect_base, updated_at)
		 VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
		 ON CONFLICT (id) DO UPDATE SET enabled=$1, issuer=$2, client_id=$3, client_secret_ref=$4, display_name=$5, scopes=$6, username_claim=$7, email_claim=$8, groups_claim=$9, default_role=$10, jit_enabled=$11, redirect_base=$12, updated_at=now()`,
		cfg.Enabled, cfg.Issuer, cfg.ClientID, cfg.ClientSecretRef, cfg.DisplayName, cfg.Scopes,
		cfg.UsernameClaim, cfg.EmailClaim, cfg.GroupsClaim, cfg.DefaultRole, cfg.JITEnabled, cfg.RedirectBase)
	return err
}

func (s *Store) CreateAuthSession(ctx context.Context, sess *AuthSession) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO auth_session (id, user_id, created_at, last_seen, expires_at, ip, user_agent) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sess.ID, sess.UserID, sess.CreatedAt, sess.LastSeen, sess.ExpiresAt, sess.IP, sess.UserAgent)
	return err
}

// GetAuthSession returns the session row as stored; expiry checks belong to
// the caller (the gate resolver) so tests can pin the boundary semantics.
func (s *Store) GetAuthSession(ctx context.Context, id string) (AuthSession, error) {
	var sess AuthSession
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, created_at, last_seen, expires_at, ip, user_agent FROM auth_session WHERE id = $1`, id).
		Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.LastSeen, &sess.ExpiresAt, &sess.IP, &sess.UserAgent)
	if errors.Is(err, pgx.ErrNoRows) {
		return sess, ErrNotFound
	}
	if err != nil {
		return sess, err
	}
	return sess, nil
}

// TouchAuthSession slides the session forward: last_seen and the new expiry.
func (s *Store) TouchAuthSession(ctx context.Context, id string, seen, expires time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE auth_session SET last_seen=$2, expires_at=$3 WHERE id=$1`, id, seen, expires)
	return err
}

func (s *Store) DeleteAuthSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_session WHERE id=$1`, id)
	return err
}

// DeleteAuthSessionsForUser logs the user out everywhere (deactivation,
// password reset, explicit "logout all" — docs/auth-federation.md §11).
func (s *Store) DeleteAuthSessionsForUser(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_session WHERE user_id=$1`, userID)
	return err
}

// PurgeAuthGarbage removes expired sessions and stale in-flight requests;
// the hourly sweeper calls it (docs/auth-federation.md §5.2).
func (s *Store) PurgeAuthGarbage(ctx context.Context, now time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM auth_session WHERE expires_at < $1`, now); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM auth_request WHERE created_at < $1`, now.Add(-15*time.Minute))
	return err
}

func (s *Store) CreateAuthRequest(ctx context.Context, req *AuthRequest) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO auth_request (state, nonce, verifier, redirect_uri, created_at) VALUES ($1, $2, $3, $4, $5)`,
		req.State, req.Nonce, req.Verifier, req.RedirectURI, req.CreatedAt)
	return err
}

// TakeAuthRequest consumes the state: the row is deleted in the same breath,
// making every state single-use whatever the callback does next.
func (s *Store) TakeAuthRequest(ctx context.Context, state string) (AuthRequest, error) {
	var req AuthRequest
	err := s.pool.QueryRow(ctx,
		`DELETE FROM auth_request WHERE state=$1 RETURNING nonce, verifier, redirect_uri, created_at`, state).
		Scan(&req.Nonce, &req.Verifier, &req.RedirectURI, &req.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return req, ErrNotFound
	}
	if err != nil {
		return req, err
	}
	req.State = state
	return req, nil
}

// GetUserByExternalID resolves the JIT key (issuer, sub) to a user.
func (s *Store) GetUserByExternalID(ctx context.Context, issuer, externalID string) (User, error) {
	var u User
	var channels []byte
	var orgUnitID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, email, role, (token <> '') AS has_token, (password_hash <> '') AS has_password, org_unit_id, active, channels, preferred_channel, external_issuer, external_id, sourced, last_login_at, created_at, updated_at
		 FROM workspace_user WHERE external_issuer=$1 AND external_id=$2`, issuer, externalID).
		Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.HasToken, &u.HasPassword, &orgUnitID, &u.Active, &channels, &u.PreferredChannel, &u.ExternalIssuer, &u.ExternalID, &u.Sourced, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.OrgUnitID = derefPtr(orgUnitID)
	u.Channels = scanChannels(channels)
	return u, nil
}

// RecordUserLogin refreshes the IdP-owned identity fields (name/email come
// from the claims on every login) and stamps last_login_at.
func (s *Store) RecordUserLogin(ctx context.Context, id, name, email string, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workspace_user SET name=$2, email=$3, last_login_at=$4, updated_at=now() WHERE id=$1`,
		id, name, email, at)
	return err
}
