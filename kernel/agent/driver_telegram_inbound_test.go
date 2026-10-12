package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// telegramUpdateBody builds one getUpdates result entry.
func telegramUpdateBody(updateID int64, chatID int64, senderID int64, text string, extra map[string]any) map[string]any {
	message := map[string]any{
		"date": time.Now().Unix(),
		"chat": map[string]any{"id": chatID, "type": "private"},
	}
	if senderID != 0 {
		message["from"] = map[string]any{"id": senderID}
	}
	if text != "" {
		message["text"] = text
	}
	for key, value := range extra {
		message[key] = value
	}
	return map[string]any{"update_id": updateID, "message": message}
}

func TestParseTelegramUpdatesTextMessage(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"ok":     true,
		"result": []any{telegramUpdateBody(100, 555, 555, "подготовь смету", nil)},
	})
	updates, next, err := ParseTelegramUpdates(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}
	if next != "101" {
		t.Fatalf("next cursor = %q, want 101", next)
	}
	if updates[0].ChatID != 555 || updates[0].SenderID != 555 || updates[0].Text != "подготовь смету" {
		t.Fatalf("update = %+v", updates[0])
	}
	normalized := telegramNormalized(updates)
	if normalized[0].Channel != "telegram" || normalized[0].ChatID != "555" || normalized[0].Sender != "555" || normalized[0].ID != "100" {
		t.Fatalf("normalized = %+v", normalized[0])
	}
}

func TestParseTelegramUpdatesDocumentWithCaption(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"ok": true,
		"result": []any{telegramUpdateBody(101, 555, 555, "", map[string]any{
			"caption":  "шаблон сметы",
			"document": map[string]any{"file_id": "AgACFile", "file_name": "smeta.xlsx"},
		})},
	})
	updates, next, err := ParseTelegramUpdates(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || next != "102" {
		t.Fatalf("updates=%d next=%q", len(updates), next)
	}
	if updates[0].Text != "шаблон сметы" {
		t.Fatalf("caption must become text, got %q", updates[0].Text)
	}
	if len(updates[0].Files) != 1 || updates[0].Files[0].Ref != "AgACFile" || updates[0].Files[0].Name != "smeta.xlsx" {
		t.Fatalf("files = %+v", updates[0].Files)
	}
}

func TestParseTelegramUpdatesPhotoTakesLargest(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"ok": true,
		"result": []any{telegramUpdateBody(102, 555, 555, "", map[string]any{
			"photo": []any{
				map[string]any{"file_id": "small", "file_size": 1000},
				map[string]any{"file_id": "large", "file_size": 90000},
				map[string]any{"file_id": "medium", "file_size": 20000},
			},
		})},
	})
	updates, _, err := ParseTelegramUpdates(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || len(updates[0].Files) != 1 {
		t.Fatalf("updates=%d files=%d", len(updates), len(updates[0].Files))
	}
	if updates[0].Files[0].Ref != "large" {
		t.Fatalf("must pick the largest photo, got %q", updates[0].Files[0].Ref)
	}
}

func TestParseTelegramUpdatesSkipsNonMessages(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"ok": true,
		"result": []any{
			map[string]any{"update_id": 200, "callback_query": map[string]any{"id": "cq"}},
			map[string]any{"update_id": 201},                                                                  // update without a message
			telegramUpdateBody(202, 555, 555, "", map[string]any{"sticker": map[string]any{"file_id": "st"}}), // no text, no supported files
			telegramUpdateBody(0, 555, 555, "no from", map[string]any{"from": nil}),
		},
	})
	updates, next, err := ParseTelegramUpdates(body, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 0 {
		t.Fatalf("updates = %d, want 0", len(updates))
	}
	if next != "203" {
		t.Fatalf("cursor must still advance past skipped updates, got %q", next)
	}
}

func TestParseTelegramUpdatesEmptyBatchKeepsCursor(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"ok": true, "result": []any{}})
	_, next, err := ParseTelegramUpdates(body, "345")
	if err != nil {
		t.Fatal(err)
	}
	if next != "345" {
		t.Fatalf("cursor = %q, want unchanged 345", next)
	}
}

// telegramBotAPI fakes the Bot API endpoints the poller and the delivery side
// use, on a private api_base (the local Bot API server deployment shape).
func telegramBotAPI(t *testing.T, updates []any, offsetParam *string, downloadPath string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/getMe"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"id": 999, "is_bot": true}})
		case strings.HasSuffix(path, "/getUpdates"):
			if offsetParam != nil {
				*offsetParam = r.URL.Query().Get("offset")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": updates})
		case strings.HasSuffix(path, "/getFile"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"file_path": downloadPath}})
		case strings.Contains(path, "/file/bot"):
			fmt.Fprint(w, "file-bytes")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestTelegramPollerPollSendsOffsetAndNormalizes(t *testing.T) {
	var offset string
	server := telegramBotAPI(t, []any{telegramUpdateBody(300, 777, 777, "вопрос по задаче", nil)}, &offset, "")
	settings := map[string]string{"bot_token": "tok", "api_base": server.URL}
	messages, next, err := telegramPoller{}.Poll(context.Background(), server.Client(), settings, "300")
	if err != nil {
		t.Fatal(err)
	}
	if offset != "300" {
		t.Fatalf("offset param = %q, want 300", offset)
	}
	if next != "301" {
		t.Fatalf("next = %q", next)
	}
	if len(messages) != 1 || messages[0].ChatID != "777" || messages[0].Sender != "777" || messages[0].ID != "300" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestTelegramPollerSelf(t *testing.T) {
	server := telegramBotAPI(t, nil, nil, "")
	self, err := telegramPoller{}.Self(context.Background(), server.Client(), map[string]string{"bot_token": "tok", "api_base": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if self != "bot:999" {
		t.Fatalf("self = %q", self)
	}
}

func TestTelegramPollerFetchFile(t *testing.T) {
	server := telegramBotAPI(t, nil, nil, "documents/report.txt")
	payload, err := telegramPoller{}.FetchFile(context.Background(), server.Client(),
		map[string]string{"bot_token": "tok", "api_base": server.URL}, InboundFile{Ref: "f1", Name: "report.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "file-bytes" {
		t.Fatalf("payload = %q", payload)
	}
}

func TestTelegramDriverRegisteredWithInbound(t *testing.T) {
	driver := channelDriverByType("telegram")
	if driver == nil {
		t.Fatal("telegram driver not registered")
	}
	if driver.Poller() == nil {
		t.Fatal("telegram driver must expose a poller now")
	}
	if _, ok := driver.Poller().(ChannelFileFetcher); !ok {
		t.Fatal("telegram poller must fetch files (FilesIn capability)")
	}
	spec := driver.Spec()
	if !spec.Capabilities.TwoWay || !spec.Capabilities.FilesIn {
		t.Fatalf("capabilities = %+v", spec.Capabilities)
	}
}
