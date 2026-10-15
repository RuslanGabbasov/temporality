package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// mattermostPoller adapts the Mattermost WebSocket event stream to the
// ChannelPoller contract (docs/channels.md §3, §9): one connection per poll
// window, an authentication challenge with the driver's access token, then
// `posted` events for every channel the account participates in — including
// direct messages, which is exactly what outgoing webhooks could never serve.
// The adapter is stateless; the loop owns the cursor (last seen post
// create_at, milliseconds).
//
// The tradeoff of a per-call connection: events that fire in the gap between
// two windows (while the loop routes the previous batch and saves the cursor)
// are not delivered — the same at-least-once posture the webhook feed had,
// with a millisecond gap per 25s instead of a route that is dead whenever the
// kernel is. Replies that arrive in a gap are re-sent by the human; intake
// messages can be resent as well.
type mattermostPoller struct {
	// window, hello and batch exist for tests; zero values fall back to the
	// production constants below.
	window time.Duration
	hello  time.Duration
	batch  int
}

const (
	// mattermostPollWindow bounds one poll call, mirroring matrixSyncTimeout:
	// the loop re-enters long-poll immediately (delay 0), so a shorter window
	// only costs more dial handshakes.
	mattermostPollWindow = 25 * time.Second
	// mattermostHelloTimeout bounds the authentication handshake inside a
	// window: a server that never says hello is a configuration problem and
	// must surface as an error, not as a silent wait.
	mattermostHelloTimeout = 10 * time.Second
	// mattermostPollBatch caps one returned batch; the loop re-polls
	// immediately, so a busy channel streams in chunks instead of one
	// unbounded slice.
	mattermostPollBatch = 100
)

// mattermostWSRequest is one client frame of the Mattermost WebSocket
// protocol; only the authentication challenge is sent today.
type mattermostWSRequest struct {
	Seq    int               `json:"seq"`
	Action string            `json:"action,omitempty"`
	Data   map[string]string `json:"data,omitempty"`
}

// mattermostPost is the post embedded (as a JSON-encoded string) in a
// `posted` event payload — the protocol's most treacherous quirk.
type mattermostPost struct {
	ID        string `json:"id"`
	CreateAt  int64  `json:"create_at"`
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
	RootID    string `json:"root_id"`
	Message   string `json:"message"`
}

func (p mattermostPoller) Poll(ctx context.Context, client *http.Client, settings map[string]string, cursor string) ([]InboundMessage, string, error) {
	base, token, err := mattermostCredentials(settings)
	if err != nil {
		return nil, "", err
	}
	window, helloWait, batch := p.window, p.hello, p.batch
	if window <= 0 {
		window = mattermostPollWindow
	}
	if helloWait <= 0 {
		helloWait = mattermostHelloTimeout
	}
	if batch <= 0 {
		batch = mattermostPollBatch
	}
	dialClient := client
	if dialClient == nil {
		dialClient = http.DefaultClient
	}
	// The handshake budget rides on top of the window so a server that
	// accepts the socket but never answers the challenge cannot pin the call
	// for the full window.
	callCtx, cancel := context.WithTimeout(ctx, window+helloWait)
	defer cancel()
	conn, _, err := websocket.Dial(callCtx, mattermostWebsocketURL(base), &websocket.DialOptions{HTTPClient: dialClient})
	if err != nil {
		return nil, "", fmt.Errorf("mattermost websocket dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "poll window closed")
	challenge, err := json.Marshal(mattermostWSRequest{
		Seq:    1,
		Action: "authentication_challenge",
		Data:   map[string]string{"token": token},
	})
	if err != nil {
		return nil, "", err
	}
	if err := conn.Write(callCtx, websocket.MessageText, challenge); err != nil {
		return nil, "", fmt.Errorf("mattermost websocket authenticate: %w", err)
	}
	messages := make([]InboundMessage, 0, 8)
	next := mattermostCursorMillis(cursor)
	helloSeen := false
	deadline := time.Now().Add(window)
	for len(messages) < batch {
		wait := time.Until(deadline)
		if !helloSeen && helloWait < wait {
			wait = helloWait
		}
		if wait <= 0 {
			break
		}
		readCtx, readCancel := context.WithTimeout(callCtx, wait)
		_, frame, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			if !deadlineExceeded(err) {
				// Server closed mid-window (restart, idle timeout): surface for
				// the loop's backoff; the next call redials.
				return nil, "", fmt.Errorf("mattermost websocket read: %w", err)
			}
			if !helloSeen {
				return nil, "", errors.New("mattermost websocket: server never completed the authentication handshake")
			}
			break // window elapsed — a normal long-poll return
		}
		var event struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
			Error *struct {
				ID      string `json:"id"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(frame, &event); err != nil {
			continue // keep-alives and foreign frames are not ours to fail on
		}
		if event.Error != nil {
			return nil, "", fmt.Errorf("mattermost websocket: %s: %s", event.Error.ID, event.Error.Message)
		}
		switch event.Event {
		case "hello":
			helloSeen = true
		case "security_error":
			return nil, "", fmt.Errorf("mattermost websocket rejected credentials: %s", truncate(string(event.Data), 200))
		case "posted":
			post, ok := mattermostParsePosted(event.Data)
			if !ok {
				continue
			}
			if next != 0 && post.CreateAt < next {
				continue // seen before the cursor — replay safety, never a loss
			}
			messages = append(messages, InboundMessage{
				ID:        post.ID,
				Channel:   "mattermost",
				ChatID:    post.ChannelID,
				Sender:    post.UserID,
				Text:      strings.TrimSpace(post.Message),
				Timestamp: time.UnixMilli(post.CreateAt).UTC(),
				ReplyTo:   post.RootID,
			})
			if post.CreateAt > next {
				next = post.CreateAt
			}
		}
	}
	if next == 0 {
		return messages, "", nil // nothing observed yet — keep the fresh-start cursor
	}
	return messages, strconv.FormatInt(next, 10), nil
}

// Self resolves the token's own account id so the loop can exclude the
// account's own posts from reply matching — the WebSocket stream echoes them.
func (p mattermostPoller) Self(ctx context.Context, client *http.Client, settings map[string]string) (string, error) {
	return mattermostDriver{}.Self(ctx, client, settings)
}

// mattermostParsePosted decodes one `posted` event payload into a post.
// File-only and malformed posts report !ok — they cannot route as text yet.
func mattermostParsePosted(data json.RawMessage) (mattermostPost, bool) {
	var payload struct {
		Post string `json:"post"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Post == "" {
		return mattermostPost{}, false
	}
	var post mattermostPost
	if err := json.Unmarshal([]byte(payload.Post), &post); err != nil {
		return mattermostPost{}, false
	}
	if post.ID == "" || post.ChannelID == "" || post.UserID == "" || strings.TrimSpace(post.Message) == "" {
		return mattermostPost{}, false
	}
	return post, true
}

// mattermostCursorMillis decodes the loop's cursor (post create_at in
// milliseconds). An unreadable cursor restarts from now — the WebSocket
// stream only carries new events, so a fresh start never replays history.
func mattermostCursorMillis(cursor string) int64 {
	ms, err := strconv.ParseInt(strings.TrimSpace(cursor), 10, 64)
	if err != nil || ms < 0 {
		return 0
	}
	return ms
}

// mattermostWebsocketURL maps the REST base URL onto the WebSocket endpoint.
func mattermostWebsocketURL(base string) string {
	endpoint := base + "/api/v4/websocket"
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		return "wss://" + strings.TrimPrefix(endpoint, "https://")
	case strings.HasPrefix(endpoint, "http://"):
		return "ws://" + strings.TrimPrefix(endpoint, "http://")
	}
	return endpoint
}

// deadlineExceeded reports whether a websocket read error is a context
// deadline — coder/websocket returns the ctx error directly on most paths but
// wraps it without %w on some read paths.
func deadlineExceeded(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded")
}
