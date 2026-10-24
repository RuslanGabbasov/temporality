package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Artifact lifecycle state machine (docs/artifact-lifecycle.md §5). The store,
// not the clients, owns the transitions: draft → in_review → approved/rejected,
// with edits reopening rejected versions as drafts.

// ErrArtifactLocked reports an edit attempt against a version whose status
// forbids it (in_review: locked while a review is open).
var ErrArtifactLocked = errors.New("artifact is locked")

// Artifact statuses and kinds (migration 000064).
const (
	ArtifactDraft    = "draft"
	ArtifactInReview = "in_review"
	ArtifactApproved = "approved"
	ArtifactRejected = "rejected"

	ArtifactKindReference = "reference"
	ArtifactKindWork      = "work"
)

// ArtifactReview is one review round of one artifact version
// (docs/artifact-lifecycle.md §3.3). A version may go through several rounds;
// each keeps its own decision set.
type ArtifactReview struct {
	ID              string                    `json:"id"`
	ArtifactID      string                    `json:"artifact_id"`
	ArtifactVersion int                       `json:"artifact_version"`
	Reviewers       []string                  `json:"reviewers"`
	Mode            string                    `json:"mode"` // all | any
	Status          string                    `json:"status"`
	Note            string                    `json:"note,omitempty"`
	RequestedBy     string                    `json:"requested_by"`
	RequestedAt     time.Time                 `json:"requested_at"`
	Decisions       map[string]ReviewDecision `json:"decisions,omitempty"`
	ResolvedBy      string                    `json:"resolved_by,omitempty"`
	ResolvedAt      *time.Time                `json:"resolved_at,omitempty"`
	// Sweep bookkeeping (docs/artifact-lifecycle.md §6 Phase 2): when the
	// deadline reminder and the escalation text went out. NULL = not sent.
	RemindedAt  *time.Time `json:"reminded_at,omitempty"`
	EscalatedAt *time.Time `json:"escalated_at,omitempty"`
}

// ReviewDecision is one reviewer's answer inside a review round. Group
// entries (org:/role:/project_owner) may be satisfied by any member: the
// decision is keyed by the entry, DecidedBy names the concrete principal.
type ReviewDecision struct {
	Decision  string    `json:"decision"` // approve | reject
	Comment   string    `json:"comment,omitempty"`
	At        time.Time `json:"at"`
	DecidedBy string    `json:"decided_by,omitempty"` // concrete principal behind a group entry
}

const artifactReviewColumns = `id, artifact_id, artifact_version, reviewers, mode, status, note, requested_by, requested_at, decisions, resolved_by, resolved_at, reminded_at, escalated_at`

func scanArtifactReview(row interface{ Scan(dest ...any) error }) (ArtifactReview, error) {
	return scanArtifactReviewInto(row, nil)
}

// scanArtifactReviewInto scans the review columns plus any trailing extras
// (joins like the sweep's title/org unit) in one pass — a pgx Rows is a
// cursor, so a row must be read with a single Scan.
func scanArtifactReviewInto(row interface{ Scan(dest ...any) error }, extra []any) (ArtifactReview, error) {
	var r ArtifactReview
	var decisions []byte
	var resolvedBy *string
	var remindedAt, escalatedAt *time.Time
	dest := []any{&r.ID, &r.ArtifactID, &r.ArtifactVersion, &r.Reviewers, &r.Mode, &r.Status,
		&r.Note, &r.RequestedBy, &r.RequestedAt, &decisions, &resolvedBy, &r.ResolvedAt, &remindedAt, &escalatedAt}
	err := row.Scan(append(dest, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if resolvedBy != nil {
		r.ResolvedBy = *resolvedBy
	}
	r.RemindedAt = remindedAt
	r.EscalatedAt = escalatedAt
	if len(decisions) > 0 {
		_ = json.Unmarshal(decisions, &r.Decisions)
	}
	return r, nil
}

// ValidateArtifactReviewers checks the recipient syntax shared with ask_human
// (docs/inbound-messages.md §7): user:<id>, org:<unit>, role:<role>,
// project_owner, or a bare workspace user id. org:/role: entries expand at
// notification time, not here — the review row stores what was asked.
func ValidateArtifactReviewers(reviewers []string) error {
	if len(reviewers) == 0 {
		return errors.New("at least one reviewer is required")
	}
	seen := make(map[string]bool, len(reviewers))
	for _, who := range reviewers {
		if who == "" || seen[who] {
			return fmt.Errorf("reviewers must be non-empty and unique (got %q)", who)
		}
		seen[who] = true
	}
	return nil
}

// SubmitArtifactReview moves a draft/rejected version to in_review and opens
// a review round. Idempotent per version: an already open round for this
// version returns as-is without touching state or creating a second row.
func (s *Store) SubmitArtifactReview(ctx context.Context, artifactID string, reviewers []string, mode, note, requestedBy string) (ArtifactReview, error) {
	if err := ValidateArtifactReviewers(reviewers); err != nil {
		return ArtifactReview{}, err
	}
	if mode == "" {
		mode = "all"
	}
	if mode != "all" && mode != "any" {
		return ArtifactReview{}, fmt.Errorf("mode must be all or any (got %q)", mode)
	}
	// An open revision (docs/artifact-lifecycle.md §5): the draft lives in
	// the version table while the approved version stays live — the round
	// reviews the draft, the live row keeps serving agents until approval.
	var draftVersion int
	var draftStatus string
	draftErr := s.pool.QueryRow(ctx,
		`SELECT version, status FROM workspace_artifact_version
		 WHERE artifact_id=$1 AND status IN ('draft','in_review','rejected')
		 ORDER BY version DESC LIMIT 1`, artifactID).Scan(&draftVersion, &draftStatus)
	if draftErr == nil {
		existing, err := scanArtifactReview(s.pool.QueryRow(ctx,
			`SELECT `+artifactReviewColumns+` FROM workspace_artifact_review
			 WHERE artifact_id = $1 AND artifact_version = $2 AND status = 'open'`, artifactID, draftVersion))
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return ArtifactReview{}, err
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE workspace_artifact_version SET status = 'in_review' WHERE artifact_id = $1 AND version = $2`,
			artifactID, draftVersion); err != nil {
			return ArtifactReview{}, err
		}
		review := ArtifactReview{
			ID: fmt.Sprintf("%s-v%d-%d", artifactID, draftVersion, time.Now().UTC().UnixNano()),
			ArtifactID: artifactID, ArtifactVersion: draftVersion,
			Reviewers: reviewers, Mode: mode, Status: "open",
			Note: note, RequestedBy: requestedBy, RequestedAt: time.Now().UTC(),
			Decisions: map[string]ReviewDecision{},
		}
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO workspace_artifact_review (`+artifactReviewColumns+`)
			 VALUES ($1,$2,$3,$4,$5,'open',$6,$7,$8,'{}'::jsonb,NULL,NULL,NULL,NULL)`,
			review.ID, review.ArtifactID, review.ArtifactVersion, review.Reviewers, review.Mode,
			review.Note, review.RequestedBy, review.RequestedAt); err != nil {
			return ArtifactReview{}, err
		}
		return review, nil
	}
	if !errors.Is(draftErr, pgx.ErrNoRows) {
		return ArtifactReview{}, draftErr
	}
	var version int
	err := s.pool.QueryRow(ctx, `SELECT version FROM workspace_artifact WHERE id = $1`, artifactID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactReview{}, ErrNotFound
	}
	if err != nil {
		return ArtifactReview{}, err
	}
	// Idempotency: one open round per version.
	existing, err := scanArtifactReview(s.pool.QueryRow(ctx,
		`SELECT `+artifactReviewColumns+` FROM workspace_artifact_review
		 WHERE artifact_id = $1 AND artifact_version = $2 AND status = 'open'`, artifactID, version))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return ArtifactReview{}, err
	}
	// draft/rejected → in_review. An approved version cannot re-enter review:
	// editing it opens a new draft version first (SaveArtifactEdit).
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_artifact SET status = 'in_review', updated_at = now() WHERE id = $1 AND status IN ('draft','rejected')`, artifactID)
	if err != nil {
		return ArtifactReview{}, err
	}
	if tag.RowsAffected() == 0 {
		var status string
		if err := s.pool.QueryRow(ctx, `SELECT status FROM workspace_artifact WHERE id = $1`, artifactID).Scan(&status); err != nil {
			return ArtifactReview{}, err
		}
		return ArtifactReview{}, fmt.Errorf("%w: artifact is %s; only draft or rejected versions enter review", ErrArtifactLocked, status)
	}
	review := ArtifactReview{
		ID:         fmt.Sprintf("%s-v%d-%d", artifactID, version, time.Now().UTC().UnixNano()),
		ArtifactID: artifactID, ArtifactVersion: version,
		Reviewers: reviewers, Mode: mode, Status: "open",
		Note: note, RequestedBy: requestedBy, RequestedAt: time.Now().UTC(),
		Decisions: map[string]ReviewDecision{},
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO workspace_artifact_review (`+artifactReviewColumns+`)
		 VALUES ($1,$2,$3,$4,$5,'open',$6,$7,$8,'{}'::jsonb,NULL,NULL,NULL,NULL)`,
		review.ID, review.ArtifactID, review.ArtifactVersion, review.Reviewers, review.Mode,
		review.Note, review.RequestedBy, review.RequestedAt)
	if err != nil {
		return ArtifactReview{}, err
	}
	return review, nil
}

// ResolveArtifactReview records one reviewer's decision and closes the round
// by its mode: any — the first answer decides; all — a reject closes
// immediately, an approve closes when every reviewer entry has approved.
// Closing moves the artifact to approved/rejected. Double resolution is a
// conflict.
//
// entries are the reviewer entries the deciding principal satisfies (see
// MatchArtifactReviewers): a group entry (org:core) is decided by any of its
// members, and one principal may satisfy several entries at once — the
// decision then lands under each of them. decidedBy is the concrete
// principal, kept on every decision and in resolved_by.
func (s *Store) ResolveArtifactReview(ctx context.Context, reviewID string, entries []string, decidedBy string, approve bool, comment string) (ArtifactReview, Artifact, error) {
	if len(entries) == 0 {
		return ArtifactReview{}, Artifact{}, fmt.Errorf("%w: %q is not a reviewer of this round", ErrConflict, decidedBy)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ArtifactReview{}, Artifact{}, err
	}
	defer tx.Rollback(ctx)
	review, err := scanArtifactReview(tx.QueryRow(ctx,
		`SELECT `+artifactReviewColumns+` FROM workspace_artifact_review WHERE id = $1 FOR UPDATE`, reviewID))
	if err != nil {
		return ArtifactReview{}, Artifact{}, err
	}
	if review.Status != "open" {
		return ArtifactReview{}, Artifact{}, fmt.Errorf("%w: review is already %s", ErrConflict, review.Status)
	}
	// Every entry must be listed on the round — the handler matched the
	// principal against the round's reviewer entries, not free-form strings.
	for _, entry := range entries {
		listed := false
		for _, who := range review.Reviewers {
			if who == entry {
				listed = true
				break
			}
		}
		if !listed {
			return ArtifactReview{}, Artifact{}, fmt.Errorf("%w: %q is not a reviewer of this round", ErrConflict, entry)
		}
	}
	if review.Decisions == nil {
		review.Decisions = map[string]ReviewDecision{}
	}
	decision := "reject"
	if approve {
		decision = "approve"
	}
	for _, entry := range entries {
		review.Decisions[entry] = ReviewDecision{Decision: decision, Comment: comment, At: time.Now().UTC(), DecidedBy: decidedBy}
	}

	closes, closedStatus := false, ""
	if !approve {
		closes, closedStatus = true, "rejected"
	} else if review.Mode == "any" {
		closes, closedStatus = true, "approved"
	} else {
		allApproved := true
		for _, who := range review.Reviewers {
			d, ok := review.Decisions[who]
			if !ok || d.Decision != "approve" {
				allApproved = false
				break
			}
		}
		if allApproved {
			closes, closedStatus = true, "approved"
		}
	}
	encoded, _ := json.Marshal(review.Decisions)
	if closes {
		now := time.Now().UTC()
		review.Status = closedStatus
		review.ResolvedBy = decidedBy
		review.ResolvedAt = &now
		if _, err := tx.Exec(ctx,
			`UPDATE workspace_artifact_review SET decisions = $2, status = $3, resolved_by = $4, resolved_at = $5 WHERE id = $1`,
			review.ID, encoded, review.Status, decidedBy, now); err != nil {
			return ArtifactReview{}, Artifact{}, err
		}
		// The round may review a pending revision (a draft row in the version
		// table) rather than the live row — the live row stayed approved and
		// kept serving agents while the revision was open. Approve promotes
		// the draft wholesale (the previous approved version is stamped
		// superseded exactly now); reject retires the draft row.
		var draftStatus string
		draftErr := tx.QueryRow(ctx,
			`SELECT status FROM workspace_artifact_version WHERE artifact_id=$1 AND version=$2 FOR UPDATE`,
			review.ArtifactID, review.ArtifactVersion).Scan(&draftStatus)
		switch {
		case draftErr == nil:
			if !approve {
				if _, err := tx.Exec(ctx,
					`UPDATE workspace_artifact_version SET status = 'rejected' WHERE artifact_id=$1 AND version=$2`,
					review.ArtifactID, review.ArtifactVersion); err != nil {
					return ArtifactReview{}, Artifact{}, err
				}
				break
			}
			if _, err := tx.Exec(ctx,
				`UPDATE workspace_artifact a SET
				   title = v.title, description = v.description, provider = v.provider, external_ref = v.external_ref,
				   content = v.content, content_hash = v.content_hash, size_bytes = v.size_bytes, mime_type = v.mime_type,
				   kind = v.kind, saga_id = v.saga_id, status = 'approved', version = v.version, updated_at = now()
				 FROM workspace_artifact_version v
				 WHERE a.id = $1 AND v.artifact_id = a.id AND v.version = $2`,
				review.ArtifactID, review.ArtifactVersion); err != nil {
				return ArtifactReview{}, Artifact{}, err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE workspace_artifact_version SET status = 'approved' WHERE artifact_id=$1 AND version=$2`,
				review.ArtifactID, review.ArtifactVersion); err != nil {
				return ArtifactReview{}, Artifact{}, err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE workspace_artifact_version SET superseded_at = $3
				 WHERE artifact_id=$1 AND version < $2 AND status = 'approved' AND superseded_at IS NULL`,
				review.ArtifactID, review.ArtifactVersion, now); err != nil {
				return ArtifactReview{}, Artifact{}, err
			}
		case errors.Is(draftErr, pgx.ErrNoRows):
			// Live-row flow: the row itself was in_review.
			if _, err := tx.Exec(ctx,
				`UPDATE workspace_artifact SET status = $2, updated_at = now() WHERE id = $1`, review.ArtifactID, closedStatus); err != nil {
				return ArtifactReview{}, Artifact{}, err
			}
		default:
			return ArtifactReview{}, Artifact{}, draftErr
		}
	} else if _, err := tx.Exec(ctx,
		`UPDATE workspace_artifact_review SET decisions = $2 WHERE id = $1`, review.ID, encoded); err != nil {
		return ArtifactReview{}, Artifact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ArtifactReview{}, Artifact{}, err
	}
	artifact, err := s.GetArtifactRange(ctx, review.ArtifactID, 0, 0)
	if err != nil {
		return review, artifact, err
	}
	return review, artifact, nil
}

// GetArtifactReview loads one review round: the decision handler matches the
// principal against the round's entries before resolving.
func (s *Store) GetArtifactReview(ctx context.Context, reviewID string) (ArtifactReview, error) {
	return scanArtifactReview(s.pool.QueryRow(ctx,
		`SELECT `+artifactReviewColumns+` FROM workspace_artifact_review WHERE id = $1`, reviewID))
}

// MatchArtifactReviewers returns the reviewer entries the principal
// satisfies (docs/artifact-lifecycle.md §6 — the ask_human recipient syntax):
// user:<id> and bare ids/names match the principal directly, role:/org:
// match the principal's workspace profile, project_owner degrades to the
// installation admin (docs/org-structure.md §14 — projects carry no owner
// yet). An account without an active workspace row satisfies nothing:
// deactivated reviewers must not decide.
func MatchArtifactReviewers(principal string, users []User, entries []string) []string {
	var self *User
	for i := range users {
		if users[i].ID == principal || users[i].Name == principal {
			self = &users[i]
			break
		}
	}
	if self == nil || !self.Active {
		return nil
	}
	var matched []string
	for _, entry := range entries {
		switch {
		case entry == "project_owner":
			if self.Role == "admin" {
				matched = append(matched, entry)
			}
		case strings.HasPrefix(entry, "role:"):
			if self.Role == strings.TrimSpace(strings.TrimPrefix(entry, "role:")) {
				matched = append(matched, entry)
			}
		case strings.HasPrefix(entry, "org:"):
			if self.OrgUnitID == strings.TrimSpace(strings.TrimPrefix(entry, "org:")) {
				matched = append(matched, entry)
			}
		default:
			name := strings.TrimSpace(strings.TrimPrefix(entry, "user:"))
			if name != "" && (name == self.ID || name == self.Name) {
				matched = append(matched, entry)
			}
		}
	}
	return matched
}

// CountArtifactSubmits returns how many review rounds one run opened in the
// window (docs/artifact-lifecycle.md §9 quota for agent submits).
func (s *Store) CountArtifactSubmits(ctx context.Context, runID string, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM workspace_artifact_review WHERE requested_by = 'kernel-internal' AND requested_at >= $1
		 AND artifact_id IN (SELECT id FROM workspace_artifact WHERE origin_run = $2)`, since, runID).Scan(&n)
	return n, err
}

// ListArtifactReviews returns the review rounds of one artifact, newest first.
func (s *Store) ListArtifactReviews(ctx context.Context, artifactID string) ([]ArtifactReview, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+artifactReviewColumns+` FROM workspace_artifact_review
		 WHERE artifact_id = $1 ORDER BY requested_at DESC`, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ArtifactReview
	for rows.Next() {
		review, err := scanArtifactReview(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, review)
	}
	return result, rows.Err()
}

// StaleArtifactReview is one open review round with the artifact fields the
// reminder sweep needs (docs/artifact-lifecycle.md §6 Phase 2): the title for
// the channel texts and the org unit for escalation routing.
type StaleArtifactReview struct {
	Review        ArtifactReview
	ArtifactTitle string
	OrgUnitID     string
}

// ListOpenArtifactReviews returns every open review round with its artifact's
// title and org unit, oldest first — the sweep's working set.
func (s *Store) ListOpenArtifactReviews(ctx context.Context) ([]StaleArtifactReview, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+artifactReviewColumns+`, a.title, COALESCE(a.org_unit_id, '')
		 FROM workspace_artifact_review r
		 JOIN workspace_artifact a ON a.id = r.artifact_id
		 WHERE r.status = 'open'
		 ORDER BY r.requested_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []StaleArtifactReview{}
	for rows.Next() {
		var stale StaleArtifactReview
		review, err := scanArtifactReviewInto(rows, []any{&stale.ArtifactTitle, &stale.OrgUnitID})
		if err != nil {
			return nil, err
		}
		stale.Review = review
		result = append(result, stale)
	}
	return result, rows.Err()
}

// MarkArtifactReviewReminded stamps the deadline reminder on an open round —
// the sweep's dedupe: one reminder per round, not one per sweep tick. Closed
// rounds never re-enter the sweep.
func (s *Store) MarkArtifactReviewReminded(ctx context.Context, id string) error {
	return s.markArtifactReviewSweep(ctx, id, `reminded_at`)
}

// MarkArtifactReviewEscalated stamps the escalation on an open round.
func (s *Store) MarkArtifactReviewEscalated(ctx context.Context, id string) error {
	return s.markArtifactReviewSweep(ctx, id, `escalated_at`)
}

func (s *Store) markArtifactReviewSweep(ctx context.Context, id, column string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_artifact_review SET `+column+` = now() WHERE id = $1 AND status = 'open' AND `+column+` IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound // closed meanwhile or already stamped — nothing to do
	}
	return nil
}

// ArtifactRefsApproved is the skill-manifest invariant (docs/artifact-lifecycle.md
// §8): every artifacts[] reference must exist and hold an approved version.
// The returned error names the offending ids with their statuses.
func (s *Store) ArtifactRefsApproved(ctx context.Context, manifest json.RawMessage) error {
	if len(manifest) == 0 {
		return nil
	}
	var refs struct {
		Artifacts []struct {
			ID string `json:"id"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(manifest, &refs); err != nil {
		return nil // malformed manifests fail their own validation elsewhere
	}
	if len(refs.Artifacts) == 0 {
		return nil
	}
	ids := make([]string, 0, len(refs.Artifacts))
	for _, ref := range refs.Artifacts {
		if ref.ID != "" {
			ids = append(ids, ref.ID)
		}
	}
	artifacts, err := s.ArtifactsByIDs(ctx, ids)
	if err != nil {
		return err
	}
	return CheckArtifactRefsApproved(artifacts, ids)
}

// CheckArtifactRefsApproved is the pure half of the skill-manifest invariant:
// against the resolved rows, every referenced id must exist and be approved.
// Exported for tests and future call sites that already hold the rows.
func CheckArtifactRefsApproved(artifacts []Artifact, ids []string) error {
	byID := make(map[string]Artifact, len(artifacts))
	for _, a := range artifacts {
		byID[a.ID] = a
	}
	var offenders []string
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		a, ok := byID[id]
		if !ok {
			offenders = append(offenders, fmt.Sprintf("%s: missing", id))
			continue
		}
		if a.Status != ArtifactApproved {
			offenders = append(offenders, fmt.Sprintf("%s: %s", id, a.Status))
		}
	}
	if len(offenders) > 0 {
		return fmt.Errorf("artifact_not_approved: skills may reference approved artifacts only — %s", joinStrings(offenders, "; "))
	}
	return nil
}

func joinStrings(items []string, sep string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += sep
		}
		out += item
	}
	return out
}
