package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// ReadMarker is the optional driver extension for marking an inbound message
// as read (matrix m.read receipts): the sender sees the dialog's read mark
// the moment the message enters the pipeline, not when the answer lands.
type ReadMarker interface {
	// MarkRead posts a read receipt for eventID in the room at address, as
	// the transport's own account. Best-effort by contract: callers log
	// failures and move on.
	MarkRead(ctx context.Context, client *http.Client, settings map[string]string, address, eventID string) error
}

// matrixMarkdown renders agent answers for Matrix formatted_body: GFM
// extensions for tables and task lists, hard wraps to keep chat-style single
// newlines visible. Raw HTML stays escaped (no Unsafe): model output must not
// inject markup into the room.
var matrixMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithHardWraps()),
)

// matrixFormattedHTML converts markdown to the HTML fragment for
// formatted_body; an empty result (nothing to convert) keeps the message
// plain — the unformatted body always ships as the fallback.
func matrixFormattedHTML(markdown string) string {
	var buf bytes.Buffer
	if err := matrixMarkdown.Convert([]byte(markdown), &buf); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

// DMResolver is the optional driver extension for transports that can locate
// a user's direct conversation from their own identity: the matrix bot finds
// the DM room it shares with the user's mxid, so a channel row does not have
// to pin the room id — leaving the address empty and registering the mxid is
// enough (docs/channels.md).
type DMResolver interface {
	// ResolveDM returns the room id of the user's direct conversation, or an
	// error when none can be found. senderID is the user's own identity in
	// this channel (mxid for matrix).
	ResolveDM(ctx context.Context, client *http.Client, settings map[string]string, senderID string) (string, error)
}

// matrixDriver delivers through the Matrix client-server API using the
// kernel's shared bot account: text messages with a stable txn id
// (homeservers deduplicate retries server-side) and media uploads. The
// channel is two-way — replies in the room route back into the paused run
// (matrixinbound.go) — so text deliveries carry the reply hint.
type matrixDriver struct{}

func (matrixDriver) Type() string { return "matrix" }

// Poller: matrix is two-way — /sync long-poll with reply routing back into
// paused runs (driver_matrix_inbound.go).
func (matrixDriver) Poller() ChannelPoller { return matrixPoller{} }

// Webhook: matrix inbound is pull — /sync long-poll (driver_matrix_inbound.go).
func (matrixDriver) Webhook() ChannelWebhook { return nil }

func (matrixDriver) Spec() ChannelSpec {
	return ChannelSpec{
		Label:             "Matrix",
		AddressHint:       "Matrix room ID, e.g. !room:matrix.org — optional: leave empty to use your direct (DM) room with the bot; the bot must share it or be able to join it",
		NotConfiguredHint: "Matrix homeserver + bot access token",
		Capabilities: ChannelCapabilities{
			Text:     true,
			TwoWay:   true,
			FilesOut: true,
			FilesIn:  true,
		},
		Settings: []SettingField{
			{Name: "homeserver", Label: "Homeserver URL"},
			{Name: "access_token", Label: "Bot access token", Secret: true},
		},
	}
}

func (matrixDriver) IsConfigured(settings map[string]string) bool {
	return setting(settings, "homeserver") != "" && setting(settings, "access_token") != ""
}

func (matrixDriver) SendText(ctx context.Context, client *http.Client, settings map[string]string, address string, msg OutboundText) error {
	homeserver := strings.TrimRight(setting(settings, "homeserver"), "/")
	// TrimBearerPrefix guards against a token saved with its "Bearer "
	// header prefix (database or env) — homeservers reject the doubled prefix.
	accessToken := TrimBearerPrefix(setting(settings, "access_token"))
	roomID := address
	if homeserver == "" || accessToken == "" || roomID == "" {
		return errors.New("matrix transport is not configured")
	}
	// The transport is two-way (matrixinbound.go): replying in the room routes
	// the answer back into the paused run, so say so next to the question.
	body := msg.Body + "\n\nReply in this room to answer."
	txnID := "temporality-" + transactionFingerprint(roomID, body)
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s", homeserver, url.PathEscape(roomID), txnID)
	payload := map[string]any{"msgtype": "m.text", "body": body}
	// Agent answers are markdown; render them for HTML-capable clients
	// (Element) via the custom format. The plain body stays the fallback for
	// clients that ignore formatted_body.
	if html := matrixFormattedHTML(body); html != "" {
		payload["format"] = "org.matrix.custom.html"
		payload["formatted_body"] = html
	}
	body2, status, err := postJSON(ctx, client, http.MethodPut, endpoint, payload, map[string]string{"Authorization": "Bearer " + accessToken})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("matrix returned %d: %s", status, truncate(body2, 200))
	}
	return nil
}

// SendFile uploads the bytes to the homeserver media store, then sends an
// m.file room message referencing the returned content URI. The note, when
// present, rides along as the message body so the recipient gets context.
func (matrixDriver) SendFile(ctx context.Context, client *http.Client, settings map[string]string, address string, file OutboundFile) error {
	homeserver := strings.TrimRight(setting(settings, "homeserver"), "/")
	accessToken := TrimBearerPrefix(setting(settings, "access_token"))
	roomID := address
	if homeserver == "" || accessToken == "" || roomID == "" {
		return errors.New("matrix transport is not configured")
	}
	uploadURL := fmt.Sprintf("%s/_matrix/media/v3/upload?filename=%s", homeserver, url.QueryEscape(file.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, strings.NewReader(string(file.Payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", fileContentType(file.Name))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("matrix upload returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var uploaded struct {
		ContentURI string `json:"content_uri"`
	}
	if err := json.Unmarshal(body, &uploaded); err != nil || uploaded.ContentURI == "" {
		return errors.New("matrix upload returned no content_uri")
	}
	messageBody := file.Note
	if strings.TrimSpace(messageBody) == "" {
		messageBody = file.Name
	}
	txnID := "temporality-file-" + transactionFingerprint(roomID, file.Name+uploaded.ContentURI)
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s", homeserver, url.PathEscape(roomID), txnID)
	message := map[string]any{
		"msgtype":  "m.file",
		"body":     messageBody,
		"filename": file.Name,
		"url":      uploaded.ContentURI,
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	sendReq, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(string(encoded)))
	if err != nil {
		return err
	}
	sendReq.Header.Set("Authorization", "Bearer "+accessToken)
	sendReq.Header.Set("Content-Type", "application/json")
	sendResp, err := client.Do(sendReq)
	if err != nil {
		return err
	}
	defer sendResp.Body.Close()
	sendBody, _ := io.ReadAll(io.LimitReader(sendResp.Body, 4096))
	if sendResp.StatusCode != http.StatusOK {
		return fmt.Errorf("matrix returned %d: %s", sendResp.StatusCode, truncate(string(sendBody), 200))
	}
	return nil
}

// transactionFingerprint derives a stable-but-unique Matrix txn id so retries
// of the same notification deduplicate server-side.
func transactionFingerprint(roomID, text string) string {
	digest := sha256.Sum256([]byte(roomID + "\x00" + text))
	return hex.EncodeToString(digest[:])[:16]
}

// matrixDMCache caches resolved DM rooms per (homeserver, mxid): the
// resolution costs a joined-rooms call plus per-room member probes. Entries
// live 10 minutes — a fresh DM created after a miss is picked up then.
var matrixDMCache = struct {
	sync.Mutex
	entries map[string]dmCacheEntry
}{entries: map[string]dmCacheEntry{}}

type dmCacheEntry struct {
	room     string
	resolved time.Time
}

// matrixTypingCache deduplicates typing refreshes: the watcher polls every
// 5s and each PUT restarts a 30s server-side window — a refresh more often
// than once per 20s is waste.
var matrixTypingCache = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

// matrixBotID fetches the bot account's own mxid (whoami) — needed to tell
// DMs (bot + user, 2 members) from small group rooms.
func matrixBotID(ctx context.Context, client *http.Client, homeserver, accessToken string) (string, error) {
	endpoint := homeserver + "/_matrix/client/v3/account/whoami"
	body, status, err := postJSON(ctx, client, http.MethodGet, endpoint, nil, map[string]string{"Authorization": "Bearer " + accessToken})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("matrix whoami returned %d: %s", status, truncate(body, 200))
	}
	var who struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal([]byte(body), &who); err != nil {
		return "", err
	}
	if who.UserID == "" {
		return "", errors.New("matrix whoami returned no user_id")
	}
	return who.UserID, nil
}

// ResolveDM (DMResolver) finds the direct-message room between the bot and
// the user's mxid: rooms the bot has joined that also have the user as a
// member, preferring rooms with exactly two members (a true DM) and rooms
// the bot has flagged in its own m.direct account data. The bot cannot read
// the user's DM map, so shared-room detection is the reliable path.
func (matrixDriver) ResolveDM(ctx context.Context, client *http.Client, settings map[string]string, senderID string) (string, error) {
	homeserver := strings.TrimRight(setting(settings, "homeserver"), "/")
	accessToken := TrimBearerPrefix(setting(settings, "access_token"))
	senderID = strings.TrimSpace(senderID)
	if homeserver == "" || accessToken == "" || senderID == "" {
		return "", errors.New("matrix DM resolution requires homeserver, access_token and the user's mxid")
	}
	// A room id pasted as the sender identity is returned as is.
	if strings.HasPrefix(senderID, "!") {
		return senderID, nil
	}
	cacheKey := homeserver + "\x00" + senderID
	matrixDMCache.Lock()
	if entry, ok := matrixDMCache.entries[cacheKey]; ok && time.Since(entry.resolved) < 10*time.Minute {
		room := entry.room
		matrixDMCache.Unlock()
		if room == "" {
			return "", errors.New("no DM room with this user (cached)")
		}
		return room, nil
	}
	matrixDMCache.Unlock()

	botID, err := matrixBotID(ctx, client, homeserver, accessToken)
	if err != nil {
		return "", err
	}
	// Rooms the bot has joined.
	joinedBody, status, err := postJSON(ctx, client, http.MethodGet, homeserver+"/_matrix/client/v3/joined_rooms", nil, map[string]string{"Authorization": "Bearer " + accessToken})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("matrix joined_rooms returned %d: %s", status, truncate(joinedBody, 200))
	}
	var joined struct {
		Rooms []string `json:"joined_rooms"`
	}
	if err := json.Unmarshal([]byte(joinedBody), &joined); err != nil {
		return "", err
	}
	// The bot's own m.direct flags (rooms the bot knows are DMs). The bot
	// cannot read the user's account data — only its own.
	directRooms := map[string]bool{}
	if directBody, dStatus, dErr := postJSON(ctx, client, http.MethodGet, homeserver+"/_matrix/client/v3/user/"+url.PathEscape(botID)+"/account_data/m.direct", nil, map[string]string{"Authorization": "Bearer " + accessToken}); dErr == nil && dStatus == http.StatusOK {
		var mdirect map[string][]string
		if err := json.Unmarshal([]byte(directBody), &mdirect); err == nil {
			for _, rooms := range mdirect[senderID] {
				directRooms[rooms] = true
			}
		}
	}
	fallback := ""
	for _, room := range joined.Rooms {
		// The user must be a member of this room.
		memberURL := homeserver + "/_matrix/client/v3/rooms/" + url.PathEscape(room) + "/state/m.room.member/" + url.PathEscape(senderID)
		memberBody, memberStatus, memberErr := postJSON(ctx, client, http.MethodGet, memberURL, nil, map[string]string{"Authorization": "Bearer " + accessToken})
		if memberErr != nil || memberStatus != http.StatusOK {
			continue
		}
		var membership struct {
			Membership string `json:"membership"`
		}
		if json.Unmarshal([]byte(memberBody), &membership) != nil || (membership.Membership != "join" && membership.Membership != "invite") {
			continue
		}
		// Count joined members: a DM is exactly bot + user.
		joinedMembersURL := homeserver + "/_matrix/client/v3/rooms/" + url.PathEscape(room) + "/joined_members"
		joinedBody2, joinedStatus, joinedErr := postJSON(ctx, client, http.MethodGet, joinedMembersURL, nil, map[string]string{"Authorization": "Bearer " + accessToken})
		twoMembers := false
		if joinedErr == nil && joinedStatus == http.StatusOK {
			var members struct {
				Joined map[string]struct{} `json:"joined"`
			}
			if json.Unmarshal([]byte(joinedBody2), &members) == nil {
				twoMembers = len(members.Joined) == 2
			}
		}
		if directRooms[room] {
			// A room the bot itself flags as a DM with this user wins outright.
			matrixDMCache.Lock()
			matrixDMCache.entries[cacheKey] = dmCacheEntry{room: room, resolved: time.Now()}
			matrixDMCache.Unlock()
			return room, nil
		}
		if twoMembers && fallback == "" {
			fallback = room
		}
	}
	matrixDMCache.Lock()
	if fallback != "" {
		matrixDMCache.entries[cacheKey] = dmCacheEntry{room: fallback, resolved: time.Now()}
	} else {
		matrixDMCache.entries[cacheKey] = dmCacheEntry{room: "", resolved: time.Now()}
	}
	matrixDMCache.Unlock()
	if fallback == "" {
		return "", errors.New("no shared DM room with " + senderID + " — invite the bot into a direct chat once, or register the room id in your profile")
	}
	return fallback, nil
}

// SendTyping sets or clears the bot's m.typing notification in a room. The
// homeserver expires the flag after the timeout window, so a live generation
// keeps refreshing it and a finished one just lets it lapse.
func (matrixDriver) SendTyping(ctx context.Context, client *http.Client, settings map[string]string, address string, typing bool) error {
	homeserver := strings.TrimRight(setting(settings, "homeserver"), "/")
	accessToken := TrimBearerPrefix(setting(settings, "access_token"))
	if homeserver == "" || accessToken == "" || address == "" {
		return errors.New("matrix transport is not configured")
	}
	botID, err := matrixBotID(ctx, client, homeserver, accessToken)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/typing/%s", homeserver, url.PathEscape(address), url.PathEscape(botID))
	payload := map[string]any{"typing": typing}
	if typing {
		payload["timeout"] = 30000
	}
	body, status, err := postJSON(ctx, client, http.MethodPut, endpoint, payload, map[string]string{"Authorization": "Bearer " + accessToken})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("matrix typing returned %d: %s", status, truncate(body, 200))
	}
	return nil
}

// MarkRead (ReadMarker) posts an m.read receipt for the event as the bot
// account: the sender's message shows as read the moment Temporality takes
// it into processing.
func (matrixDriver) MarkRead(ctx context.Context, client *http.Client, settings map[string]string, address, eventID string) error {
	homeserver := strings.TrimRight(setting(settings, "homeserver"), "/")
	accessToken := TrimBearerPrefix(setting(settings, "access_token"))
	if homeserver == "" || accessToken == "" || address == "" || eventID == "" {
		return errors.New("matrix transport is not configured")
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/receipt/m.read/%s", homeserver, url.PathEscape(address), url.PathEscape(eventID))
	body, status, err := postJSON(ctx, client, http.MethodPost, endpoint, map[string]any{}, map[string]string{"Authorization": "Bearer " + accessToken})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("matrix read receipt returned %d: %s", status, truncate(body, 200))
	}
	return nil
}
