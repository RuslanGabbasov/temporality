package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// telegramDriver delivers through the Telegram Bot API using the kernel's
// shared bot, and receives initiative messages and replies through getUpdates
// (docs/inbound-messages.md §12 phase 3). Chat id is the user's channel
// address; for private chats it doubles as the sender identity
// (chat.id == from.id), in groups the registered SenderID is the member's own
// telegram id.
//
// Outbound surface (docs/channels.md §3): plain text with Telegram-HTML
// formatting (tgformat.go), inline keyboards for interactive asks, rich
// messages, HTML widgets for schema forms (Bot API 10.4) and ephemeral
// drafts. Every extension degrades to the text baseline on transport
// errors — the ask itself never fails because a fancy surface did.
type telegramDriver struct{}

func (telegramDriver) Type() string { return "telegram" }

func (telegramDriver) Poller() ChannelPoller { return newTelegramPoller() }

// Webhook: telegram inbound is pull — getUpdates long-poll (driver_telegram_inbound.go).
func (telegramDriver) Webhook() ChannelWebhook { return nil }

func (telegramDriver) Spec() ChannelSpec {
	return ChannelSpec{
		Label:             "Telegram",
		AddressHint:       "Telegram chat ID, e.g. 123456789 — the user must have started the bot",
		NotConfiguredHint: "Telegram bot token",
		Capabilities: ChannelCapabilities{
			Text:        true,
			TwoWay:      true,
			FilesOut:    true,
			FilesIn:     true,
			Interactive: true,
			Rich:        true,
			Widgets:     true,
			Drafts:      true,
		},
		Settings: []SettingField{
			{Name: "bot_token", Label: "Bot token", Secret: true},
			{Name: "api_base", Label: "API base URL (optional; default https://api.telegram.org, set for a self-hosted Bot API server)"},
		},
	}
}

func (telegramDriver) IsConfigured(settings map[string]string) bool {
	return setting(settings, "bot_token") != ""
}

// telegramEndpoint resolves the Bot API method URL from the channel settings
// (api_base honours a self-hosted Bot API server, same as the poller).
func telegramEndpoint(settings map[string]string, method string) (string, error) {
	base, token, err := telegramCredentials(settings)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/bot%s/%s", base, token, method), nil
}

// telegramMarkup renders the interactive surface of one text delivery:
// the kernel's button rows (callback tokens verbatim) plus the hosted-form
// URL button when present. nil = plain message.
func telegramMarkup(msg OutboundText) map[string]any {
	rows := make([][]map[string]string, 0, len(msg.Buttons)+1)
	for _, row := range msg.Buttons {
		if len(row) == 0 {
			continue
		}
		buttons := make([]map[string]string, 0, len(row))
		for _, button := range row {
			buttons = append(buttons, map[string]string{"text": button.Text, "callback_data": button.Token})
		}
		rows = append(rows, buttons)
	}
	// A schema ask carries the hosted form (docs/interactive-HITL.md §6.2):
	// the URL button under the message; the link also stays in the body for
	// clients that render keyboards poorly.
	if msg.FormURL != "" {
		rows = append(rows, []map[string]string{
			{"text": ChannelText(msg.Lang, "ask_form_button"), "url": msg.FormURL},
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return map[string]any{"inline_keyboard": rows}
}

// telegramParseRejected reports a Bot API 400 whose cause is the HTML we
// sent: the transport cannot parse it, the caller retries as plain text.
func telegramParseRejected(status int, body string) bool {
	return status == http.StatusBadRequest && strings.Contains(strings.ToLower(body), "can't parse entities")
}

// telegramSendOne posts one text payload (parse_mode=HTML) and degrades to
// plain text when the parser rejects the markup. Returns the posted
// message's id when the response carries one.
func telegramSendOne(ctx context.Context, client *http.Client, settings map[string]string, chatID string, text string, markup map[string]any) (string, error) {
	endpoint, err := telegramEndpoint(settings, "sendMessage")
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"chat_id":               chatID,
		"text":                  text,
		"link_preview_options":  map[string]any{"is_disabled": true},
		"parse_mode":            "HTML",
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	body, status, err := postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return "", err
	}
	if telegramParseRejected(status, body) {
		// The rendered markup hit an unsupported construct: retry the same
		// text without parse_mode — delivery beats formatting.
		delete(payload, "parse_mode")
		payload["text"] = telegramStripTags(text)
		body, status, err = postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
		if err != nil {
			return "", err
		}
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("telegram returned %d: %s", status, truncate(body, 200))
	}
	return telegramMessageID(body), nil
}

// telegramStripTags reduces a rendered fragment to its text — the plain-text
// degradation path.
func telegramStripTags(html string) string {
	var b strings.Builder
	for i := 0; i < len(html); {
		if html[i] != '<' {
			b.WriteByte(html[i])
			i++
			continue
		}
		end := strings.IndexByte(html[i:], '>')
		if end < 0 {
			break
		}
		i += end + 1
	}
	return html_unescapeEntities(b.String())
}

// html_unescapeEntities restores the three named entities the formatter
// escapes; everything else arrived escaped text already.
func html_unescapeEntities(s string) string {
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&", "&quot;", `"`, "&#39;", "'")
	return r.Replace(s)
}

// telegramMessageID extracts result.message_id from a Bot API response body.
func telegramMessageID(body string) string {
	var decoded struct {
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(body), &decoded) != nil || decoded.Result.MessageID == 0 {
		return ""
	}
	return strconv.FormatInt(decoded.Result.MessageID, 10)
}

// SendText delivers one rendered notification: Telegram-HTML markup, split
// across messages when the body exceeds the 4096-character limit; the
// interactive markup rides the first chunk only.
func (telegramDriver) SendText(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) error {
	chatID := address
	if _, _, credErr := telegramCredentials(settings); credErr != nil || chatID == "" {
		return errors.New("telegram transport is not configured")
	}
	markup := telegramMarkup(msg)
	chunks := telegramChunks(msg.Body)
	if len(chunks) == 0 {
		chunks = []string{msg.Body}
	}
	var postedID string
	var lastErr error
	for i, chunk := range chunks {
		// Buttons and the form link belong to the first chunk; the rest are
		// continuation reads. The markdown renders into the Telegram HTML
		// subset here — the wire always carries rendered fragments.
		text := telegramHTML(chunk)
		if text == "" {
			text = chunk
		}
		rowMarkup := markup
		if i > 0 {
			rowMarkup = nil
		}
		id, err := telegramSendOne(ctx, client, settings, chatID, text, rowMarkup)
		if err != nil {
			lastErr = err
			break
		}
		if id != "" {
			postedID = id
		}
	}
	_ = postedID // reported through the rich/threaded extensions; text sends stay fire-and-forget
	return lastErr
}

// SendTextRich delivers the body as a rich message (Bot API sendRichMessage):
// headings, tables, details and in-text buttons render natively. The
// markdown field carries the channel-neutral body verbatim — Telegram's rich
// markdown is GFM-compatible, so no re-render is needed. The posted message's
// id rides back for the kernel's resolve-in-place edits.
func (telegramDriver) SendTextRich(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) (string, error) {
	chatID := address
	if _, _, credErr := telegramCredentials(settings); credErr != nil || chatID == "" {
		return "", errors.New("telegram transport is not configured")
	}
	endpoint, err := telegramEndpoint(settings, "sendRichMessage")
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"chat_id":       chatID,
		"rich_message":  map[string]any{"markdown": msg.Body, "skip_entity_detection": true},
	}
	if markup := telegramMarkup(msg); markup != nil {
		payload["reply_markup"] = markup
	}
	body, status, err := postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("telegram returned %d: %s", status, truncate(body, 200))
	}
	return telegramMessageID(body), nil
}

// AnswerCallback acknowledges a button press (answerCallbackQuery): the tap
// receipt the user sees while the run resumes in the background.
func (telegramDriver) AnswerCallback(ctx context.Context, client *http.Client, settings map[string]string, queryID, text string) error {
	endpoint, err := telegramEndpoint(settings, "answerCallbackQuery")
	if err != nil {
		return err
	}
	payload := map[string]any{"callback_query_id": queryID}
	if strings.TrimSpace(text) != "" {
		payload["text"] = truncate(text, 200)
	}
	body, status, err := postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("telegram returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// EditMessage rewrites a posted message in place (editMessageText): asks
// resolve visually — buttons out, verdict line in. Parse failures degrade to
// plain text like SendText.
func (telegramDriver) EditMessage(ctx context.Context, client *http.Client, settings map[string]string, address, messageID, text string, buttons [][]OutboundButton) error {
	chatID := address
	if _, _, credErr := telegramCredentials(settings); credErr != nil || chatID == "" || messageID == "" {
		return errors.New("telegram transport is not configured")
	}
	endpoint, err := telegramEndpoint(settings, "editMessageText")
	if err != nil {
		return err
	}
	payload := map[string]any{"chat_id": chatID, "message_id": messageID, "text": text, "parse_mode": "HTML"}
	if markup := telegramMarkup(OutboundText{Buttons: buttons, Lang: ""}); markup != nil {
		payload["reply_markup"] = markup
	}
	body, status, err := postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return err
	}
	if telegramParseRejected(status, body) {
		delete(payload, "parse_mode")
		payload["text"] = telegramStripTags(text)
		body, status, err = postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
		if err != nil {
			return err
		}
	}
	if status != http.StatusOK {
		return fmt.Errorf("telegram returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// SendWidget renders the neutral widget model into an HTML widget and posts
// it (sendWidget). Queries are enabled so the form can talk back; the
// hosted-form fallback button rides along when the kernel has one. Returns
// the posted message's id — the kernel pins it to the ask.
func (telegramDriver) SendWidget(ctx context.Context, client *http.Client, settings map[string]string, address string, w OutboundWidget) (string, error) {
	chatID := address
	if _, _, credErr := telegramCredentials(settings); credErr != nil || chatID == "" {
		return "", errors.New("telegram transport is not configured")
	}
	model, err := ParseWidgetModel(string(w.Model))
	if err != nil {
		return "", err
	}
	chatData, err := model.Encode()
	if err != nil {
		return "", err // over the data cap: the caller falls back to the hosted form
	}
	html, err := telegramWidgetHTML(model, w.FormURL, w.Lang)
	if err != nil {
		return "", err
	}
	endpoint, err := telegramEndpoint(settings, "sendWidget")
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"chat_id":       chatID,
		"html":          html,
		"chat_data":     chatData,
		"allow_queries": true,
	}
	if w.FormURL != "" {
		payload["reply_markup"] = map[string]any{
			"inline_keyboard": [][]map[string]string{{
				{"text": ChannelText(w.Lang, "ask_form_button"), "url": w.FormURL},
			}},
		}
	}
	body, status, err := postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("telegram returned %d: %s", status, truncate(body, 200))
	}
	messageID := telegramMessageID(body)
	if messageID == "" {
		return "", errors.New("sendWidget returned no message id")
	}
	return messageID, nil
}

// EditWidgetData pushes an updated widget model into a posted widget
// (editMessageWidgetData): the state channel the widget re-renders from.
func (telegramDriver) EditWidgetData(ctx context.Context, client *http.Client, settings map[string]string, address, messageID, model string) error {
	endpoint, err := telegramEndpoint(settings, "editMessageWidgetData")
	if err != nil {
		return err
	}
	payload := map[string]any{"chat_id": address, "message_id": messageID, "chat_data": model}
	body, status, err := postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("telegram returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// AnswerWidget acknowledges one widget query inside the transport's answer
// window; data reaches that widget instance only (Bot API 10.4).
func (telegramDriver) AnswerWidget(ctx context.Context, client *http.Client, settings map[string]string, queryID, data string) error {
	endpoint, err := telegramEndpoint(settings, "answerWidgetQuery")
	if err != nil {
		return err
	}
	payload := map[string]any{"widget_query_id": queryID}
	if strings.TrimSpace(data) != "" {
		payload["data"] = truncate(data, 1024)
	}
	body, status, err := postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("telegram returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// SendDraft streams an ephemeral preview of a message being generated
// (sendMessageDraft): the preview lives ~30 seconds and is replaced by the
// real delivery. can_stop stays off — stopping a run is a deliberate product
// surface, not a side effect of watching progress.
func (telegramDriver) SendDraft(ctx context.Context, client *http.Client, settings map[string]string, address string, draftID int64, text string) error {
	if draftID == 0 {
		return errors.New("telegram draft id must be non-zero")
	}
	endpoint, err := telegramEndpoint(settings, "sendMessageDraft")
	if err != nil {
		return err
	}
	payload := map[string]any{
		"chat_id":    address,
		"draft_id":   draftID,
		"text":       truncate(text, 4096),
		"parse_mode": "HTML",
	}
	body, status, err := postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return err
	}
	if telegramParseRejected(status, body) {
		delete(payload, "parse_mode")
		_, status, err = postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
		if err != nil {
			return err
		}
	}
	if status != http.StatusOK {
		return fmt.Errorf("telegram returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// SendFile posts the attachment through the Bot API multipart endpoint; the
// note rides along as the caption. Image payloads go through sendPhoto so
// Telegram shows them inline — sendDocument renders as a bare file card
// (docs/presentation-engine.md §9.3 Telegram row: photos, not documents).
func (telegramDriver) SendFile(ctx context.Context, client *http.Client, settings map[string]string, address string, file OutboundFile) error {
	chatID := address
	if _, _, credErr := telegramCredentials(settings); credErr != nil || chatID == "" {
		return errors.New("telegram transport is not configured")
	}
	method, field := "sendDocument", "document"
	if _, _, isImage := imageDimensions(file.Name, file.Payload); isImage {
		method, field = "sendPhoto", "photo"
	}
	endpoint, err := telegramEndpoint(settings, method)
	if err != nil {
		return err
	}
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	if err := writer.WriteField("chat_id", chatID); err != nil {
		return err
	}
	if strings.TrimSpace(file.Note) != "" {
		if err := writer.WriteField("caption", file.Note); err != nil {
			return err
		}
	}
	part, err := writer.CreateFormFile(field, file.Name)
	if err != nil {
		return err
	}
	if _, err := part.Write(file.Payload); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(form.Bytes()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}
