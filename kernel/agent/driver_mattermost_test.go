package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func mattermostSettings() map[string]string {
	return map[string]string{"base_url": "https://mm.example.com", "token": "tok"}
}

// mattermostWSServer stands up one Mattermost-shaped WebSocket endpoint: it
// reads the client's first frame (the authentication challenge), hands it to
// the script for inspection and streaming, then holds the connection until
// the client closes it.
func mattermostWSServer(t *testing.T, script func(ctx context.Context, conn *websocket.Conn, auth string)) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		defer conn.Close(websocket.StatusNormalClosure, "server done")
		_, auth, err := conn.Read(ctx)
		if err != nil {
			return
		}
		mu.Lock()
		script(ctx, conn, string(auth))
		mu.Unlock()
		// Hold the connection: the poller keeps reading until its window
		// closes, and a server-side close mid-window reads as an error there.
		for ctx.Err() == nil {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// mattermostPostedFrame builds one `posted` event with the post encoded as a
// JSON string inside the payload — the protocol quirk mattermostParsePosted
// exists for.
func mattermostPostedFrame(t *testing.T, post mattermostPost) []byte {
	t.Helper()
	encoded, err := json.Marshal(post)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := json.Marshal(map[string]any{
		"event": "posted",
		"data":  map[string]any{"post": string(encoded), "channel_type": "D"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func mattermostWrite(ctx context.Context, conn *websocket.Conn, frame string) {
	if err := conn.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		// The poller may have closed the connection first on failure tests.
		return
	}
}

func TestMattermostPollerStreamsPostedEvents(t *testing.T) {
	post := mattermostPost{
		ID: "post1", CreateAt: 1697200000000, UserID: "u1", ChannelID: "ch1",
		RootID: "root0", Message: "  сделай смету на разработку  ",
	}
	authSeen := make(chan string, 1)
	server := mattermostWSServer(t, func(ctx context.Context, conn *websocket.Conn, auth string) {
		authSeen <- auth
		mattermostWrite(ctx, conn, `{"event":"hello","data":{"server_version":"9.5.0"}}`)
		mattermostWrite(ctx, conn, string(mattermostPostedFrame(t, post)))
		mattermostWrite(ctx, conn, `{"event":"typing","data":{}}`)                                                                                                                   // ignored event
		mattermostWrite(ctx, conn, `{"event":"posted","data":{"post":"{\"id\":\"p2\",\"create_at\":1697200000001,\"user_id\":\"u2\",\"channel_id\":\"ch1\",\"message\":\"   \"}"}}`) // whitespace-only text → skipped
	})
	settings := mattermostSettings()
	settings["base_url"] = server.URL
	poller := mattermostPoller{window: 300 * time.Millisecond, hello: time.Second}

	messages, next, err := poller.Poll(context.Background(), server.Client(), settings, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %+v", messages)
	}
	m := messages[0]
	if m.ID != "post1" || m.Channel != "mattermost" || m.ChatID != "ch1" || m.Sender != "u1" || m.ReplyTo != "root0" {
		t.Fatalf("unexpected message: %+v", m)
	}
	if m.Text != "сделай смету на разработку" {
		t.Fatalf("text not trimmed: %q", m.Text)
	}
	if !m.Timestamp.Equal(time.UnixMilli(1697200000000).UTC()) {
		t.Fatalf("timestamp not parsed: %v", m.Timestamp)
	}
	if next != "1697200000000" {
		t.Fatalf("next cursor %q", next)
	}
	select {
	case auth := <-authSeen:
		if !strings.Contains(auth, `"authentication_challenge"`) || !strings.Contains(auth, `"token":"tok"`) {
			t.Fatalf("auth challenge %q", auth)
		}
	default:
		t.Fatal("authentication challenge never arrived")
	}
}

func TestMattermostPollerCursorFiltersStalePosts(t *testing.T) {
	post := mattermostPost{ID: "post1", CreateAt: 1697200000000, UserID: "u1", ChannelID: "ch1", Message: "hi"}
	server := mattermostWSServer(t, func(ctx context.Context, conn *websocket.Conn, auth string) {
		mattermostWrite(ctx, conn, `{"event":"hello","data":{}}`)
		mattermostWrite(ctx, conn, string(mattermostPostedFrame(t, post)))
	})
	settings := mattermostSettings()
	settings["base_url"] = server.URL
	poller := mattermostPoller{window: 250 * time.Millisecond, hello: time.Second}

	messages, next, err := poller.Poll(context.Background(), server.Client(), settings, "1697200000001")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("stale post passed the cursor: %+v", messages)
	}
	if next != "1697200000001" {
		t.Fatalf("cursor must stay unchanged, got %q", next)
	}
}

func TestMattermostPollerAuthFailures(t *testing.T) {
	cases := []struct {
		name   string
		frame  string
		wantIn string
	}{
		{
			name:   "security error event",
			frame:  `{"event":"security_error","data":{"error":"invalid or missing session token"}}`,
			wantIn: "rejected credentials",
		},
		{
			name:   "error response frame",
			frame:  `{"status":"ERROR","seq_reply":1,"error":{"id":"api.web_socket_router.not_authenticated","message":"not authenticated"}}`,
			wantIn: "not_authenticated",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := mattermostWSServer(t, func(ctx context.Context, conn *websocket.Conn, auth string) {
				mattermostWrite(ctx, conn, tc.frame)
			})
			settings := mattermostSettings()
			settings["base_url"] = server.URL
			poller := mattermostPoller{window: time.Second, hello: time.Second}
			_, _, err := poller.Poll(context.Background(), server.Client(), settings, "")
			if err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("expected error containing %q, got %v", tc.wantIn, err)
			}
		})
	}
}

func TestMattermostPollerRequiresHello(t *testing.T) {
	server := mattermostWSServer(t, func(ctx context.Context, conn *websocket.Conn, auth string) {
		// Say nothing: the server accepted the socket but never completes the
		// handshake — the poller must fail, not wait out the window.
	})
	settings := mattermostSettings()
	settings["base_url"] = server.URL
	poller := mattermostPoller{window: time.Second, hello: 150 * time.Millisecond}
	start := time.Now()
	_, _, err := poller.Poll(context.Background(), server.Client(), settings, "")
	if err == nil || !strings.Contains(err.Error(), "authentication handshake") {
		t.Fatalf("expected handshake error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("handshake timeout not honored: %v", elapsed)
	}
}

func TestMattermostWebsocketURL(t *testing.T) {
	cases := map[string]string{
		"https://mm.example.com": "wss://mm.example.com/api/v4/websocket",
		"http://localhost:8065":  "ws://localhost:8065/api/v4/websocket",
	}
	for base, want := range cases {
		if got := mattermostWebsocketURL(base); got != want {
			t.Fatalf("mattermostWebsocketURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestMattermostSendText(t *testing.T) {
	var gotAuth, gotPayload string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		gotPayload = string(raw[:n])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"post9","create_at":1}`))
	}))
	defer server.Close()
	settings := mattermostSettings()
	settings["base_url"] = server.URL

	if err := (&mattermostDriver{}).SendText(context.Background(), server.Client(), settings, "ch1", OutboundText{Body: "Deploy?"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth header %q", gotAuth)
	}
	if !strings.Contains(gotPayload, `"channel_id":"ch1"`) || !strings.Contains(gotPayload, "Reply in this channel") {
		t.Fatalf("payload %q", gotPayload)
	}
}
