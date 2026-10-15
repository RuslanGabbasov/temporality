package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Artifact is a reference document (docs/artifact-registry.md §3.1): a
// regulation, approved instruction or checklist that skills reference from
// their manifests but never inline into the prompt — the agent loads the
// body on demand through artifact_read. Description is the applicability
// condition ("read this before X when Y"), not a content summary: it is the
// signal the agent matches the task against.
type Artifact struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Provider    string `json:"provider"` // builtin | url
	ExternalRef string `json:"external_ref,omitempty"`
	// Content is the document body (builtin provider). List queries never
	// select it; GetArtifactRange is the only sized reader.
	Content     string `json:"content,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	MimeType    string `json:"mime_type,omitempty"`
	// Lifecycle (docs/artifact-lifecycle.md §3): status is per version,
	// kind separates reference documents from saga work products, origin
	// records who created the artifact (human user or agent run) and stays
	// fixed — the approver signs off knowing exactly what they approve.
	Status     string `json:"status"`                // draft | in_review | approved | rejected
	Kind       string `json:"kind"`                  // reference | work
	Version    int    `json:"version"`               // 1-based; approved bodies are immutable per version
	OriginType string `json:"origin_type"`           // human | agent
	OriginUser string `json:"origin_user,omitempty"` // creator (human path)
	OriginRun  string `json:"origin_run,omitempty"`  // creating run (agent path)
	SagaID     string `json:"saga_id,omitempty"`     // traceability, not access (visibility stays org-scoped)
	// Org binding (docs/org-structure.md §3.2): empty = whole installation.
	OrgUnitID string    `json:"org_unit_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// HashArtifactContent returns the sha256 hex digest of the body. Every write
// recomputes it; artifact.read events carry it, so audit answers "which
// document version informed this run" without body snapshots.
func HashArtifactContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// normalize fills the derived fields (hash, size) for builtin artifacts and
// blanks them for reference-only providers, where the body lives outside the
// registry.
func (a *Artifact) normalize() {
	if a.MimeType == "" {
		a.MimeType = "text/markdown"
	}
	if a.Provider != "builtin" {
		a.Content = ""
		a.ContentHash = ""
		a.SizeBytes = 0
		return
	}
	a.ContentHash = HashArtifactContent(a.Content)
	a.SizeBytes = int64(len(a.Content))
}

const artifactColumns = `id, title, description, provider, external_ref, content, content_hash, size_bytes, mime_type, status, kind, version, origin_type, origin_user, origin_run, saga_id, org_unit_id, created_at, updated_at`

func scanArtifact(row interface{ Scan(dest ...any) error }) (Artifact, error) {
	var a Artifact
	var orgUnitID, originUser, originRun, sagaID *string
	if err := row.Scan(&a.ID, &a.Title, &a.Description, &a.Provider, &a.ExternalRef,
		&a.Content, &a.ContentHash, &a.SizeBytes, &a.MimeType,
		&a.Status, &a.Kind, &a.Version, &a.OriginType, &originUser, &originRun, &sagaID, &orgUnitID, &a.CreatedAt, &a.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return a, ErrNotFound
		}
		return a, err
	}
	a.OriginUser = derefPtr(originUser)
	a.OriginRun = derefPtr(originRun)
	a.SagaID = derefPtr(sagaID)
	a.OrgUnitID = derefPtr(orgUnitID)
	return a, nil
}

func (s *Store) CreateArtifact(ctx context.Context, artifact *Artifact) error {
	artifact.normalize()
	if artifact.Kind == "" {
		artifact.Kind = "reference"
	}
	if artifact.Kind != "reference" && artifact.Kind != "work" {
		return fmt.Errorf("kind must be reference or work (got %q)", artifact.Kind)
	}
	// Safe default: a row without an explicit status is a draft — approval is
	// always an explicit human act, never a storage default (docs/artifact-lifecycle.md §4).
	if artifact.Status == "" {
		artifact.Status = "draft"
	}
	if artifact.Status != "draft" && artifact.Status != "approved" {
		return fmt.Errorf("create accepts draft or approved status only (got %q)", artifact.Status)
	}
	if artifact.OriginType == "" {
		artifact.OriginType = "human"
	}
	if artifact.Version == 0 {
		artifact.Version = 1
	}
	now := time.Now().UTC()
	artifact.CreatedAt = now
	artifact.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_artifact (id, title, description, provider, external_ref, content, content_hash, size_bytes, mime_type, status, kind, version, origin_type, origin_user, origin_run, saga_id, org_unit_id, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		artifact.ID, artifact.Title, artifact.Description, artifact.Provider, artifact.ExternalRef,
		artifact.Content, artifact.ContentHash, artifact.SizeBytes, artifact.MimeType,
		artifact.Status, artifact.Kind, artifact.Version, artifact.OriginType,
		nullString(artifact.OriginUser), nullString(artifact.OriginRun), nullString(artifact.SagaID),
		nullString(artifact.OrgUnitID), artifact.CreatedAt, artifact.UpdatedAt)
	return err
}

// GetArtifactRange returns the artifact with a slice of its body: content
// bytes [offset, offset+limit). limit <= 0 means the whole body. The chunk
// boundary backs off to a UTF-8 rune boundary so a multi-byte character is
// never split across chunks.
func (s *Store) GetArtifactRange(ctx context.Context, id string, offset, limit int) (Artifact, error) {
	if offset < 0 {
		offset = 0
	}
	artifact, err := scanArtifact(s.pool.QueryRow(ctx,
		`SELECT `+artifactColumns+` FROM workspace_artifact WHERE id = $1`, id))
	if err != nil {
		return artifact, err
	}
	artifact.chunk(offset, limit)
	return artifact, nil
}

// chunk replaces Content with the requested byte range. next_offset and
// truncation are the caller's to compute from SizeBytes, which stays whole.
func (a *Artifact) chunk(offset, limit int) {
	content := a.Content
	if offset > len(content) {
		offset = len(content)
	}
	content = content[offset:]
	if limit > 0 && limit < len(content) {
		content = content[:limit]
		// Back off to a rune boundary: a cut continuation sequence decodes as
		// (RuneError, 1) from the end, so dropping byte-by-byte converges on
		// the last complete character — the model never sees a garbled one.
		for len(content) > 0 {
			r, size := utf8.DecodeLastRuneInString(content)
			if r != utf8.RuneError || size != 1 {
				break
			}
			content = content[:len(content)-1]
		}
	}
	a.Content = content
}

func (s *Store) ListAllArtifacts(ctx context.Context) ([]Artifact, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+artifactColumns+` FROM workspace_artifact ORDER BY title, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArtifacts(rows)
}

// ArtifactFilter narrows a listing (docs/artifact-lifecycle.md §9): status and
// kind are exact matches, saga_id traces a saga's work products. Zero values
// mean "no constraint".
type ArtifactFilter struct {
	Status string
	Kind   string
	SagaID string
}

// ListArtifactsVisible honors the org-visibility rule shared by every
// workspace resource: org_unit_id IS NULL (installation-wide) or a unit on
// the caller's path. Bodies are stripped: listing must not ship megabytes.
func (s *Store) ListArtifactsVisible(ctx context.Context, units []string) ([]Artifact, error) {
	return s.ListArtifactsFiltered(ctx, units, ArtifactFilter{})
}

// ListArtifactsFiltered is ListArtifactsVisible plus lifecycle filters.
func (s *Store) ListArtifactsFiltered(ctx context.Context, units []string, f ArtifactFilter) ([]Artifact, error) {
	query := `SELECT ` + artifactColumns + ` FROM workspace_artifact WHERE 1=1`
	args := []any{}
	if units != nil {
		args = append(args, units)
		query += ` AND (org_unit_id IS NULL OR org_unit_id = ANY($` + strconv.Itoa(len(args)) + `))`
	}
	if f.Status != "" {
		args = append(args, f.Status)
		query += ` AND status = $` + strconv.Itoa(len(args))
	}
	if f.Kind != "" {
		args = append(args, f.Kind)
		query += ` AND kind = $` + strconv.Itoa(len(args))
	}
	if f.SagaID != "" {
		args = append(args, f.SagaID)
		query += ` AND saga_id = $` + strconv.Itoa(len(args))
	}
	query += ` ORDER BY title, id`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArtifacts(rows)
}

func scanArtifacts(rows pgx.Rows) ([]Artifact, error) {
	var result []Artifact
	for rows.Next() {
		artifact, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range result {
		result[i].Content = ""
	}
	return result, nil
}

// ArtifactsByIDs resolves manifest references to metadata rows. Order is by
// title for stable output; callers (skill_inspect enrichment) render it as-is.
func (s *Store) ArtifactsByIDs(ctx context.Context, ids []string) ([]Artifact, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+artifactColumns+` FROM workspace_artifact WHERE id = ANY($1) ORDER BY title`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	artifacts, err := scanArtifacts(rows)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Artifact, len(artifacts))
	for _, a := range artifacts {
		byID[a.ID] = a
	}
	// Deterministic order follows the manifest's reference order, not the
	// registry's title order: the skill author's intent reads top-down.
	ordered := make([]Artifact, 0, len(artifacts))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if a, ok := byID[id]; ok && !seen[id] {
			ordered = append(ordered, a)
			seen[id] = true
		}
	}
	return ordered, nil
}

// SaveArtifactEdit applies an edit respecting the lifecycle (docs/artifact-lifecycle.md
// §5): draft/rejected rewrite in place (an edit of a rejected version reopens
// it as draft), approved opens the next version as draft — the approved body is
// immutable, its trail lives in artifact.read events with content_hash — and
// in_review refuses (the version is locked while a review is open). The org
// binding is preserved: bindings move through the admin binding endpoint only.
func (s *Store) SaveArtifactEdit(ctx context.Context, artifact Artifact) error {
	artifact.normalize()
	artifact.Status = "draft"
	artifact.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_artifact
		 SET title=$2, description=$3, provider=$4, external_ref=$5, content=$6, content_hash=$7, size_bytes=$8, mime_type=$9, kind=$10, saga_id=$11, status='draft', updated_at=$12
		 WHERE id=$1 AND status IN ('draft','rejected')`,
		artifact.ID, artifact.Title, artifact.Description, artifact.Provider, artifact.ExternalRef,
		artifact.Content, artifact.ContentHash, artifact.SizeBytes, artifact.MimeType,
		artifact.Kind, nullString(artifact.SagaID), artifact.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// Editing an approved version opens the next one as a draft: same id, the
	// version pointer advances. With phase-1 storage (single body column) the
	// previous body is gone from the table — its hash trail in artifact.read
	// events keeps decisions reproducible; full snapshots are phase 3.
	tag, err = s.pool.Exec(ctx,
		`UPDATE workspace_artifact
		 SET title=$2, description=$3, provider=$4, external_ref=$5, content=$6, content_hash=$7, size_bytes=$8, mime_type=$9, kind=$10, saga_id=$11, status='draft', version=version+1, updated_at=$12
		 WHERE id=$1 AND status='approved'`,
		artifact.ID, artifact.Title, artifact.Description, artifact.Provider, artifact.ExternalRef,
		artifact.Content, artifact.ContentHash, artifact.SizeBytes, artifact.MimeType,
		artifact.Kind, nullString(artifact.SagaID), artifact.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return s.artifactEditConflict(ctx, artifact.ID)
}

// artifactEditConflict distinguishes the reasons an edit landed on zero rows.
func (s *Store) artifactEditConflict(ctx context.Context, id string) error {
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM workspace_artifact WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: artifact is %s (locked while a review is open)", ErrArtifactLocked, status)
}

func (s *Store) DeleteArtifact(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_artifact WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ArtifactReferencedBy returns the ids of skills whose manifest references
// the artifact. Manifests are JSONB; a scan keeps the delete guard honest
// without a join table, at a scale (tens of skills) where it is free.
func (s *Store) ArtifactReferencedBy(ctx context.Context, artifactID string) ([]string, error) {
	skills, err := s.ListAllSkills(ctx)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, skill := range skills {
		if len(skill.Manifest) == 0 {
			continue
		}
		var manifest struct {
			Artifacts []struct {
				ID string `json:"id"`
			} `json:"artifacts"`
		}
		if err := json.Unmarshal(skill.Manifest, &manifest); err != nil {
			continue
		}
		for _, ref := range manifest.Artifacts {
			if ref.ID == artifactID {
				refs = append(refs, skill.ID)
				break
			}
		}
	}
	sort.Strings(refs)
	return refs, nil
}

// ArtifactReferences maps artifact id → ids of the visible skills whose
// manifest references it, in one scan: the read-only twin of the delete
// guard, powering the registry UI's "used by N skills" indicator.
func (s *Store) ArtifactReferences(ctx context.Context, units []string) (map[string][]string, error) {
	skills, err := s.ListSkillsVisible(ctx, units)
	if err != nil {
		return nil, err
	}
	references := make(map[string][]string)
	for _, skill := range skills {
		if len(skill.Manifest) == 0 {
			continue
		}
		var manifest struct {
			Artifacts []struct {
				ID string `json:"id"`
			} `json:"artifacts"`
		}
		if err := json.Unmarshal(skill.Manifest, &manifest); err != nil {
			continue
		}
		for _, ref := range manifest.Artifacts {
			if ref.ID != "" {
				references[ref.ID] = append(references[ref.ID], skill.ID)
			}
		}
	}
	for id := range references {
		sort.Strings(references[id])
	}
	return references, nil
}
