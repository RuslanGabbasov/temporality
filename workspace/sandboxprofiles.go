package workspace

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sandbox profile registry (docs/sandbox-profiles.md). A profile is the unit
// of "what toolchain does this agent's sandbox run": an image reference plus
// resource overrides and cache env pointers. Isolation flags never vary by
// profile — the container hardening is identical for everyone.

// SandboxProfile is one registry row. Builtin rows are seeded from the
// in-code catalog; custom rows are created through the API with an external
// digest-pinned image.
type SandboxProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Image is the digest-pinned reference (repo@sha256:…). Empty means the
	// profile is not built yet — run_command under it fails with an explicit
	// error instead of silently substituting another image (§4.3).
	Image      string   `json:"image,omitempty"`
	Toolchains []string `json:"toolchains,omitempty"`
	// Resource overrides; empty = the runner's installation defaults.
	Memory      string `json:"memory,omitempty"`
	CPUs        string `json:"cpus,omitempty"`
	ScratchSize string `json:"scratch_size,omitempty"`
	TmpSize     string `json:"tmp_size,omitempty"`
	// CacheEnv entries are KEY=VALUE pairs pointing toolchain caches at /cache
	// (the persistent per-project cache mount). Values outside /cache are
	// rejected at save time (§12).
	CacheEnv  []string  `json:"cache_env,omitempty"`
	Builtin   bool      `json:"builtin"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ErrProfileBuiltin reports an attempt to delete or rewrite a builtin profile.
var ErrProfileBuiltin = errors.New("sandbox profile is builtin")

// ErrProfileInUse reports a delete blocked by agents still assigned to the
// profile: removing it would strand their runs on a missing image.
var ErrProfileInUse = errors.New("sandbox profile is assigned to agents")

var sandboxProfileIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// BuiltinSandboxProfiles is the in-code catalog (§5). Images stay empty until
// an administrator builds them through POST …/{id}/build, which pins the
// digest into the row.
func BuiltinSandboxProfiles() []SandboxProfile {
	return []SandboxProfile{
		{
			ID:          "node",
			Name:        "Node 22",
			Description: "Node.js 22 с git, curl, jq, bash и ripgrep — фронтенд-сборки, npm-скрипты, JS-инструменты.",
			Toolchains:  []string{"node"},
			CacheEnv:    []string{"NPM_CONFIG_CACHE=/cache/npm"},
			Builtin:     true,
		},
		{
			ID:          "python",
			Name:        "Python 3.12",
			Description: "Python 3.12 с git, curl, jq, bash и ripgrep — скрипты, тесты, data-обработка.",
			Toolchains:  []string{"python"},
			CacheEnv:    []string{"PIP_CACHE_DIR=/cache/pip", "UV_CACHE_DIR=/cache/uv"},
			Builtin:     true,
		},
		{
			ID:          "browser-e2e",
			Name:        "Browser E2E (Playwright)",
			Description: "Node 22 + Playwright с предустановленным Chromium — браузерные E2E-тесты и скриншоты.",
			Toolchains:  []string{"node", "browser"},
			// Chromium is memory-hungry: the browser profile raises the
			// container budget instead of every agent paying for it.
			Memory:      "2g",
			ScratchSize: "1g",
			TmpSize:     "512m",
			CacheEnv:    []string{"NPM_CONFIG_CACHE=/cache/npm", "PLAYWRIGHT_BROWSERS_PATH=/cache/ms-playwright"},
			Builtin:     true,
		},
	}
}

// CacheSubdirs extracts the /cache subdirectories referenced by cache env
// entries so the runner can pre-create them: "NPM_CONFIG_CACHE=/cache/npm"
// yields "npm". Duplicates collapse; entries not under /cache are skipped
// (validation rejects them earlier, this is the defensive read side).
func CacheSubdirs(cacheEnv []string) []string {
	seen := make(map[string]bool)
	var dirs []string
	for _, entry := range cacheEnv {
		value := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			value = entry[idx+1:]
		}
		value = strings.TrimSuffix(strings.TrimPrefix(value, "/cache/"), "/")
		if value == "" || strings.Contains(value, "/") || seen[value] {
			continue
		}
		seen[value] = true
		dirs = append(dirs, value)
	}
	return dirs
}

// ValidateSandboxProfile checks a profile before save: id shape, required
// name, digest-pinned image, byte-size resource fields and KEY=VALUE cache
// entries pointing under /cache.
func ValidateSandboxProfile(p SandboxProfile) error {
	if !sandboxProfileIDPattern.MatchString(p.ID) {
		return fmt.Errorf("profile id must match %s", sandboxProfileIDPattern.String())
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("profile name is required")
	}
	if p.Image != "" && !IsDigestPinnedImage(p.Image) {
		return fmt.Errorf("image %q must be pinned by a sha256 digest (repo@sha256:…)", p.Image)
	}
	for _, size := range []struct{ field, value string }{
		{"memory", p.Memory}, {"cpus", p.CPUs}, {"scratch_size", p.ScratchSize}, {"tmp_size", p.TmpSize},
	} {
		if size.value != "" && !isValidResourceValue(size.field, size.value) {
			return fmt.Errorf("%s %q is not a valid resource value", size.field, size.value)
		}
	}
	for _, entry := range p.CacheEnv {
		idx := strings.IndexByte(entry, '=')
		if idx <= 0 {
			return fmt.Errorf("cache_env entry %q must be KEY=VALUE", entry)
		}
		value := entry[idx+1:]
		if !strings.HasPrefix(value, "/cache/") || strings.HasSuffix(value, "/") {
			return fmt.Errorf("cache_env %q must point inside /cache/ (e.g. /cache/npm)", entry)
		}
	}
	return nil
}

// IsDigestPinnedImage reports whether ref is repo@sha256:<64 hex>. It mirrors
// kernel/sandbox's validateImage so both sides reject mutable tags without
// importing each other.
func IsDigestPinnedImage(ref string) bool {
	marker := strings.LastIndex(ref, "@sha256:")
	if marker <= 0 || len(ref)-marker-len("@sha256:") != 64 {
		return false
	}
	for _, c := range ref[marker+len("@sha256:"):] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// isValidResourceValue accepts cpus as a decimal fraction and sizes as bytes
// or a k/m/g unit — the shapes docker accepts for --cpus/--memory/tmpfs size.
func isValidResourceValue(field, value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if field == "cpus" {
		return regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`).MatchString(trimmed)
	}
	digits := strings.TrimRight(trimmed, "bBkKmMgG")
	return digits != "" && strings.Trim(digits, "0123456789") == ""
}

const sandboxProfileColumns = `id, name, description, image, toolchains, memory, cpus, scratch_size, tmp_size, cache_env, builtin, created_at, updated_at`

func scanSandboxProfile(row interface{ Scan(dest ...any) error }) (SandboxProfile, error) {
	var p SandboxProfile
	if err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Image, &p.Toolchains,
		&p.Memory, &p.CPUs, &p.ScratchSize, &p.TmpSize, &p.CacheEnv, &p.Builtin, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, ErrNotFound
		}
		return p, err
	}
	return p, nil
}

// SeedSandboxProfiles inserts the builtin catalog rows that are missing.
// ON CONFLICT DO NOTHING keeps an admin-built image and any local tweaks —
// seeding never overwrites.
func (s *Store) SeedSandboxProfiles(ctx context.Context) error {
	for _, p := range BuiltinSandboxProfiles() {
		now := time.Now().UTC()
		_, err := s.pool.Exec(ctx,
			`INSERT INTO workspace_sandbox_profile (id, name, description, image, toolchains, memory, cpus, scratch_size, tmp_size, cache_env, builtin, created_at, updated_at)
			 VALUES ($1,$2,$3,'',$4,$5,$6,$7,$8,$9,TRUE,$10,$10)
			 ON CONFLICT (id) DO NOTHING`,
			p.ID, p.Name, p.Description, p.Toolchains, p.Memory, p.CPUs, p.ScratchSize, p.TmpSize, p.CacheEnv, now)
		if err != nil {
			return fmt.Errorf("seed sandbox profile %s: %w", p.ID, err)
		}
	}
	return nil
}

func (s *Store) ListSandboxProfiles(ctx context.Context) ([]SandboxProfile, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+sandboxProfileColumns+` FROM workspace_sandbox_profile ORDER BY builtin DESC, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SandboxProfile
	for rows.Next() {
		p, err := scanSandboxProfile(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (s *Store) GetSandboxProfile(ctx context.Context, id string) (SandboxProfile, error) {
	return scanSandboxProfile(s.pool.QueryRow(ctx,
		`SELECT `+sandboxProfileColumns+` FROM workspace_sandbox_profile WHERE id = $1`, id))
}

// CreateSandboxProfile stores a custom profile. Builtin ids are reserved by
// the catalog, so a custom row colliding with one is rejected up front.
func (s *Store) CreateSandboxProfile(ctx context.Context, p *SandboxProfile) error {
	for _, builtin := range BuiltinSandboxProfiles() {
		if p.ID == builtin.ID {
			return fmt.Errorf("%w: %s", ErrProfileBuiltin, p.ID)
		}
	}
	if err := ValidateSandboxProfile(*p); err != nil {
		return err
	}
	p.Builtin = false
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_sandbox_profile (id, name, description, image, toolchains, memory, cpus, scratch_size, tmp_size, cache_env, builtin, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,FALSE,$11,$11)`,
		p.ID, p.Name, p.Description, p.Image, p.Toolchains, p.Memory, p.CPUs, p.ScratchSize, p.TmpSize, p.CacheEnv, now)
	return err
}

// UpdateSandboxProfile rewrites a custom profile. A builtin row accepts only
// resource overrides (memory, cpus, scratch/tmp sizes) and cache env: the
// catalog identity (name, description, toolchains) travels with kernel
// updates and the image is pinned by build, so a PUT never rewrites them.
func (s *Store) UpdateSandboxProfile(ctx context.Context, p SandboxProfile) error {
	existing, err := s.GetSandboxProfile(ctx, p.ID)
	if err != nil {
		return err
	}
	if existing.Builtin {
		p.Name = existing.Name
		p.Description = existing.Description
		p.Toolchains = existing.Toolchains
		p.Image = existing.Image
		p.Builtin = true
	} else {
		p.Builtin = false
	}
	if err := ValidateSandboxProfile(p); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_sandbox_profile
		 SET name=$2, description=$3, image=$4, toolchains=$5, memory=$6, cpus=$7, scratch_size=$8, tmp_size=$9, cache_env=$10, updated_at=$11
		 WHERE id=$1`,
		p.ID, p.Name, p.Description, p.Image, p.Toolchains, p.Memory, p.CPUs, p.ScratchSize, p.TmpSize, p.CacheEnv, time.Now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSandboxProfileImage pins a freshly built digest into a builtin profile
// and refreshes the catalog metadata (name, description, toolchains) from the
// in-code catalog. Locally tuned resource overrides and cache env survive the
// rebuild: they are editable through the API and belong to the deployment.
func (s *Store) SetSandboxProfileImage(ctx context.Context, id, image string) error {
	var builtin SandboxProfile
	found := false
	for _, p := range BuiltinSandboxProfiles() {
		if p.ID == id {
			builtin = p
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %s is not a builtin profile", ErrProfileBuiltin, id)
	}
	if !IsDigestPinnedImage(image) {
		return fmt.Errorf("image %q must be pinned by a sha256 digest", image)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_sandbox_profile
		 SET image=$2, name=$3, description=$4, toolchains=$5, updated_at=now()
		 WHERE id=$1 AND builtin = TRUE`,
		id, image, builtin.Name, builtin.Description, builtin.Toolchains)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.GetSandboxProfile(ctx, id); err != nil {
			return err
		}
		return errors.New("sandbox profile row did not update")
	}
	return nil
}

// DeleteSandboxProfile removes a custom profile. Refusing when agents still
// reference it keeps assigned cards from pointing at a missing image.
func (s *Store) DeleteSandboxProfile(ctx context.Context, id string) error {
	profile, err := s.GetSandboxProfile(ctx, id)
	if err != nil {
		return err
	}
	if profile.Builtin {
		return fmt.Errorf("%w: %s", ErrProfileBuiltin, id)
	}
	agents, err := s.ListAllAgents(ctx)
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.SandboxProfile == id {
			return fmt.Errorf("%w: %s is used by agent %s", ErrProfileInUse, id, a.ID)
		}
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_sandbox_profile WHERE id=$1 AND builtin = FALSE`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
