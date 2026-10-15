package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
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

const artifactColumns = `id, title, description, provider, external_ref, content, content_hash, size_bytes, mime_type, org_unit_id, created_at, updated_at`

func scanArtifact(row interface{ Scan(dest ...any) error }) (Artifact, error) {
	var a Artifact
	var orgUnitID *string
	if err := row.Scan(&a.ID, &a.Title, &a.Description, &a.Provider, &a.ExternalRef,
		&a.Content, &a.ContentHash, &a.SizeBytes, &a.MimeType, &orgUnitID, &a.CreatedAt, &a.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return a, ErrNotFound
		}
		return a, err
	}
	a.OrgUnitID = derefPtr(orgUnitID)
	return a, nil
}

func (s *Store) CreateArtifact(ctx context.Context, artifact *Artifact) error {
	artifact.normalize()
	now := time.Now().UTC()
	artifact.CreatedAt = now
	artifact.UpdatedAt = now
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_artifact (id, title, description, provider, external_ref, content, content_hash, size_bytes, mime_type, org_unit_id, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		artifact.ID, artifact.Title, artifact.Description, artifact.Provider, artifact.ExternalRef,
		artifact.Content, artifact.ContentHash, artifact.SizeBytes, artifact.MimeType,
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

// ListArtifactsVisible honors the org-visibility rule shared by every
// workspace resource: org_unit_id IS NULL (installation-wide) or a unit on
// the caller's path. Bodies are stripped: listing must not ship megabytes.
func (s *Store) ListArtifactsVisible(ctx context.Context, units []string) ([]Artifact, error) {
	if units == nil {
		return s.ListAllArtifacts(ctx)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+artifactColumns+` FROM workspace_artifact
		 WHERE org_unit_id IS NULL OR org_unit_id = ANY($1) ORDER BY title, id`, units)
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

// UpdateArtifact rewrites metadata and body; normalize recomputes the hash.
// Like the agent update path, the org binding is preserved: bindings move
// through the admin binding endpoint only.
func (s *Store) UpdateArtifact(ctx context.Context, artifact Artifact) error {
	artifact.normalize()
	artifact.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_artifact
		 SET title=$2, description=$3, provider=$4, external_ref=$5, content=$6, content_hash=$7, size_bytes=$8, mime_type=$9, updated_at=$10
		 WHERE id=$1`,
		artifact.ID, artifact.Title, artifact.Description, artifact.Provider, artifact.ExternalRef,
		artifact.Content, artifact.ContentHash, artifact.SizeBytes, artifact.MimeType, artifact.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
