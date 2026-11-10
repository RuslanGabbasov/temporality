package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// telegramCall is one recorded Bot API request.
type telegramCall struct {
	path    string
	payload map[string]any
}

// telegramRecorder stands in for the Bot API and records every JSON call.
func telegramRecorder(t *testing.T, respond func(call int) string) (*[]telegramCall, *http.Client) {
	t.Helper()
	calls := &[]telegramCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		*calls = append(*calls, telegramCall{path: r.URL.Path, payload: payload})
		w.Write([]byte(respond(len(*calls))))
	}))
	t.Cleanup(server.Close)
	return calls, &http.Client{Transport: roundTripperTo(strings.TrimPrefix(server.URL, "http://"))}
}

// TestTelegramSendTextHTMLAndMarkup pins the Phase-0/1 wire format: HTML
// parse_mode, option buttons as callback rows with the cancel row, and the
// hosted-form URL button last.
func TestTelegramSendTextHTMLAndMarkup(t *testing.T) {
	calls, client := telegramRecorder(t, func(int) string {
		return `{"ok":true,"result":{"message_id":42}}`
	})
	driver := telegramDriver{}
	msg := OutboundText{
		Body:    "## Вопрос\n\nВыбери вариант",
		Buttons: [][]OutboundButton{{{Text: "canary", Token: "a:op1:0"}, {Text: "stable", Token: "a:op1:1"}}},
		FormURL: "https://ui.example/forms/tok",
		Lang:    "en",
	}
	require.NoError(t, driver.SendText(context.Background(), client, map[string]string{"bot_token": "tok"}, "123", msg))

	require.Len(t, *calls, 1)
	payload := (*calls)[0].payload
	require.Equal(t, "123", payload["chat_id"])
	require.Equal(t, "HTML", payload["parse_mode"])
	text, _ := payload["text"].(string)
	require.Contains(t, text, "<b>Вопрос</b>")
	markup, _ := payload["reply_markup"].(map[string]any)
	require.NotNil(t, markup)
	rows, _ := markup["inline_keyboard"].([]any)
	require.Len(t, rows, 2) // the kernel's button row + the hosted-form URL row
}

// TestTelegramSendTextParseFallback pins the degradation contract: a markup
// rejection (Bot API 400 "can't parse entities") retries the same text plain.
func TestTelegramSendTextParseFallback(t *testing.T) {
	var attempts int
	var firstText, retryText any
	var firstHasParseMode, retryHasParseMode bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		attempts++
		if attempts == 1 {
			firstText, firstHasParseMode = body["text"], body["parse_mode"] != nil
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"description":"Bad Request: can't parse entities"}`))
			return
		}
		retryText, retryHasParseMode = body["text"], body["parse_mode"] != nil
		w.Write([]byte(`{"ok":true,"result":{"message_id":7}}`))
	}))
	t.Cleanup(server.Close)
	client := &http.Client{Transport: roundTripperTo(strings.TrimPrefix(server.URL, "http://"))}
	driver := telegramDriver{}
	require.NoError(t, driver.SendText(context.Background(), client,
		map[string]string{"bot_token": "tok"}, "123", OutboundText{Body: "просто текст"}))
	require.Equal(t, 2, attempts, "must retry without parse_mode")
	require.True(t, firstHasParseMode)
	require.False(t, retryHasParseMode, "retry must not carry parse_mode")
	require.Equal(t, firstText, retryText)
}

// TestTelegramSendWidgetPayload pins the Phase-3 wire format: allow_queries,
// the state in chat_data, HTML within the transport caps, the message id back.
func TestTelegramSendWidgetPayload(t *testing.T) {
	calls, client := telegramRecorder(t, func(int) string {
		return `{"ok":true,"result":{"message_id":77}}`
	})
	driver := telegramDriver{}
	model, err := BuildWidgetModel("Deploy?", json.RawMessage(widgetTestSchema), WidgetState{Values: map[string]any{}})
	require.NoError(t, err)
	encoded, err := model.Encode()
	require.NoError(t, err)

	id, err := driver.SendWidget(context.Background(), client,
		map[string]string{"bot_token": "tok"}, "123",
		OutboundWidget{Model: json.RawMessage(encoded), FormURL: "https://ui.example/forms/x", Lang: "ru"})
	require.NoError(t, err)
	require.Equal(t, "77", id)

	call := (*calls)[0]
	require.Contains(t, call.path, "/sendWidget")
	require.Equal(t, true, call.payload["allow_queries"])
	chatData, _ := call.payload["chat_data"].(string)
	require.Contains(t, chatData, "canary")
	html, _ := call.payload["html"].(string)
	require.NotEmpty(t, html)
	require.LessOrEqual(t, len(html), 32768)
	require.Contains(t, html, "TelegramWidget.send")
	markup, _ := call.payload["reply_markup"].(map[string]any)
	require.NotNil(t, markup, "form URL button must ride the widget")
}

// TestTelegramSendWidgetRefusesOversizedModel: an over-cap model fails
// before any HTTP call — the delivery degrades to the hosted form.
func TestTelegramSendWidgetRefusesOversizedModel(t *testing.T) {
	calls, client := telegramRecorder(t, func(int) string {
		return `{"ok":true,"result":{"message_id":1}}`
	})
	huge := strings.Repeat("x", WidgetDataLimit+10)
	driver := telegramDriver{}
	_, err := driver.SendWidget(context.Background(), client,
		map[string]string{"bot_token": "tok"}, "123",
		OutboundWidget{Model: json.RawMessage(`{"q":"` + huge + `","f":[],"s":{}}`)})
	require.Error(t, err)
	require.Empty(t, *calls, "no HTTP call may happen for an over-cap model")
}

// TestTelegramInteractiveMethods pins the ack/edit/draft wire calls.
func TestTelegramInteractiveMethods(t *testing.T) {
	calls, client := telegramRecorder(t, func(int) string {
		return `{"ok":true,"result":{}}`
	})
	driver := telegramDriver{}
	settings := map[string]string{"bot_token": "tok"}

	require.NoError(t, driver.AnswerCallback(context.Background(), client, settings, "cbq1", "Answer accepted"))
	require.Contains(t, (*calls)[0].path, "/answerCallbackQuery")
	require.Equal(t, "cbq1", (*calls)[0].payload["callback_query_id"])

	require.NoError(t, driver.AnswerWidget(context.Background(), client, settings, "wq1", "ok"))
	require.Contains(t, (*calls)[1].path, "/answerWidgetQuery")
	require.Equal(t, "wq1", (*calls)[1].payload["widget_query_id"])
	require.Equal(t, "ok", (*calls)[1].payload["data"])

	require.NoError(t, driver.EditWidgetData(context.Background(), client, settings, "123", "55", `{"q":"x","f":[],"s":{}}`))
	require.Contains(t, (*calls)[2].path, "/editMessageWidgetData")
	require.Equal(t, "55", (*calls)[2].payload["message_id"])

	require.NoError(t, driver.EditMessage(context.Background(), client, settings, "123", "55", "✅ Answered", nil))
	require.Contains(t, (*calls)[3].path, "/editMessageText")
	require.Equal(t, "HTML", (*calls)[3].payload["parse_mode"])

	require.NoError(t, driver.SendDraft(context.Background(), client, settings, "123", 4242, "пишу ответ…"))
	require.Contains(t, (*calls)[4].path, "/sendMessageDraft")
	require.Equal(t, float64(4242), (*calls)[4].payload["draft_id"])

	// Rich messages carry the markdown verbatim and report the message id.
	calls2, client2 := telegramRecorder(t, func(int) string {
		return `{"ok":true,"result":{"message_id":91}}`
	})
	id, err := driver.SendTextRich(context.Background(), client2, settings, "123",
		OutboundText{Body: "## Итог\n\n| a | b |\n|---|---|\n| 1 | 2 |"})
	require.NoError(t, err)
	require.Equal(t, "91", id)
	require.Contains(t, (*calls2)[0].path, "/sendRichMessage")
	rich, _ := (*calls2)[0].payload["rich_message"].(map[string]any)
	require.Contains(t, rich["markdown"], "## Итог")
}

// TestAskButtons pins the token contract: option indexes, cancel row, and the
// 64-byte degradation.
func TestAskButtons(t *testing.T) {
	rows := askButtons("op1", []string{"canary", "stable"}, "en")
	require.Len(t, rows, 2)
	require.Equal(t, []OutboundButton{{Text: "canary", Token: "a:op1:0"}, {Text: "stable", Token: "a:op1:1"}}, rows[0])
	require.Equal(t, OutboundButton{Text: "Cancel", Token: "a:op1:x"}, rows[1][0])

	// An overlong operation id drops the whole surface: the buttons would be
	// undeliverable (callback_data ≤64 bytes) and must not lie silently.
	require.Nil(t, askButtons(strings.Repeat("x", 100), []string{"yes"}, "en"))
	// No options — nothing to button.
	require.Nil(t, askButtons("op", nil, "en"))
}
