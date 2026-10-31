package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"slices"
	"strconv"
	"strings"
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
	// Contributors is everyone who put a hand on the artifact: the human
	// creator, the run's agent, editors, the review requester, reviewers,
	// tag editors. The author filter and suggestions search this set —
	// agent-created artifacts carry an empty origin_user.
	Contributors []string `json:"contributors,omitempty"`
	// Tags are catalog labels (like real knowledge bases): free strings the
	// registry filters by — topics, products, teams. Catalog metadata, not
	// content: tagging never flips lifecycle state or bumps the version.
	Tags []string `json:"tags,omitempty"`
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
	// Tags: trimmed, lowercased, deduped — filters compare the same way.
	if len(a.Tags) > 0 {
		tags := make([]string, 0, len(a.Tags))
		seen := make(map[string]bool, len(a.Tags))
		for _, tag := range a.Tags {
			tag = strings.ToLower(strings.TrimSpace(tag))
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			tags = append(tags, tag)
		}
		a.Tags = tags
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

const artifactColumns = `id, title, description, provider, external_ref, content, content_hash, size_bytes, mime_type, status, kind, version, origin_type, origin_user, origin_run, saga_id, tags, contributors, org_unit_id, created_at, updated_at`

func scanArtifact(row interface{ Scan(dest ...any) error }) (Artifact, error) {
	var a Artifact
	var orgUnitID, originUser, originRun, sagaID *string
	if err := row.Scan(&a.ID, &a.Title, &a.Description, &a.Provider, &a.ExternalRef,
		&a.Content, &a.ContentHash, &a.SizeBytes, &a.MimeType,
		&a.Status, &a.Kind, &a.Version, &a.OriginType, &originUser, &originRun, &sagaID, &a.Tags, &a.Contributors, &orgUnitID, &a.CreatedAt, &a.UpdatedAt); err != nil {
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
	initialContributors := artifact.Contributors
	if artifact.OriginUser != "" && !slices.Contains(initialContributors, artifact.OriginUser) {
		initialContributors = append([]string{artifact.OriginUser}, initialContributors...)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO workspace_artifact (id, title, description, provider, external_ref, content, content_hash, size_bytes, mime_type, status, kind, version, origin_type, origin_user, origin_run, saga_id, tags, contributors, org_unit_id, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		artifact.ID, artifact.Title, artifact.Description, artifact.Provider, artifact.ExternalRef,
		artifact.Content, artifact.ContentHash, artifact.SizeBytes, artifact.MimeType,
		artifact.Status, artifact.Kind, artifact.Version, artifact.OriginType,
		nullString(artifact.OriginUser), nullString(artifact.OriginRun), nullString(artifact.SagaID),
		textArray(artifact.Tags), textArray(initialContributors), nullString(artifact.OrgUnitID), artifact.CreatedAt, artifact.UpdatedAt)
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
// ArtifactTagCount is one filter-dropdown option: the tag and how many
// visible artifacts carry it.
type ArtifactTagCount struct {
	Tag   string `json:"tag"`
	Count int64  `json:"count"`
}

type ArtifactFilter struct {
	Status string
	Kind   string
	SagaID string
	// Provider and OrgUnitID narrow further; OrgUnitID is honored for admins
	// only (the handler drops it otherwise) — everyone else keeps their
	// visibility chain.
	Provider  string
	OrgUnitID string
	// Query narrows by name: case-insensitive substring over title,
	// description and id — the search box for registries of tens of thousands.
	Query string
	// Tags is AND semantics: an artifact must carry every listed tag.
		Tags []string
		// Author narrows to one creator (origin_user, case-insensitive) — the
		// «my artifacts» and per-author views.
		Author string
		// AuthorIDs is the exact-match form of Author: the handler resolves the
		// operator's pick (a user id, name or email) into every identifier form
		// that may appear in origin_user/contributors (ids there, names on older
		// rows), and the row matches when any of them equals one of these.
		AuthorIDs []string
		// CreatedAt window: the new / this-week / this-month presets.
		CreatedAfter  *time.Time
		CreatedBefore *time.Time
		Limit  int
		Offset int
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
	if f.Provider != "" {
		args = append(args, f.Provider)
		query += ` AND provider = $` + strconv.Itoa(len(args))
	}
	if f.OrgUnitID != "" {
		args = append(args, f.OrgUnitID)
		query += ` AND org_unit_id = $` + strconv.Itoa(len(args))
	}
	if f.Query != "" {
		args = append(args, "%"+f.Query+"%")
		n := strconv.Itoa(len(args))
		// The id matches too: registry ids (discovery-omni-presence-100k)
		// carry meaning users search by, and titles alone don't cover them.
		query += ` AND (title ILIKE $` + n + ` OR description ILIKE $` + n + ` OR id ILIKE $` + n + `)`
	}
	// Creator filter matches EVERYONE who put a hand on the artifact —
	// origin_user and the contributors set (creator, agent, editors,
	// reviewers). Agent artifacts carry an empty origin_user, so origin_user
	// alone would find nothing for them.
	if f.Author != "" {
		args = append(args, "%"+f.Author+"%")
		n := strconv.Itoa(len(args))
		query += ` AND (origin_user ILIKE $` + n + ` OR EXISTS (SELECT 1 FROM unnest(contributors) AS c WHERE c ILIKE $` + n + `))`
	}
	if len(f.AuthorIDs) > 0 {
		// Exact user resolution (the filter row picks a directory user): id,
		// display name and email all count — contributors store ids on new rows
		// and free-form names on older ones.
		args = append(args, f.AuthorIDs)
		n := strconv.Itoa(len(args))
		query += ` AND (origin_user = ANY($` + n + `) OR EXISTS (SELECT 1 FROM unnest(contributors) AS c WHERE c = ANY($` + n + `)))`
	}
	if f.CreatedAfter != nil {
		args = append(args, *f.CreatedAfter)
		query += ` AND created_at >= $` + strconv.Itoa(len(args))
	}
	if f.CreatedBefore != nil {
		args = append(args, *f.CreatedBefore)
		query += ` AND created_at < $` + strconv.Itoa(len(args))
	}
	// AND semantics: every requested tag must be present (tags @> the set) —
	// narrowing filters, the way knowledge bases drill down.
	if len(f.Tags) > 0 {
		args = append(args, textArray(f.Tags))
		query += ` AND tags @> $` + strconv.Itoa(len(args))
	}
	query += ` ORDER BY title, id`
	if f.Limit > 0 {
		args = append(args, f.Limit)
		query += ` LIMIT $` + strconv.Itoa(len(args))
		if f.Offset > 0 {
			args = append(args, f.Offset)
			query += ` OFFSET $` + strconv.Itoa(len(args))
		}
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArtifacts(rows)
}

// SetArtifactTags replaces an artifact's tag set (the tag editor): tags are
// lowercased and deduped — the listing filter narrows by lowercase tags, so
// a mixed-case tag would silently never match. The artifact body is not
// touched; versions and review rounds keep their history.
func (s *Store) SetArtifactTags(ctx context.Context, id, principal string, tags []string) (Artifact, error) {
	defer func() { s.AddArtifactContributor(ctx, id, principal) }()
	clean := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		clean = append(clean, tag)
	}
	sort.Strings(clean)
	rows, err := s.pool.Query(ctx,
		`UPDATE workspace_artifact SET tags = $2, updated_at = now() WHERE id = $1 RETURNING `+artifactColumns,
		id, textArray(clean))
	if err != nil {
		return Artifact{}, err
	}
	defer rows.Close()
	artifacts, err := scanArtifacts(rows)
	if err != nil {
		return Artifact{}, err
	}
	if len(artifacts) == 0 {
		return Artifact{}, ErrNotFound
	}
	return artifacts[0], nil
}

// AddArtifactContributor appends one principal to the contributors set
// (idempotent): every mutation path calls it with its actor so the «who put
// a hand on this» list stays complete — the author filter and the
// suggestions search it.
func (s *Store) AddArtifactContributor(ctx context.Context, id, principal string) {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE workspace_artifact SET contributors = (SELECT array_agg(DISTINCT x) FROM unnest(contributors || ARRAY[$2]) AS x) WHERE id = $1`,
		id, principal)
}

// ListArtifactTags returns the distinct catalog tags visible to the org chain
// with how often each occurs — the filter dropdown's options, most-used
// first (docs/artifact-registry.md: tagging like real knowledge bases).
func (s *Store) ListArtifactTags(ctx context.Context, units []string) ([]ArtifactTagCount, error) {
	query := `SELECT tag, count(*) AS uses FROM workspace_artifact, unnest(tags) AS tag WHERE 1=1`
	args := []any{}
	if units != nil {
		args = append(args, units)
		query += ` AND (org_unit_id IS NULL OR org_unit_id = ANY($` + strconv.Itoa(len(args)) + `))`
	}
	query += ` GROUP BY tag ORDER BY uses DESC, tag LIMIT 200`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ArtifactTagCount
	for rows.Next() {
		var entry ArtifactTagCount
		if err := rows.Scan(&entry.Tag, &entry.Count); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
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
// §5, §9): draft/rejected rewrite in place (an edit of a rejected version reopens
// it as draft), in_review refuses (the version is locked while a review is
// open), and approved opens a NEW REVISION: the approved body is snapshotted
// into workspace_artifact_version (migration 0006), then the row becomes
// version+1 / draft with the new content. Approving the revision closes the
// loop — the previous approved version stays readable in the history. The
// tradeoff is documented (§8): while the revision is open the artifact's live
// status is draft, so skills referencing it see an unapproved source until
// the new version is approved.
func (s *Store) SaveArtifactEdit(ctx context.Context, artifact Artifact) error {
	artifact.normalize()
	artifact.Status = "draft"
	artifact.UpdatedAt = time.Now().UTC()
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_artifact
		 SET title=$2, description=$3, provider=$4, external_ref=$5, content=$6, content_hash=$7, size_bytes=$8, mime_type=$9, kind=$10, saga_id=$11, tags=$12, status='draft', updated_at=$13
		 WHERE id=$1 AND status IN ('draft','rejected')`,
		artifact.ID, artifact.Title, artifact.Description, artifact.Provider, artifact.ExternalRef,
		artifact.Content, artifact.ContentHash, artifact.SizeBytes, artifact.MimeType,
		artifact.Kind, nullString(artifact.SagaID), textArray(artifact.Tags), artifact.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return s.artifactEditConflict(ctx, artifact.ID)
}

// SaveArtifactNewVersion is the human edit path for an approved artifact
// (docs/artifact-lifecycle.md §5 «правка approved → draft(v+1)»): the
// approved version STAYS live — agents, skills and reads keep serving it —
// while the revision is a separate draft row in the version table. First
// call snapshots the approved body and opens the draft; follow-up calls
// update the same draft (a rejected draft reopens). The approved version is
// marked superseded only when the revision is approved (ResolveArtifactReview).
func (s *Store) SaveArtifactNewVersion(ctx context.Context, artifact Artifact) error {
	artifact.normalize()
	artifact.Status = "draft"
	artifact.UpdatedAt = time.Now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current Artifact
	var originUser string
	err = tx.QueryRow(ctx,
		`SELECT version, title, description, content, content_hash, size_bytes, mime_type, status, origin_type, COALESCE(origin_user, '')
		 FROM workspace_artifact WHERE id=$1 FOR UPDATE`, artifact.ID).
		Scan(&current.Version, &current.Title, &current.Description, &current.Content, &current.ContentHash,
			&current.SizeBytes, &current.MimeType, &current.Status, &current.OriginType, &originUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	current.OriginUser = originUser
	if current.Status == ArtifactInReview {
		return fmt.Errorf("%w: artifact is in_review — the version is locked while a review is open", ErrArtifactLocked)
	}
	if current.Status != ArtifactApproved {
		return fmt.Errorf("%w: artifact is %s", ErrArtifactLocked, current.Status)
	}
	// An already-open revision is updated in place; a rejected one reopens.
	var pendingVersion int
	var pendingStatus string
	pendingErr := tx.QueryRow(ctx,
		`SELECT version, status FROM workspace_artifact_version
		 WHERE artifact_id=$1 AND status IN ('draft','in_review','rejected')
		 ORDER BY version DESC LIMIT 1 FOR UPDATE`, artifact.ID).
		Scan(&pendingVersion, &pendingStatus)
	if pendingErr == nil {
		if pendingStatus == ArtifactInReview {
			return fmt.Errorf("%w: revision v%d is in_review — resolve the round first", ErrArtifactLocked, pendingVersion)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE workspace_artifact_version
			 SET title=$3, description=$4, content=$5, content_hash=$6, size_bytes=$7, mime_type=$8,
			     provider=$9, external_ref=$10, kind=$11, saga_id=$12, status='draft'
			 WHERE artifact_id=$1 AND version=$2`,
			artifact.ID, pendingVersion, artifact.Title, artifact.Description, artifact.Content,
			artifact.ContentHash, artifact.SizeBytes, artifact.MimeType,
			artifact.Provider, artifact.ExternalRef, artifact.Kind, nullString(artifact.SagaID)); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(pendingErr, pgx.ErrNoRows) {
		return pendingErr
	}
	// First revision: the approved body becomes immutable history (not yet
	// superseded — it serves until the revision is approved), then the draft.
	if _, err := tx.Exec(ctx,
		`INSERT INTO workspace_artifact_version
		 (artifact_id, version, title, description, content, content_hash, size_bytes, mime_type, status, origin_type, origin_user, provider, external_ref, kind, saga_id, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'approved',$9,$10,$11,$12,$13,$14, now())
		 ON CONFLICT (artifact_id, version) DO NOTHING`,
		artifact.ID, current.Version, current.Title, current.Description, current.Content, current.ContentHash,
		current.SizeBytes, current.MimeType, current.OriginType, nullString(current.OriginUser),
		current.Provider, current.ExternalRef, current.Kind, nullString(current.SagaID)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO workspace_artifact_version
		 (artifact_id, version, title, description, content, content_hash, size_bytes, mime_type, status, origin_type, origin_user, provider, external_ref, kind, saga_id, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'draft',$9,$10,$11,$12,$13,$14, now())`,
		artifact.ID, current.Version+1, artifact.Title, artifact.Description, artifact.Content,
		artifact.ContentHash, artifact.SizeBytes, artifact.MimeType,
		artifact.OriginType, nullString(artifact.OriginUser),
		artifact.Provider, artifact.ExternalRef, artifact.Kind, nullString(artifact.SagaID)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PendingArtifactRevision loads the open revision of an artifact — the draft
// row in the version table (draft/in_review/rejected), if any.
func (s *Store) PendingArtifactRevision(ctx context.Context, id string) (*ArtifactVersion, error) {
	var v ArtifactVersion
	var provider, externalRef, kind, sagaID string
	var supersededAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT version, title, description, content, content_hash, size_bytes, mime_type, status,
		        provider, external_ref, kind, COALESCE(saga_id, ''), superseded_at
		 FROM workspace_artifact_version
		 WHERE artifact_id=$1 AND status IN ('draft','in_review','rejected')
		 ORDER BY version DESC LIMIT 1`, id).
		Scan(&v.Version, &v.Title, &v.Description, &v.Content, &v.ContentHash, &v.SizeBytes, &v.MimeType, &v.Status,
			&provider, &externalRef, &kind, &sagaID, &supersededAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.Provider, v.ExternalRef, v.Kind, v.SagaID, v.SupersededAt = provider, externalRef, kind, sagaID, supersededAt
	return &v, nil
}

// ArtifactVersion is one archived revision of an artifact (migration 0006):
// approved versions are snapshotted when a new revision opens; the open
// revision itself is a draft row in the same table (migration 0007 adds the
// full document fields it carries).
type ArtifactVersion struct {
	Version      int        `json:"version"`
	Title        string     `json:"title"`
	Description  string     `json:"description,omitempty"`
	Content      string     `json:"content,omitempty"`
	ContentHash  string     `json:"content_hash,omitempty"`
	SizeBytes    int64      `json:"size_bytes"`
	MimeType     string     `json:"mime_type"`
	Status       string     `json:"status"`
	OriginType   string     `json:"origin_type,omitempty"`
	OriginUser   string     `json:"origin_user,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	SupersededAt *time.Time `json:"superseded_at,omitempty"`
	// Draft-row fields (migration 0007): the open revision carries the full
	// document so promotion can overwrite the live row wholesale.
	Provider    string `json:"provider,omitempty"`
	ExternalRef string `json:"external_ref,omitempty"`
	Kind        string `json:"kind,omitempty"`
	SagaID      string `json:"saga_id,omitempty"`
}

// ListArtifactVersions returns the archived revisions of one artifact,
// newest first, bodies included (bounded list — versions accumulate slowly:
// one row per approved revision).
func (s *Store) ListArtifactVersions(ctx context.Context, id string) ([]ArtifactVersion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT version, title, description, content, content_hash, size_bytes, mime_type, status, origin_type, COALESCE(origin_user, ''), created_at, superseded_at
		 FROM workspace_artifact_version WHERE artifact_id = $1 ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ArtifactVersion{}
	for rows.Next() {
		var v ArtifactVersion
		var originUser string
		var supersededAt *time.Time
		if err := rows.Scan(&v.Version, &v.Title, &v.Description, &v.Content, &v.ContentHash, &v.SizeBytes,
			&v.MimeType, &v.Status, &v.OriginType, &originUser, &v.CreatedAt, &supersededAt); err != nil {
			return nil, err
		}
		v.OriginUser = originUser
		v.SupersededAt = supersededAt
		result = append(result, v)
	}
	return result, rows.Err()
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
	switch status {
	case ArtifactApproved:
		return fmt.Errorf("%w: artifact is approved — the approved body is immutable; a new version path arrives with versioned storage (docs/artifact-lifecycle.md §9)", ErrArtifactLocked)
	case ArtifactInReview:
		return fmt.Errorf("%w: artifact is in_review — the version is locked while a review is open", ErrArtifactLocked)
	default:
		return fmt.Errorf("%w: artifact is %s", ErrArtifactLocked, status)
	}
}

func (s *Store) DeleteArtifact(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_artifact WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// The version archive has no FK to the live row — sweep it explicitly so
	// a deleted artifact leaves no orphaned bodies behind.
	_, _ = s.pool.Exec(ctx, `DELETE FROM workspace_artifact_version WHERE artifact_id=$1`, id)
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
