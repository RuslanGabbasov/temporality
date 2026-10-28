package workspace

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
)

// Secret is a first-class resource (docs/secrets-management.md §3.1): metadata
// only, never the value. Agents reference secrets by name (secret://name) or
// receive them as sandbox env vars; plaintext exists solely inside the kernel
// process at the execution boundary.
type Secret struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"` // builtin | env
	ExternalRef string `json:"external_ref,omitempty"`
	Description string `json:"description,omitempty"`
	// HasValue reports whether an encrypted value row exists (builtin
	// provider). It never exposes the value itself.
	HasValue bool `json:"has_value"`
	// Canary marks a honeypot credential (docs/agent-behavior-monitoring.md):
	// handed to agents like any other secret, but its value exists only to be
	// caught — any appearance of it anywhere in tool arguments, output or
	// artifact payloads is a confirmed exfiltration signal, zero false
	// positives by construction.
	Canary bool `json:"canary,omitempty"`
	// Enabled is the uniform on/off switch (the trigger pattern): a disabled
	// secret stops resolving to values — runs degrade without it, the stored
	// value and metadata stay untouched.
	Enabled bool `json:"enabled"`
	// Org binding (docs/org-structure.md §3.2): empty = whole installation.
	OrgUnitID string    `json:"org_unit_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SecretReference is the parsed form of secret://<name>[#key] used in MCP env
// and header values (docs/secrets-management.md §3.2).
type SecretReference struct {
	Name string
	Key  string // optional sub-key inside structured values (JSON)
}

// ParseSecretReference decodes "secret://name" and "secret://name#key". The
// name charset matches what administrators may configure (letters, digits,
// dash, underscore, dot); key allows the same plus a colon for nested paths.
func ParseSecretReference(value string) (SecretReference, bool) {
	const prefix = "secret://"
	if len(value) <= len(prefix) || value[:len(prefix)] != prefix {
		return SecretReference{}, false
	}
	rest := value[len(prefix):]
	key := ""
	for i := 0; i < len(rest); i++ {
		if rest[i] == '#' {
			key = rest[i+1:]
			rest = rest[:i]
			break
		}
	}
	if rest == "" || rest == "#" {
		return SecretReference{}, false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return SecretReference{}, false
		}
	}
	return SecretReference{Name: rest, Key: key}, true
}

// SealSecret encrypts plaintext with AES-256-GCM under a 32-byte master key.
// A fresh random nonce is returned alongside the ciphertext; both are stored
// in workspace_secret_value (envelope encryption, docs/secrets-management.md §6).
func SealSecret(masterKey, plaintext []byte) (ciphertext, nonce []byte, err error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, nil, fmt.Errorf("secret master key: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// OpenSecret decrypts a workspace_secret_value envelope. A wrong or rotated
// master key fails here with GCM's auth error, never with garbage output.
func OpenSecret(masterKey, ciphertext, nonce []byte) ([]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("secret master key: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func (s *Store) CreateSecret(ctx context.Context, secret *Secret) error {
	now := time.Now().UTC()
	secret.CreatedAt = now
	secret.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_secret (id, name, provider, external_ref, description, canary, org_unit_id, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		secret.ID, secret.Name, secret.Provider, secret.ExternalRef, secret.Description,
		secret.Canary, nullString(secret.OrgUnitID), secret.CreatedAt, secret.UpdatedAt)
	return err
}

func (s *Store) GetSecret(ctx context.Context, id string) (Secret, error) {
	var secret Secret
	var orgUnitID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, provider, external_ref, description, enabled, org_unit_id, created_at, updated_at,
		        EXISTS (SELECT 1 FROM workspace_secret_value v WHERE v.secret_id = workspace_secret.id)
		 FROM workspace_secret WHERE id = $1`, id).
		Scan(&secret.ID, &secret.Name, &secret.Provider, &secret.ExternalRef, &secret.Description,
			&secret.Enabled, &orgUnitID, &secret.CreatedAt, &secret.UpdatedAt, &secret.HasValue)
	if errors.Is(err, pgx.ErrNoRows) {
		return secret, ErrNotFound
	}
	if err != nil {
		return secret, err
	}
	secret.OrgUnitID = derefPtr(orgUnitID)
	return secret, nil
}

// GetSecretByName resolves the unique name used by secret:// references.
func (s *Store) GetSecretByName(ctx context.Context, name string) (Secret, error) {
	var secret Secret
	var orgUnitID *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, provider, external_ref, description, canary, enabled, org_unit_id, created_at, updated_at,
		        EXISTS (SELECT 1 FROM workspace_secret_value v WHERE v.secret_id = workspace_secret.id)
		 FROM workspace_secret WHERE name = $1`, name).
		Scan(&secret.ID, &secret.Name, &secret.Provider, &secret.ExternalRef, &secret.Description,
			&secret.Canary, &secret.Enabled, &orgUnitID, &secret.CreatedAt, &secret.UpdatedAt, &secret.HasValue)
	if errors.Is(err, pgx.ErrNoRows) {
		return secret, ErrNotFound
	}
	if err != nil {
		return secret, err
	}
	secret.OrgUnitID = derefPtr(orgUnitID)
	return secret, nil
}

func (s *Store) ListAllSecrets(ctx context.Context) ([]Secret, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, provider, external_ref, description, enabled, org_unit_id, created_at, updated_at,
		        EXISTS (SELECT 1 FROM workspace_secret_value v WHERE v.secret_id = workspace_secret.id)
		 FROM workspace_secret ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSecrets(rows)
}

// ListSecretsVisible returns secrets visible to a viewer whose org chain is
// units; nil units disables filtering (admin and service tokens).
func (s *Store) ListSecretsVisible(ctx context.Context, units []string) ([]Secret, error) {
	if units == nil {
		return s.ListAllSecrets(ctx)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, provider, external_ref, description, enabled, org_unit_id, created_at, updated_at,
		        EXISTS (SELECT 1 FROM workspace_secret_value v WHERE v.secret_id = workspace_secret.id)
		 FROM workspace_secret WHERE org_unit_id IS NULL OR org_unit_id = ANY($1) ORDER BY name`, units)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSecrets(rows)
}

// UpdateSecret updates metadata; the value changes only through SetSecretValue.
// Like the agent update path, the org binding is preserved: bindings move
// through the admin binding endpoint only.
func (s *Store) UpdateSecret(ctx context.Context, secret Secret) error {
	secret.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_secret
		 SET name=$2, provider=$3, external_ref=$4, description=$5, canary=$6, updated_at=$7
		 WHERE id=$1`,
		secret.ID, secret.Name, secret.Provider, secret.ExternalRef, secret.Description, secret.Canary, secret.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSecretEnabled flips the uniform on/off switch without touching the
// stored value: a disabled secret stops resolving — runs degrade without it,
// exactly like disabled skills.
func (s *Store) SetSecretEnabled(ctx context.Context, id string, enabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_secret SET enabled=$2, updated_at=now() WHERE id=$1`, id, enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSecret(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_secret WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSecretValue upserts the encrypted envelope for a secret.
func (s *Store) SetSecretValue(ctx context.Context, secretID string, ciphertext, nonce []byte) error {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_secret_value (secret_id, ciphertext, nonce, updated_at)
		 VALUES ($1,$2,$3,now())
		 ON CONFLICT (secret_id) DO UPDATE SET ciphertext=EXCLUDED.ciphertext, nonce=EXCLUDED.nonce, updated_at=now()`,
		secretID, ciphertext, nonce)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("secret value was not stored")
	}
	return nil
}

// GetSecretValue returns the encrypted envelope; decryption happens in the
// caller which owns the master key (the Store never sees plaintext keys).
func (s *Store) GetSecretValue(ctx context.Context, secretID string) (ciphertext, nonce []byte, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT ciphertext, nonce FROM workspace_secret_value WHERE secret_id = $1`, secretID).
		Scan(&ciphertext, &nonce)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	return ciphertext, nonce, err
}

func scanSecrets(rows pgx.Rows) ([]Secret, error) {
	var result []Secret
	for rows.Next() {
		var secret Secret
		var orgUnitID *string
		if err := rows.Scan(&secret.ID, &secret.Name, &secret.Provider, &secret.ExternalRef, &secret.Description,
			&secret.Enabled, &orgUnitID, &secret.CreatedAt, &secret.UpdatedAt, &secret.HasValue); err != nil {
			return nil, err
		}
		secret.OrgUnitID = derefPtr(orgUnitID)
		result = append(result, secret)
	}
	return result, rows.Err()
}
