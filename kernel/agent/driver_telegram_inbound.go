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
	"sync"
	"time"
)

// telegramPoller adapts the Telegram Bot API to the ChannelPoller contract:
// getUpdates long-poll with an offset cursor, getMe for the bot's own
// identity, getFile + download for attachments (docs/inbound-messages.md §12
// phase 3). Chat id is the identity: for private chats chat.id == from.id, so
// the closed-world check matches the registered address and sender directly;
// in groups the registered SenderID is the member's own telegram id.
// Media groups (albums) pass through an internal buffer: parts arrive as
// separate updates and must reach the pipeline as one message.
type telegramPoller struct {
	albums *telegramAlbumBuffer
}

func newTelegramPoller() *telegramPoller {
	return &telegramPoller{albums: newTelegramAlbumBuffer()}
}

// telegramPollTimeout is the server-side long-poll window of getUpdates:
// shorter than the loop's cycle budget so one poll returns within it.
const telegramPollTimeout = 25 * time.Second

// telegramAlbumFlushDelay waits out the tail of a media group (album): its
// parts trickle in as separate updates — often split across getUpdates
// responses — and each part alone would start its own intake run. The buffer
// releases the whole group as one message. Vars so tests can shrink them.
var (
	telegramAlbumFlushDelay = 2500 * time.Millisecond
	telegramAlbumPollStep   = 200 * time.Millisecond
)

func (p *telegramPoller) Poll(ctx context.Context, client *http.Client, settings map[string]string, cursor string) ([]InboundMessage, string, error) {
	base, token, err := telegramCredentials(settings)
	if err != nil {
		return nil, "", err
	}
	if p.albums == nil {
		p.albums = newTelegramAlbumBuffer()
	}
	consumed := parseTelegramOffset(cursor)
	for {
		// While an album is buffering the long poll is replaced by short polls:
		// the group must complete within the flush window, not after the next
		// 25-second quiet poll.
		timeout := int(telegramPollTimeout.Seconds())
		if p.albums.pending() {
			timeout = 0
		}
		updates, batchNext, err := p.fetchUpdates(ctx, client, base, token, cursor, timeout)
		if err != nil {
			return nil, "", err
		}
		now := time.Now()
		var out []InboundMessage
		for _, update := range updates {
			if p.albums.contains(update.UpdateID) {
				continue // replayed while buffering — the part is already parked
			}
			if update.MediaGroupID != "" && len(update.Files) > 0 {
				p.albums.add(update, now)
				continue
			}
			out = append(out, telegramNormalizedOne(update))
		}
		flushed, flushedMax := p.albums.flushDue(now)
		out = append(out, flushed...)
		// batchNext already moved past updates the parser skipped (stickers and
		// other unsupported payloads) — the cursor may not stall on them.
		if parsed := parseTelegramOffset(batchNext); parsed > consumed {
			consumed = parsed
		}
		if flushedMax+1 > consumed {
			consumed = flushedMax + 1
		}
		next := ""
		if consumed > 0 {
			next = strconv.FormatInt(consumed, 10)
		}
		// The cursor stays behind buffered parts on purpose: a crash must
		// redeliver them (at-least-once), never silently drop an album.
		if replay, ok := p.albums.replayID(); ok && replay < consumed {
			next = strconv.FormatInt(replay, 10)
		}
		if len(out) > 0 {
			return out, next, nil
		}
		if p.albums.pending() {
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(telegramAlbumPollStep):
			}
			continue
		}
		return nil, next, nil
	}
}

// fetchUpdates performs one getUpdates call and returns the parsed updates
// plus the parser's next-offset candidate (max update_id + 1 vs the cursor).
func (p *telegramPoller) fetchUpdates(ctx context.Context, client *http.Client, base, token string, offset string, timeoutSeconds int) ([]TelegramUpdate, string, error) {
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates?timeout=%d", base, token, timeoutSeconds)
	if offset != "" {
		// The offset acknowledges every update below it: a persisted cursor
		// crossed with at-least-once redelivery cannot reprocess a message.
		endpoint += "&offset=" + url.QueryEscape(offset)
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
	return ParseTelegramUpdates(body, offset)
}

func (p *telegramPoller) Self(ctx context.Context, client *http.Client, settings map[string]string) (string, error) {
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
	UpdateID     int64
	ChatID       int64
	SenderID     int64
	Text         string
	Files        []InboundFile
	Timestamp    time.Time
	MediaGroupID string // albums: parts share one media_group_id
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
				MediaGroup string `json:"media_group_id"`
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
			UpdateID:     result.UpdateID,
			ChatID:       message.Chat.ID,
			SenderID:     message.From.ID,
			Text:         strings.TrimSpace(message.Text),
			Timestamp:    time.Unix(message.Date, 0).UTC(),
			MediaGroupID: strings.TrimSpace(message.MediaGroup),
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
		normalized = append(normalized, telegramNormalizedOne(update))
	}
	return normalized
}

func telegramNormalizedOne(update TelegramUpdate) InboundMessage {
	return InboundMessage{
		ID:        strconv.FormatInt(update.UpdateID, 10),
		Channel:   "telegram",
		ChatID:    strconv.FormatInt(update.ChatID, 10),
		Sender:    strconv.FormatInt(update.SenderID, 10),
		Text:      update.Text,
		Files:     update.Files,
		Timestamp: update.Timestamp,
	}
}

// telegramAlbumBuffer parks media-group parts between polls: an album's parts
// arrive as separate updates (sometimes split across getUpdates responses),
// and the pipeline must see one message, not one intake run per photo.
type telegramAlbumBuffer struct {
	mu     sync.Mutex
	groups map[string]*telegramAlbum
}

type telegramAlbum struct {
	parts []TelegramUpdate // ascending by UpdateID
	first time.Time        // when the first part was buffered
}

func newTelegramAlbumBuffer() *telegramAlbumBuffer {
	return &telegramAlbumBuffer{groups: map[string]*telegramAlbum{}}
}

func (b *telegramAlbumBuffer) key(update TelegramUpdate) string {
	return strconv.FormatInt(update.ChatID, 10) + ":" + update.MediaGroupID
}

func (b *telegramAlbumBuffer) add(update TelegramUpdate, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := b.key(update)
	album := b.groups[key]
	if album == nil {
		album = &telegramAlbum{first: now}
		b.groups[key] = album
	}
	album.parts = append(album.parts, update)
	sort.SliceStable(album.parts, func(i, j int) bool { return album.parts[i].UpdateID < album.parts[j].UpdateID })
}

func (b *telegramAlbumBuffer) contains(updateID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, album := range b.groups {
		for _, part := range album.parts {
			if part.UpdateID == updateID {
				return true
			}
		}
	}
	return false
}

func (b *telegramAlbumBuffer) pending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.groups) > 0
}

// replayID returns the smallest update_id still buffered: a cursor at or
// below it makes Telegram redeliver every buffered part after a crash, so the
// buffer never becomes a black hole (at-least-once).
func (b *telegramAlbumBuffer) replayID() (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	replay, found := int64(0), false
	for _, album := range b.groups {
		for _, part := range album.parts {
			if !found || part.UpdateID < replay {
				replay, found = part.UpdateID, true
			}
		}
	}
	return replay, found
}

// flushDue releases groups whose first part outlived the flush delay. Each
// group becomes one message: id and timestamp of the first part, the caption
// of whichever part carries one, every attachment in update order. Returns
// the messages and the highest released update id (for the cursor).
func (b *telegramAlbumBuffer) flushDue(now time.Time) ([]InboundMessage, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var messages []InboundMessage
	maxID := int64(0)
	for key, album := range b.groups {
		if now.Sub(album.first) < telegramAlbumFlushDelay {
			continue
		}
		delete(b.groups, key)
		first := album.parts[0]
		entry := InboundMessage{
			ID:        strconv.FormatInt(first.UpdateID, 10),
			Channel:   "telegram",
			ChatID:    strconv.FormatInt(first.ChatID, 10),
			Sender:    strconv.FormatInt(first.SenderID, 10),
			Timestamp: first.Timestamp,
		}
		for _, part := range album.parts {
			if entry.Text == "" {
				entry.Text = part.Text
			}
			entry.Files = append(entry.Files, part.Files...)
			if part.UpdateID > maxID {
				maxID = part.UpdateID
			}
		}
		messages = append(messages, entry)
	}
	return messages, maxID
}
