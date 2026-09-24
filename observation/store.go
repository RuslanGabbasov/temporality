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
