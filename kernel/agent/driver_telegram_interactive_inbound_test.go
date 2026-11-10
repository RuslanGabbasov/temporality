package agent

import (
	"encoding/json"
	"testing"
	"time"
)

// Interactive updates (Phase 1 + Phase 3): button presses and widget ops are
// first-class inbound events — they carry their Kind, the transport receipt
// and the message they landed on, and they advance the offset cursor exactly
// like messages (a skipped interaction must never stall the poll).
func TestParseTelegramUpdatesCallbackQuery(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"ok": true,
		"result": []any{map[string]any{
			"update_id": 200,
			"callback_query": map[string]any{
				"id":   "cbq-1",
				"from": map[string]any{"id": 555},
				"data": "a:op1:0",
				"message": map[string]any{
					"message_id": 42,
					"chat":       map[string]any{"id": 555, "type": "private"},
				},
			},
		}},
	})
	updates, next, err := ParseTelegramUpdates(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if next != "201" {
		t.Fatalf("next = %q, want 201", next)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %d", len(updates))
	}
	u := updates[0]
	if u.Kind != "callback" || u.QueryID != "cbq-1" || u.CallbackData != "a:op1:0" {
		t.Fatalf("update = %+v", u)
	}
	if u.ChatID != 555 || u.SenderID != 555 || u.SourceMessageID != 42 {
		t.Fatalf("ids = %+v", u)
	}
	msg := telegramNormalizedOne(u)
	if msg.Kind != "callback" || msg.QueryID != "cbq-1" || msg.CallbackData != "a:op1:0" || msg.SourceMessageID != "42" {
		t.Fatalf("normalized = %+v", msg)
	}
}

func TestParseTelegramUpdatesWidgetQuery(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"ok": true,
		"result": []any{map[string]any{
			"update_id": 300,
			"widget_query": map[string]any{
				"id":         "wq-1",
				"from":       map[string]any{"id": 777},
				"type":       "data",
				"data":       `{"op":"set","i":0,"v":"canary"}`,
				"chat":       map[string]any{"id": 777, "type": "private"},
				"message_id": 99,
			},
		}},
	})
	updates, next, err := ParseTelegramUpdates(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if next != "301" {
		t.Fatalf("next = %q", next)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %d", len(updates))
	}
	u := updates[0]
	if u.Kind != "widget_query" || u.QueryID != "wq-1" || u.SourceMessageID != 99 {
		t.Fatalf("update = %+v", u)
	}
	if _, err := DecodeWidgetOp(u.CallbackData); err != nil {
		t.Fatalf("op must survive the channel verbatim: %v", err)
	}
}

func TestParseTelegramUpdatesSkipsMalformedInteractions(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"ok": true,
		"result": []any{
			map[string]any{"update_id": 400, "callback_query": map[string]any{
				"id": "cbq-2", "from": map[string]any{"id": 1}, // no data, no message
			}},
			map[string]any{"update_id": 401, "widget_query": map[string]any{
				"from": map[string]any{"id": 2}, // no id
			}},
			telegramUpdateBody(402, 555, 555, "обычное сообщение", nil),
		},
	})
	updates, next, err := ParseTelegramUpdates(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if next != "403" {
		t.Fatalf("cursor must advance past skipped interactions, got %q", next)
	}
	if len(updates) != 1 || updates[0].Kind != "message" {
		t.Fatalf("updates = %+v", updates)
	}
}

func TestParseTelegramUpdatesMessageKindDefault(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"ok":     true,
		"result": []any{telegramUpdateBody(500, 555, 555, "текст", nil)},
	})
	updates, _, err := ParseTelegramUpdates(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if updates[0].Kind != "message" {
		t.Fatalf("kind = %q", updates[0].Kind)
	}
	if updates[0].Timestamp.After(time.Now()) {
		t.Fatal("message timestamp must come from the update")
	}
}
