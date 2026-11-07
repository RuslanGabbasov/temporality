package workspace

// Presentations (docs/presentation-engine.md): the durable store of
// agent-authored structured documents. This file implements the persistence
// slice of kernel/presentation's Store contract — the lifecycle state
// machine itself lives in the kernel package and is enforced here again at
// the SQL boundary so a hostile API caller cannot bypass it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/temporality-project/temporality/kernel/presentation"
)

// Presentation mirrors presentation.Presentation with storage-shaped jsonb
// fields; the API layer converts between the two so the kernel package never
// imports the database.
type Presentation struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	RunID        string          `json:"run_id,omitempty"`
	TaskID       string          `json:"task_id,omitempty"`
	AgentID      string          `json:"agent_id,omitempty"`
	Title        string          `json:"title,omitempty"`
	Status       string          `json:"status"`
	Version      int             `json:"version"`
	Document     json.RawMessage `json:"document"`
	TextFallback string          `json:"text_fallback,omitempty"`
	Artifacts    []string        `json:"artifacts,omitempty"`
	CreatedBy    string          `json:"created_by,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	PublishedAt  *time.Time      `json:"published_at,omitempty"`
	ExpiresAt    *time.Time      `json:"expires_at,omitempty"`
}

// PresentationSizeLimit bounds one stored document: the kernel validator
// caps the IR at 256 KiB; the storage cap adds JSON envelope slack.
const PresentationSizeLimit = 384 << 10

// PresentationRevisionLimit bounds the kept version history per row.
const PresentationRevisionLimit = 20

// presentationStatuses is the CHECK constraint mirror for fast refusals.
var presentationStatuses = map[string]bool{
	"draft": true, "published": true, "updated": true, "archived": true, "expired": true,
}

// ValidatePresentation enforces the storage contract before any write.
func ValidatePresentation(p *Presentation) error {
	if p.ID == "" || len(p.ID) > 128 {
		return fmt.Errorf("presentation id must be 1..128 characters")
	}
	if p.ProjectID == "" {
		return errors.New("project_id is required")
	}
	if !json.Valid(p.Document) {
		return errors.New("document must be valid JSON")
	}
	if len(p.Document) > PresentationSizeLimit {
		return fmt.Errorf("document exceeds %d bytes", PresentationSizeLimit)
	}
	if !presentationStatuses[p.Status] {
		return fmt.Errorf("unknown presentation status %q", p.Status)
	}
	if p.Version < 1 {
		return errors.New("version must be >= 1")
	}
	return nil
}

// ToKernel converts the storage row into the kernel runtime record.
func (p Presentation) ToKernel() presentation.Presentation {
	return presentation.Presentation{
		ID: p.ID, ProjectID: p.ProjectID, RunID: p.RunID, TaskID: p.TaskID,
		AgentID: p.AgentID, Title: p.Title, Status: presentation.Status(p.Status),
		Version: p.Version, Document: p.Document, TextFallback: p.TextFallback,
		Artifacts: p.Artifacts, CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt, PublishedAt: p.PublishedAt, ExpiresAt: p.ExpiresAt,
	}
}

// PresentationFromKernel converts a kernel runtime record into a storage row.
func PresentationFromKernel(p presentation.Presentation) Presentation {
	return Presentation{
		ID: p.ID, ProjectID: p.ProjectID, RunID: p.RunID, TaskID: p.TaskID,
		AgentID: p.AgentID, Title: p.Title, Status: string(p.Status),
		Version: p.Version, Document: p.Document, TextFallback: p.TextFallback,
		Artifacts: p.Artifacts, CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt, PublishedAt: p.PublishedAt, ExpiresAt: p.ExpiresAt,
	}
}

const presentationColumns = `id, project_id, run_id, task_id, agent_id, title, status, version,
	document, text_fallback, artifacts, created_by, created_at, updated_at, published_at, expires_at`

func scanPresentation(row pgx.Row) (Presentation, error) {
	var p Presentation
	var artifacts []byte
	err := row.Scan(&p.ID, &p.ProjectID, &p.RunID, &p.TaskID, &p.AgentID, &p.Title, &p.Status, &p.Version,
		&p.Document, &p.TextFallback, &artifacts, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt, &p.PublishedAt, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if len(artifacts) > 0 {
		_ = json.Unmarshal(artifacts, &p.Artifacts)
	}
	return p, nil
}

// CreatePresentation persists one row. The status must be draft or published
// (creation never lands in updated/archived/expired); publishing stamps the
// published/expires timestamps per the kernel lifecycle defaults.
func (s *Store) CreatePresentation(ctx context.Context, p *Presentation) error {
	if err := ValidatePresentation(p); err != nil {
		return err
	}
	if p.Status != "draft" && p.Status != "published" {
		return fmt.Errorf("%w: create with status %q", presentation.ErrIllegalTransition, p.Status)
	}
	now := time.Now().UTC()
	if p.Status == "published" {
		p.PublishedAt = &now
		expires := presentation.ExpiryFor(now)
		p.ExpiresAt = &expires
	}
	artifacts, err := json.Marshal(p.Artifacts)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO workspace_presentation (`+presentationColumns+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		p.ID, p.ProjectID, p.RunID, p.TaskID, p.AgentID, p.Title, p.Status, p.Version,
		[]byte(p.Document), p.TextFallback, artifacts, p.CreatedBy, now, now, p.PublishedAt, p.ExpiresAt)
	if err != nil {
		return err
	}
	p.CreatedAt, p.UpdatedAt = now, now
	return nil
}

// GetPresentation loads one row by id.
func (s *Store) GetPresentation(ctx context.Context, id string) (Presentation, error) {
	return scanPresentation(s.pool.QueryRow(ctx,
		`SELECT `+presentationColumns+` FROM workspace_presentation WHERE id = $1`, id))
}

// UpdatePresentation stores a new version of the document: the previous
// document moves into the revision history (bounded), the row advances to
// updated. Only published/updated rows accept updates — the kernel state
// machine, enforced at the boundary.
func (s *Store) UpdatePresentation(ctx context.Context, p *Presentation, revision presentation.Revision) error {
	if err := ValidatePresentation(p); err != nil {
		return err
	}
	current, err := s.GetPresentation(ctx, p.ID)
	if err != nil {
		return err
	}
	if presentation.Status(current.Status) != presentation.StatusPublished &&
		presentation.Status(current.Status) != presentation.StatusUpdated {
		return fmt.Errorf("%w: %s -> updated", presentation.ErrIllegalTransition, current.Status)
	}
	now := time.Now().UTC()
	p.UpdatedAt = now
	p.Status = "updated"
	if revision.ChangedAt.IsZero() {
		revision.ChangedAt = now
	}
	revision.Version = current.Version
	artifacts, err := json.Marshal(p.Artifacts)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`INSERT INTO workspace_presentation_revision (presentation_id, version, document, changed_by, changed_at)
		 VALUES ($1,$2,$3,$4,$5)`,
		p.ID, current.Version, []byte(current.Document), revision.ChangedBy, revision.ChangedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE workspace_presentation SET document = $2, title = $3, version = version + 1,
		 status = 'updated', text_fallback = $4, artifacts = $5, run_id = $6, task_id = $7, updated_at = $8
		 WHERE id = $1`,
		p.ID, []byte(p.Document), p.Title, p.TextFallback, artifacts, p.RunID, p.TaskID, now); err != nil {
		return err
	}
	// The history stays bounded: versions older than the window fall off.
	if _, err := tx.Exec(ctx,
		`DELETE FROM workspace_presentation_revision
		 WHERE presentation_id = $1 AND version <= $2`,
		p.ID, current.Version+1-PresentationRevisionLimit); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	p.Version = current.Version + 1
	return nil
}

// SetPresentationStatus moves one row through the lifecycle state machine
// with optimistic status checking: the UPDATE only lands when the row is
// still in the expected state.
func (s *Store) SetPresentationStatus(ctx context.Context, id string, from, to presentation.Status) error {
	if err := presentation.ValidateTransition(from, to); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_presentation SET status = $3, updated_at = $4,
		 published_at = COALESCE(published_at, CASE WHEN $3 = 'published' THEN $4 END),
		 expires_at = COALESCE(expires_at, CASE WHEN $3 = 'published' THEN $5 END)
		 WHERE id = $1 AND status = $2`,
		id, string(from), string(to), time.Now().UTC(), presentation.ExpiryFor(time.Now().UTC()))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s is not in state %s", ErrNotFound, id, from)
	}
	return nil
}

// ListPresentations returns the project's rows, newest first.
func (s *Store) ListPresentations(ctx context.Context, projectID string, limit int) ([]Presentation, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+presentationColumns+` FROM workspace_presentation WHERE project_id = $1
		 ORDER BY created_at DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Presentation, 0, 16)
	for rows.Next() {
		p, err := scanPresentation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPresentationsByRun returns the run's presentations in document order
// — the chat renderer resolves which visual block belongs to an answer.
func (s *Store) ListPresentationsByRun(ctx context.Context, runID string, limit int) ([]Presentation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+presentationColumns+` FROM workspace_presentation WHERE run_id = $1
		 ORDER BY created_at ASC LIMIT $2`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Presentation, 0, 4)
	for rows.Next() {
		p, err := scanPresentation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPresentationRevisions returns the version history, newest first.
func (s *Store) ListPresentationRevisions(ctx context.Context, id string) ([]presentation.Revision, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT version, document, changed_by, changed_at FROM workspace_presentation_revision
		 WHERE presentation_id = $1 ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]presentation.Revision, 0, 4)
	for rows.Next() {
		var revision presentation.Revision
		if err := rows.Scan(&revision.Version, &revision.Document, &revision.ChangedBy, &revision.ChangedAt); err != nil {
			return nil, err
		}
		out = append(out, revision)
	}
	return out, rows.Err()
}

// ExpireOverdue moves rows past their expiry into the expired state.
func (s *Store) ExpireOverdue(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_presentation SET status = 'expired', updated_at = $2
		 WHERE expires_at IS NOT NULL AND expires_at < $2 AND status IN ('published','updated')`, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// KernelPresentationStore exposes the store through the kernel runtime's
// persistence contract: the kernel validates and owns the state machine,
// this store owns the rows. The adapter converts the record types at the
// boundary so neither package leaks its internals into the other.
type kernelPresentationStore struct{ store *Store }

func (k kernelPresentationStore) CreatePresentation(ctx context.Context, p *presentation.Presentation) error {
	row := PresentationFromKernel(*p)
	if err := k.store.CreatePresentation(ctx, &row); err != nil {
		return err
	}
	*p = row.ToKernel()
	return nil
}

func (k kernelPresentationStore) GetPresentation(ctx context.Context, id string) (presentation.Presentation, error) {
	row, err := k.store.GetPresentation(ctx, id)
	if err != nil {
		return presentation.Presentation{}, err
	}
	return row.ToKernel(), nil
}

func (k kernelPresentationStore) UpdatePresentation(ctx context.Context, p *presentation.Presentation, revision presentation.Revision) error {
	row := PresentationFromKernel(*p)
	if err := k.store.UpdatePresentation(ctx, &row, revision); err != nil {
		return err
	}
	*p = row.ToKernel()
	return nil
}

func (k kernelPresentationStore) SetPresentationStatus(ctx context.Context, id string, from, to presentation.Status) error {
	return k.store.SetPresentationStatus(ctx, id, from, to)
}

func (k kernelPresentationStore) ListPresentations(ctx context.Context, projectID string, limit int) ([]presentation.Presentation, error) {
	rows, err := k.store.ListPresentations(ctx, projectID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]presentation.Presentation, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ToKernel())
	}
	return out, nil
}

func (k kernelPresentationStore) ExpireOverdue(ctx context.Context, now time.Time) (int64, error) {
	return k.store.ExpireOverdue(ctx, now)
}

// KernelPresentationStore returns the adapter; the returned value satisfies
// presentation.Store (compile-checked here).
func (s *Store) KernelPresentationStore() presentation.Store {
	return kernelPresentationStore{store: s}
}

// AttachArtifact records one artifact id on the presentation row (the
// attach_artifact operation, §10.2). The artifacts list stays bounded — a
// presentation references work, it is not a dump.
func (s *Store) AttachArtifact(ctx context.Context, id, artifactID, actor string) (Presentation, error) {
	if artifactID == "" || len(artifactID) > 128 {
		return Presentation{}, errors.New("artifact_id must be 1..128 characters")
	}
	current, err := s.GetPresentation(ctx, id)
	if err != nil {
		return Presentation{}, err
	}
	for _, existing := range current.Artifacts {
		if existing == artifactID {
			return current, nil // idempotent attach
		}
	}
	if len(current.Artifacts) >= 20 {
		return Presentation{}, errors.New("presentation holds the artifact attach limit (20)")
	}
	updated := append(current.Artifacts, artifactID)
	encoded, err := json.Marshal(updated)
	if err != nil {
		return Presentation{}, err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE workspace_presentation SET artifacts = $2, updated_at = $3 WHERE id = $1`,
		id, encoded, time.Now().UTC()); err != nil {
		return Presentation{}, err
	}
	_ = actor
	return s.GetPresentation(ctx, id)
}
