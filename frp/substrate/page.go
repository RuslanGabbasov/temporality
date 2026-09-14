package substrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/temporality-project/temporality/frp/protocol"
)

const (
	DefaultPageSize = 100
	MaxPageSize     = 1000
)

type PageRequest struct {
	Filter EventFilter
	Cursor string
	Limit  int
}

type EventPage struct {
	Events     []protocol.Event `json:"events"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type PageStore interface {
	ListPage(context.Context, PageRequest) (EventPage, error)
}

type Cursor struct {
	ValidTime       time.Time `json:"valid_time"`
	TransactionTime time.Time `json:"tx_time"`
	EventID         string    `json:"event_id"`
}

func NormalizePageSize(limit int) (int, error) {
	if limit == 0 {
		return DefaultPageSize, nil
	}
	if limit < 0 || limit > MaxPageSize {
		return 0, errors.New("limit must be between 1 and 1000")
	}
	return limit, nil
}

func EncodeCursor(event protocol.Event) string {
	data, _ := json.Marshal(Cursor{ValidTime: event.ValidTime, TransactionTime: event.TransactionTime, EventID: event.EventID})
	return base64.RawURLEncoding.EncodeToString(data)
}

func DecodeCursor(value string) (*Cursor, error) {
	if value == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("invalid cursor encoding")
	}
	var cursor Cursor
	if err = json.Unmarshal(data, &cursor); err != nil || cursor.ValidTime.IsZero() || cursor.TransactionTime.IsZero() || cursor.EventID == "" {
		return nil, errors.New("invalid cursor")
	}
	return &cursor, nil
}

func BuildPage(events []protocol.Event, limit int) EventPage {
	page := EventPage{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		page.NextCursor = EncodeCursor(page.Events[len(page.Events)-1])
	}
	return page
}
