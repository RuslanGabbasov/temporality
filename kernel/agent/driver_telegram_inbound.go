package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// telegramPoller adapts the Telegram Bot API to the ChannelPoller contract:
// getUpdates long-poll with an offset cursor, getMe for the bot's own
// identity, getFile + download for attachments (docs/inbound-messages.md §12
// phase 3). Chat id is the identity: for private chats chat.id == from.id, so
// the closed-world check matches the registered address and sender directly;
// in groups the registered SenderID is the member's own telegram id.
type telegramPoller struct{}

// telegramPollTimeout is the server-side long-poll window of getUpdates:
// shorter than the loop's cycle budget so one poll returns within it.
const telegramPollTimeout = 25 * time.Second

func (telegramPoller) Poll(ctx context.Context, client *http.Client, settings map[string]string, cursor string) ([]InboundMessage, string, error) {
	base, token, err := telegramCredentials(settings)
	if err != nil {
		return nil, "", err
	}
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates?timeout=%d", base, token, int(telegramPollTimeout.Seconds()))
	if cursor != "" {
		// The offset acknowledges every update below it: a persisted cursor
		// crossed with at-least-once redelivery cannot reprocess a message.
		endpoint += "&offset=" + url.QueryEscape(cursor)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		// 409 Conflict: another getUpdates consumer or a set webhook owns the
		// stream — both are admin-fixable, the surfaced error is the diagnosis.
		return nil, "", fmt.Errorf("telegram getUpdates returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	updates, next, err := ParseTelegramUpdates(body, cursor)
	if err != nil {
		return nil, "", err
	}
	return telegramNormalized(updates), next, nil
}

func (telegramPoller) Self(ctx context.Context, client *http.Client, settings map[string]string) (string, error) {
	base, token, err := telegramCredentials(settings)
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("%s/bot%s/getMe", base, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("telegram getMe returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var decoded struct {
		Result struct {
			ID int64 `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", err
	}
	if decoded.Result.ID == 0 {
		return "", errors.New("no id in getMe response")
	}
	// The bot never receives its own messages through getUpdates (updates are
	// what was sent TO it), so the identity is a safeguard, not a filter.
	return "bot:" + strconv.FormatInt(decoded.Result.ID, 10), nil
}

// FetchFile resolves a file_id through getFile and downloads the payload from
// the bot's file endpoint.
func (telegramPoller) FetchFile(ctx context.Context, client *http.Client, settings map[string]string, file InboundFile) ([]byte, error) {
	base, token, err := telegramCredentials(settings)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", base, token, url.QueryEscape(file.Ref))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram getFile returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var decoded struct {
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	if decoded.Result.FilePath == "" {
		return nil, fmt.Errorf("no file_path for attachment %q", file.Name)
	}
	downloadURL := fmt.Sprintf("%s/file/bot%s/%s", base, token, decoded.Result.FilePath)
	download, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	fileResp, err := client.Do(download)
	if err != nil {
		return nil, err
	}
	defer fileResp.Body.Close()
	if fileResp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(fileResp.Body, 2048))
		return nil, fmt.Errorf("telegram file download returned %d: %s", fileResp.StatusCode, truncate(string(payload), 200))
	}
	payload, err := io.ReadAll(io.LimitReader(fileResp.Body, inboundFileCap+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > inboundFileCap {
		return nil, fmt.Errorf("attachment exceeds the %d-byte inbound cap", inboundFileCap)
	}
	return payload, nil
}

// telegramCredentials extracts the Bot API endpoint and token. api_base is
// optional (tests, a self-hosted Bot API server for bigger files); the
// default is the public Telegram Bot API.
func telegramCredentials(settings map[string]string) (base, token string, err error) {
	base = strings.TrimRight(setting(settings, "api_base"), "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	token = strings.TrimSpace(setting(settings, "bot_token"))
	if token == "" {
		return "", "", errors.New("telegram transport is not configured")
	}
	return base, token, nil
}

// TelegramUpdate is one message-bearing update in normalized form.
type TelegramUpdate struct {
	UpdateID  int64
	ChatID    int64
	SenderID  int64
	Text      string
	Files     []InboundFile
	Timestamp time.Time
}

// ParseTelegramUpdates extracts message updates from a getUpdates body and
// computes the next offset cursor (max update_id + 1; unchanged when the
// batch is empty). Non-message updates (callbacks, inline queries) and
// channel posts without a sender are skipped; edited messages are ignored —
// the original delivery already entered the pipeline.
func ParseTelegramUpdates(body []byte, previousCursor string) ([]TelegramUpdate, string, error) {
	var decoded struct {
		OK     bool `json:"ok"`
		Result []struct {
			UpdateID int64 `json:"update_id"`
			Message  *struct {
				Date int64 `json:"date"`
				From *struct {
					ID int64 `json:"id"`
				} `json:"from"`
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
				Text     string `json:"text"`
				Caption  string `json:"caption"`
				Document *struct {
					FileID   string `json:"file_id"`
					FileName string `json:"file_name"`
				} `json:"document"`
				Photo []struct {
					FileID   string `json:"file_id"`
					FileSize int64  `json:"file_size"`
				} `json:"photo"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, "", err
	}
	if !decoded.OK {
		return nil, "", errors.New("getUpdates responded ok=false")
	}
	updates := []TelegramUpdate{}
	next := previousCursor
	for _, result := range decoded.Result {
		if result.UpdateID+1 > parseTelegramOffset(next) {
			next = strconv.FormatInt(result.UpdateID+1, 10)
		}
		message := result.Message
		if message == nil || message.From == nil || message.Chat.ID == 0 {
			continue
		}
		entry := TelegramUpdate{
			UpdateID:  result.UpdateID,
			ChatID:    message.Chat.ID,
			SenderID:  message.From.ID,
			Text:      strings.TrimSpace(message.Text),
			Timestamp: time.Unix(message.Date, 0).UTC(),
		}
		if entry.Text == "" {
			entry.Text = strings.TrimSpace(message.Caption)
		}
		if message.Document != nil {
			name := strings.TrimSpace(message.Document.FileName)
			if name == "" {
				name = "attachment"
			}
			entry.Files = append(entry.Files, InboundFile{Ref: message.Document.FileID, Name: name})
		}
		if len(message.Photo) > 0 {
			// Photo arrives as every size variant; the last is the largest.
			sizes := append([]struct {
				FileID   string `json:"file_id"`
				FileSize int64  `json:"file_size"`
			}{}, message.Photo...)
			sort.SliceStable(sizes, func(i, j int) bool { return sizes[i].FileSize < sizes[j].FileSize })
			entry.Files = append(entry.Files, InboundFile{Ref: sizes[len(sizes)-1].FileID, Name: "photo.jpg"})
		}
		if entry.Text == "" && len(entry.Files) == 0 {
			continue // stickers, voice and other payloads stay out of MVP
		}
		updates = append(updates, entry)
	}
	return updates, next, nil
}

// parseTelegramOffset reads a cursor as an integer offset; "" and junk are 0.
func parseTelegramOffset(cursor string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(cursor), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// telegramNormalized converts parsed updates into the channel-neutral model.
func telegramNormalized(updates []TelegramUpdate) []InboundMessage {
	normalized := make([]InboundMessage, 0, len(updates))
	for _, update := range updates {
		normalized = append(normalized, InboundMessage{
			ID:        strconv.FormatInt(update.UpdateID, 10),
			Channel:   "telegram",
			ChatID:    strconv.FormatInt(update.ChatID, 10),
			Sender:    strconv.FormatInt(update.SenderID, 10),
			Text:      update.Text,
			Files:     update.Files,
			Timestamp: update.Timestamp,
		})
	}
	return normalized
}
