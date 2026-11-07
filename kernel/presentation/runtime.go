package presentation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Lifecycle (docs/presentation-engine.md §13): a presentation is the record
// of one communication, not knowledge — no memory entries are produced from
// rendering facts. The runtime owns the state machine, the storage persists
// rows; both stay swappable for tests.

// Status is the lifecycle state of one presentation.
type Status string

const (
	// StatusDraft: created, not yet visible.
	StatusDraft Status = "draft"
	// StatusPublished: delivered to the channel/UI.
	StatusPublished Status = "published"
	// StatusUpdated: published and revised at least once.
	StatusUpdated Status = "updated"
	// StatusArchived: withdrawn from active view.
	StatusArchived Status = "archived"
	// StatusExpired: the retention window passed.
	StatusExpired Status = "expired"
)

// ValidTransitions is the state machine of §13. Transitions outside it are
// refused — archived presentations do not resurrect by accident.
var ValidTransitions = map[Status][]Status{
	StatusDraft:     {StatusPublished, StatusArchived, StatusExpired},
	StatusPublished: {StatusUpdated, StatusArchived, StatusExpired},
	StatusUpdated:   {StatusUpdated, StatusArchived, StatusExpired},
	StatusArchived:  {},
	StatusExpired:   {},
}

// CanTransition reports whether moving from -> to is legal.
func CanTransition(from, to Status) bool {
	for _, next := range ValidTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// ErrIllegalTransition is returned by the store helpers when a lifecycle
// move violates the state machine.
var ErrIllegalTransition = errors.New("illegal presentation lifecycle transition")

// Presentation is one stored document with its lifecycle metadata (§13:
// version history, agent run link, work item link, author, timestamps).
type Presentation struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	RunID        string          `json:"run_id,omitempty"`
	TaskID       string          `json:"task_id,omitempty"`
	AgentID      string          `json:"agent_id,omitempty"`
	Title        string          `json:"title,omitempty"`
	Status       Status          `json:"status"`
	Version      int             `json:"version"`
	Document     json.RawMessage `json:"document"`
	TextFallback string          `json:"text_fallback,omitempty"`
	CreatedBy    string          `json:"created_by,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	PublishedAt  *time.Time      `json:"published_at,omitempty"`
	ExpiresAt    *time.Time      `json:"expires_at,omitempty"`
	// Artifacts lists the artifact ids attached through attach_artifact.
	Artifacts []string `json:"artifacts,omitempty"`
}

// Revision is one stored version of a presentation (§13: version history).
type Revision struct {
	Version   int             `json:"version"`
	Document  json.RawMessage `json:"document"`
	ChangedBy string          `json:"changed_by,omitempty"`
	ChangedAt time.Time       `json:"changed_at"`
}

// ErrNotFound mirrors the workspace store's sentinel: callers compare with
// errors.Is without importing two packages for one case.
var ErrNotFound = errors.New("presentation not found")

// Store is the persistence contract of the runtime. The workspace store
// implements it over PostgreSQL; tests use in-memory fakes. One presentation
// row keeps its full revision list (bounded by the store implementation).
type Store interface {
	// CreatePresentation persists a new row in the given status.
	CreatePresentation(ctx context.Context, p *Presentation) error
	// GetPresentation loads one row by id.
	GetPresentation(ctx context.Context, id string) (Presentation, error)
	// UpdatePresentation stores a new version of the document and bumps the
	// row to StatusUpdated (only legal from Published/Updated).
	UpdatePresentation(ctx context.Context, p *Presentation, revision Revision) error
	// SetPresentationStatus moves the row through the state machine.
	SetPresentationStatus(ctx context.Context, id string, from, to Status) error
	// ListPresentations returns the project's rows, newest first.
	ListPresentations(ctx context.Context, projectID string, limit int) ([]Presentation, error)
	// ExpireOverdue moves expired rows (expires_at < now) to StatusExpired;
	// called from periodic maintenance, best-effort.
	ExpireOverdue(ctx context.Context, now time.Time) (int64, error)
}

// ValidateTransition enforces the state machine and returns the well-formed
// error for refusals.
func ValidateTransition(from, to Status) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	return nil
}

// DefaultExpiry is how long a published presentation stays addressable
// before Expired (§13); presentations remain communication records, not
// permanent knowledge, so the default window is bounded.
const DefaultExpiry = 90 * 24 * time.Hour

// ExpiryFor computes the expiry timestamp of a published presentation.
func ExpiryFor(publishedAt time.Time) time.Time {
	return publishedAt.Add(DefaultExpiry)
}

// NormalizeStatus maps empty/unknown statuses onto Draft: storage always
// holds a legal state.
func NormalizeStatus(raw string) Status {
	switch Status(raw) {
	case StatusDraft, StatusPublished, StatusUpdated, StatusArchived, StatusExpired:
		return Status(raw)
	default:
		return StatusDraft
	}
}
