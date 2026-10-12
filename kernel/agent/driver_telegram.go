package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// telegramDriver delivers text and documents through the Telegram Bot API
// using the kernel's shared bot. Inbound replies are a future capability
// (docs/channels.md §11 phase 2: pull getUpdates with a persisted offset) —
// until then deliveries carry no reply hint and the answer happens in the UI.
type telegramDriver struct{}

func (telegramDriver) Type() string { return "telegram" }

// Poller: inbound telegram (getUpdates, persisted offset) is future work
// (docs/channels.md phase 2) — outbound-only today.
func (telegramDriver) Poller() ChannelPoller { return nil }

func (telegramDriver) Spec() ChannelSpec {
	return ChannelSpec{
		Label:             "Telegram",
		AddressHint:       "Telegram chat ID, e.g. 123456789 — the user must have started the bot",
		NotConfiguredHint: "Telegram bot token",
		Capabilities: ChannelCapabilities{
			Text:     true,
			FilesOut: true,
		},
		Settings: []SettingField{
			{Name: "bot_token", Label: "Bot token", Secret: true},
		},
	}
}

func (telegramDriver) IsConfigured(settings map[string]string) bool {
	return setting(settings, "bot_token") != ""
}

func (telegramDriver) SendText(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) error {
	botToken := setting(settings, "bot_token")
	chatID := address
	if botToken == "" || chatID == "" {
		return errors.New("telegram transport is not configured")
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	payload := map[string]any{"chat_id": chatID, "text": msg.Body, "disable_web_page_preview": true}
	body, status, err := postJSON(ctx, client, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("telegram returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// SendFile posts the document through the Bot API multipart endpoint; the
// note rides along as the caption.
func (telegramDriver) SendFile(ctx context.Context, client *http.Client, settings map[string]string, address string, file OutboundFile) error {
	botToken := setting(settings, "bot_token")
	chatID := address
	if botToken == "" || chatID == "" {
		return errors.New("telegram transport is not configured")
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
	part, err := writer.CreateFormFile("document", file.Name)
	if err != nil {
		return err
	}
	if _, err := part.Write(file.Payload); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", botToken), bytes.NewReader(form.Bytes()))
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
