package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// KernelServiceUserName is the workspace service principal the kernel uses for
// its own journal calls when no TEMPORALITY_API_TOKEN is configured (or the
// configured one is rejected by the journal). Both services share this
// database and the journal reloads user tokens on a short interval, so a row
// here authorizes the kernel on the journal without any static configuration.
// writer role, org-unassigned: the kernel ingests events and reads
// hints/listings across every project.
const KernelServiceUserName = "temporality-kernel"

// EnsureKernelServiceToken returns the service principal's bearer token,
// creating the row (and minting a token) on first use. The token is stable
// across restarts; deactivating the user is a deliberate admin action and
// fails boot with this error until reactivated or TEMPORALITY_API_TOKEN is
// configured.
func (s *Store) EnsureKernelServiceToken(ctx context.Context) (string, error) {
	var id, token string
	var active bool
	err := s.pool.QueryRow(ctx,
		`SELECT id, token, active FROM workspace_user WHERE name=$1 ORDER BY created_at LIMIT 1`,
		KernelServiceUserName).Scan(&id, &token, &active)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		token = newServiceToken()
		user := &User{ID: "usr-temporality-kernel", Name: KernelServiceUserName, Role: "writer", Token: token, Active: true}
		if err := s.CreateUser(ctx, user); err != nil {
			return "", fmt.Errorf("create kernel service user: %w", err)
		}
		return token, nil
	case err != nil:
		return "", err
	case !active:
		return "", fmt.Errorf("kernel service user %q is deactivated: reactivate it or set TEMPORALITY_API_TOKEN", KernelServiceUserName)
	case token == "":
		token = newServiceToken()
		if err := s.UpdateUserToken(ctx, id, token); err != nil {
			return "", fmt.Errorf("mint kernel service token: %w", err)
		}
		return token, nil
	}
	return token, nil
}

func newServiceToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
