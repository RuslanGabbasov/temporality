package observation

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("observation event not found")
	ErrConflict = errors.New("source event ID already exists with different content")
)

func NormalizeLimit(limit int) (int, error) {
	if limit == 0 {
		return 100, nil
	}
	if limit < 1 || limit > 500 {
		return 0, errors.New("limit must be between 1 and 500")
	}
	return limit, nil
}

type Filter struct {
	Project  string
	SourceID string
	EventID  string
	Run      string
	Task     string
	Actor    string
	Type     string
	Since    *time.Time
	Until    *time.Time
	KnownAt  *time.Time
	Limit    int
	// ScopeKind restricts to events whose data.scope_kind matches exactly.
	// Used to load shared (org_unit / organization) knowledge regardless of
	// which project originated it.
	ScopeKind string
	// ScopeIDs restricts to events whose data.scope_id is one of the listed
	// org units; meaningful together with ScopeKind="org_unit".
	ScopeIDs []string
	// KnowledgeIDs restricts to events whose data.knowledge_id is listed;
	// used to pull the full lifecycle of shared knowledge across projects.
	KnowledgeIDs []string
}

type Page struct {
	Events     []Event `json:"events"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

type Store interface {
	AppendObservation(context.Context, Event) (inserted bool, err error)
	GetObservation(context.Context, string, string) (Event, error)
	ListObservations(context.Context, Filter) ([]Event, error)
	ListObservationPage(context.Context, Filter, string) (Page, error)
}
