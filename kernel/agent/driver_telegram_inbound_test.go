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
	messages, next, err := newTelegramPoller().Poll(context.Background(), server.Client(), settings, "300")
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
	self, err := newTelegramPoller().Self(context.Background(), server.Client(), map[string]string{"bot_token": "tok", "api_base": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if self != "bot:999" {
		t.Fatalf("self = %q", self)
	}
}

func TestTelegramPollerFetchFile(t *testing.T) {
	server := telegramBotAPI(t, nil, nil, "documents/report.txt")
	payload, err := newTelegramPoller().FetchFile(context.Background(), server.Client(),
		map[string]string{"bot_token": "tok", "api_base": server.URL}, InboundFile{Ref: "f1", Name: "report.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "file-bytes" {
		t.Fatalf("payload = %q", payload)
	}
}

// TestTelegramPollerCoalescesMediaGroup: an album arrives as separate
// updates — here split across two getUpdates responses, with the first part
// replayed in the second batch — and must reach the pipeline as one message
// carrying every attachment. The text message routed in between is not held
// back by the buffering album.
func TestTelegramPollerCoalescesMediaGroup(t *testing.T) {
	oldDelay, oldStep := telegramAlbumFlushDelay, telegramAlbumPollStep
	telegramAlbumFlushDelay, telegramAlbumPollStep = 30*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { telegramAlbumFlushDelay, telegramAlbumPollStep = oldDelay, oldStep })

	photoUpdate := func(id int64, caption string) map[string]any {
		extra := map[string]any{
			"media_group_id": "group-1",
			"photo": []any{
				map[string]any{"file_id": fmt.Sprintf("thumb-%d", id), "file_size": 1000},
				map[string]any{"file_id": fmt.Sprintf("large-%d", id), "file_size": 90000},
			},
		}
		if caption != "" {
			extra["caption"] = caption
		}
		return telegramUpdateBody(id, 777, 777, "", extra)
	}
	calls := 0
	pages := [][]map[string]any{
		{telegramUpdateBody(299, 777, 777, "проверь фото", nil), photoUpdate(300, "отчёт с фото")},
		{photoUpdate(300, "отчёт с фото"), photoUpdate(301, ""), photoUpdate(302, "")},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			var updates []any
			if calls < len(pages) {
				for _, update := range pages[calls] {
					updates = append(updates, update)
				}
			}
			calls++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": updates})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	poller := newTelegramPoller()
	settings := map[string]string{"bot_token": "tok", "api_base": server.URL}
	ctx := context.Background()

	first, next, err := poller.Poll(ctx, server.Client(), settings, "299")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID != "299" || first[0].Text != "проверь фото" {
		t.Fatalf("the plain text must route immediately, got %+v", first)
	}
	if next != "300" {
		t.Fatalf("cursor must stay behind the buffered part, got %q", next)
	}

	second, next, err := poller.Poll(ctx, server.Client(), settings, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("album must arrive as one message, got %d: %+v", len(second), second)
	}
	album := second[0]
	if album.ID != "300" || album.Text != "отчёт с фото" {
		t.Fatalf("unexpected album: %+v", album)
	}
	if len(album.Files) != 3 || album.Files[0].Ref != "large-300" || album.Files[2].Ref != "large-302" {
		t.Fatalf("attachments not merged: %+v", album.Files)
	}
	if next != "303" {
		t.Fatalf("cursor after the flush = %q, want 303", next)
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
