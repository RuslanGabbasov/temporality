package agent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func slackTestDriver() *slackBotDriver { return &slackBotDriver{} }

func slackSettings() map[string]string {
	return map[string]string{"bot_token": "xoxb-test", "signing_secret": "sekrit"}
}

// slackSign computes the X-Slack-Signature header value over the body.
func slackSign(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write([]byte(body))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func slackSignedRequest(t *testing.T, payload string, age time.Duration) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/v1/channels/slackbot/events", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	ts := strconv.FormatInt(time.Now().Add(-age).Unix(), 10)
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackSign("sekrit", ts, payload))
	return req
}

func TestSlackEventsVerifyRequest(t *testing.T) {
	hook := slackEventsWebhook{}
	body := `{"type":"event_callback","event_id":"Ev1"}`

	if err := hook.VerifyRequest(slackSignedRequest(t, body, 0), []byte(body), slackSettings()); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := hook.VerifyRequest(slackSignedRequest(t, body, 10*time.Minute), []byte(body), slackSettings()); err == nil {
		t.Fatal("stale timestamp accepted")
	}
	req := slackSignedRequest(t, body, 0)
	req.Header.Set("X-Slack-Signature", "v0=deadbeef")
	if err := hook.VerifyRequest(req, []byte(body), slackSettings()); err == nil {
		t.Fatal("bad signature accepted")
	}
	req = slackSignedRequest(t, body, 0)
	req.Header.Del("X-Slack-Signature")
	if err := hook.VerifyRequest(req, []byte(body), slackSettings()); err == nil {
		t.Fatal("missing signature accepted")
	}
	if err := hook.VerifyRequest(slackSignedRequest(t, body, 0), []byte(body), map[string]string{"bot_token": "xoxb-test"}); err == nil {
		t.Fatal("missing signing secret accepted")
	}
}

func TestSlackEventsParse(t *testing.T) {
	hook := slackEventsWebhook{}

	t.Run("url verification carries no messages", func(t *testing.T) {
		body := `{"type":"url_verification","challenge":"abc123"}`
		if got := hook.Parse(slackSignedRequest(t, body, 0), []byte(body)); len(got) != 0 {
			t.Fatalf("expected none, got %+v", got)
		}
	})

	t.Run("message event normalizes with thread pointer", func(t *testing.T) {
		body := `{"type":"event_callback","event_id":"Ev42","event":{"type":"message","text":"yes","user":"U1","channel":"C1","ts":"1697000000.000100","thread_ts":"1697000000.000050"}}`
		messages := hook.Parse(slackSignedRequest(t, body, 0), []byte(body))
		if len(messages) != 1 {
			t.Fatalf("expected one message, got %+v", messages)
		}
		m := messages[0]
		if m.ID != "Ev42" || m.Channel != "slackbot" || m.ChatID != "C1" || m.Sender != "U1" || m.Text != "yes" {
			t.Fatalf("unexpected message: %+v", m)
		}
		if m.ReplyTo != "1697000000.000050" {
			t.Fatalf("thread pointer lost: %+v", m)
		}
		if m.Timestamp.Unix() != 1697000000 {
			t.Fatalf("timestamp not parsed from ts: %v", m.Timestamp)
		}
	})

	t.Run("bot and subtype events are skipped", func(t *testing.T) {
		for name, body := range map[string]string{
			"bot":     `{"type":"event_callback","event_id":"EvB","event":{"type":"message","text":"q","bot_id":"B1","channel":"C1","ts":"1697000000.000200"}}`,
			"subtype": `{"type":"event_callback","event_id":"EvS","event":{"type":"message","text":"edited","user":"U1","subtype":"message_changed","channel":"C1","ts":"1697000000.000300"}}`,
		} {
			if got := hook.Parse(slackSignedRequest(t, body, 0), []byte(body)); len(got) != 0 {
				t.Fatalf("%s: expected none, got %+v", name, got)
			}
		}
	})
}

func TestSlackEventsRespondEchoesChallenge(t *testing.T) {
	hook := slackEventsWebhook{}
	rec := httptest.NewRecorder()
	hook.Respond(rec, httptest.NewRequest(http.MethodPost, "/v1/channels/slackbot/events", nil), []byte(`{"type":"url_verification","challenge":"abc123"}`))
	if rec.Code != http.StatusOK || rec.Body.String() != "abc123" {
		t.Fatalf("challenge echo broken: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	hook.Respond(rec, httptest.NewRequest(http.MethodPost, "/v1/channels/slackbot/events", nil), []byte(`{"type":"event_callback"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("plain acknowledgement broken: %d", rec.Code)
	}
}

func TestSlackBotSendText(t *testing.T) {
	var gotAuth, gotPayload string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		gotPayload = buf.String()
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1697000000.000200"}`))
	}))
	defer server.Close()
	original := slackAPIBase
	slackAPIBase = server.URL
	t.Cleanup(func() { slackAPIBase = original })

	driver := slackTestDriver()
	if err := driver.SendText(context.Background(), server.Client(), slackSettings(), "C123", OutboundText{Body: "Deploy?"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer xoxb-test" {
		t.Fatalf("auth header %q", gotAuth)
	}
	if !strings.Contains(gotPayload, `"channel":"C123"`) || !strings.Contains(gotPayload, "Reply in this channel") {
		t.Fatalf("payload %q", gotPayload)
	}
	if strings.Contains(gotPayload, "thread_ts") {
		t.Fatalf("top-level post must not carry thread_ts: %q", gotPayload)
	}
}

func TestSlackBotSendTextSurfacesErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer server.Close()
	original := slackAPIBase
	slackAPIBase = server.URL
	t.Cleanup(func() { slackAPIBase = original })

	err := slackTestDriver().SendText(context.Background(), server.Client(), slackSettings(), "C404", OutboundText{Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("expected envelope error, got %v", err)
	}
}

// TestSlackBotThreadsFollowUpsAndReportsPostedID pins the thread contract
// (docs/channels.md §9): a non-empty OutboundText.Thread posts into that
// thread (thread_ts in the chat.postMessage payload), and the posted
// message's ts comes back so the kernel can pin the conversation.
func TestSlackBotThreadsFollowUpsAndReportsPostedID(t *testing.T) {
	var gotPayload string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		gotPayload = buf.String()
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1697000000.000777"}`))
	}))
	defer server.Close()
	original := slackAPIBase
	slackAPIBase = server.URL
	t.Cleanup(func() { slackAPIBase = original })

	driver := slackTestDriver()
	postedID, err := driver.SendTextThreaded(context.Background(), server.Client(), slackSettings(), "C123",
		OutboundText{Body: "Follow-up", Thread: "1697000000.000100"})
	if err != nil {
		t.Fatal(err)
	}
	if postedID != "1697000000.000777" {
		t.Fatalf("posted message ts not decoded: %q", postedID)
	}
	if !strings.Contains(gotPayload, `"thread_ts":"1697000000.000100"`) {
		t.Fatalf("thread pointer not sent: %q", gotPayload)
	}
}

// A top-level post still reports its ts through the threaded call — the
// kernel pins it as the conversation root the user's replies will thread on.
func TestSlackBotTopLevelPostCarriesNoThreadTS(t *testing.T) {
	var gotPayload string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		gotPayload = buf.String()
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1697000000.000300"}`))
	}))
	defer server.Close()
	original := slackAPIBase
	slackAPIBase = server.URL
	t.Cleanup(func() { slackAPIBase = original })

	postedID, err := slackTestDriver().SendTextThreaded(context.Background(), server.Client(), slackSettings(), "C123",
		OutboundText{Body: "First question"})
	if err != nil {
		t.Fatal(err)
	}
	if postedID != "1697000000.000300" {
		t.Fatalf("posted message ts not decoded: %q", postedID)
	}
	if strings.Contains(gotPayload, "thread_ts") {
		t.Fatalf("top-level post must not carry thread_ts: %q", gotPayload)
	}
}
