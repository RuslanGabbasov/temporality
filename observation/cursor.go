package observation

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

type cursor struct {
	OccurredAt time.Time `json:"occurred_at"`
	ReceivedAt time.Time `json:"received_at"`
	SourceID   string    `json:"source_id"`
	EventID    string    `json:"event_id"`
}

func EncodeCursor(event Event) string {
	value, _ := json.Marshal(cursor{OccurredAt: event.OccurredAt, ReceivedAt: event.ReceivedAt, SourceID: event.Source.ID, EventID: event.EventID})
	return base64.RawURLEncoding.EncodeToString(value)
}

func DecodeCursor(value string) (*cursor, error) {
	if value == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("invalid cursor encoding")
	}
	var result cursor
	if err = json.Unmarshal(data, &result); err != nil || result.OccurredAt.IsZero() || result.ReceivedAt.IsZero() || result.SourceID == "" || result.EventID == "" {
		return nil, errors.New("invalid cursor")
	}
	return &result, nil
}

func AfterCursor(event Event, value cursor) bool {
	if !event.OccurredAt.Equal(value.OccurredAt) {
		return event.OccurredAt.After(value.OccurredAt)
	}
	if !event.ReceivedAt.Equal(value.ReceivedAt) {
		return event.ReceivedAt.After(value.ReceivedAt)
	}
	if event.Source.ID != value.SourceID {
		return event.Source.ID > value.SourceID
	}
	return event.EventID > value.EventID
}
