package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// slackBotTestDriver finds the registered slackbot driver — the receiver is
// generic, the scenarios stay slack-shaped (signed Events API envelopes).
func slackBotTestDriver(t *testing.T) agent.ChannelDriver {
	t.Helper()
	for _, driver := range agent.ChannelDrivers() {
		if driver.Type() == "slackbot" {
			return driver
		}
	}
	t.Fatal("slackbot driver not registered")
	return nil
}

func newWebhookReceiver(t *testing.T, store *fakeInboundStore, sig *recordedSignal) *channelWebhookReceiver {
	t.Helper()
	driver := slackBotTestDriver(t)
	hook := driver.Webhook()
	if hook == nil {
		t.Fatal("slackbot driver has no webhook")
	}
	return &channelWebhookReceiver{
		loop: &channelInboundLoop{
			driver:     driver,
			httpClient: http.DefaultClient,
			settings: func(context.Context) (agent.ChannelSettings, error) {
				return agent.ChannelSettings{Channels: map[string]map[string]string{
					"slackbot": {"bot_token": "xoxb-test", "signing_secret": "sekrit"},
				}}, nil
			},
			store:    store,
			sourceID: "src",
			log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			signal:   sig.record,
			intake: func(context.Context, agent.InboundMessage, string) (string, error) {
				t.Fatal("intake must not start for a reply")
				return "", nil
			},
			intakeEnabled: true,
		},
		hook: hook,
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// slackWebhookPost posts one signed Events API message envelope. The event
// ts is generated fresh: reply matching rejects messages older than the ask
// (beyond clock skew), and a literal doc timestamp is years in the past.
func slackWebhookPost(t *testing.T, receiver *channelWebhookReceiver, eventID, user, channel, text string) *httptest.ResponseRecorder {
	t.Helper()
	ts := strconv.FormatFloat(float64(time.Now().Unix())+0.0001, 'f', -1, 64)
	payload := `{"type":"event_callback","event_id":"` + eventID + `","event":{"type":"message","text":"` + text + `","user":"` + user + `","channel":"` + channel + `","ts":"` + ts + `"}}`
	req, err := http.NewRequest(http.MethodPost, "/v1/channels/slackbot/events", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	headerTS := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("X-Slack-Request-Timestamp", headerTS)
	req.Header.Set("X-Slack-Signature", slackSignForTest("sekrit", headerTS, payload))
	rec := httptest.NewRecorder()
	receiver.handle(rec, req)
	return rec
}

func slackSignForTest(secret, timestamp, body string) string {
	mac := hmacSHA256HexForTest(secret, "v0:"+timestamp+":"+body)
	return "v0=" + mac
}

func hmacSHA256HexForTest(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// TestChannelWebhookRoutesReplyIntoRun: a signed message in the channel an
// open ask was delivered to answers it — the approval signal fires, the
// request row closes, the HTTP answer is 200.
func TestChannelWebhookRoutesReplyIntoRun(t *testing.T) {
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "run/turn/01/call_q", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
			Channel: "slackbot", Status: "delivered", Question: "Deploy?",
			CreatedAt: time.Now().Add(-5 * time.Minute),
		}},
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "slackbot", Address: "C1", SenderID: "U1", Enabled: true}},
		}},
	}
	sig := &recordedSignal{}
	receiver := newWebhookReceiver(t, store, sig)

	rec := slackWebhookPost(t, receiver, "Ev1", "U1", "C1", "yes, deploy")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if sig.count() != 1 {
		t.Fatalf("expected one signal, got %d", sig.count())
	}
	approval := sig.calls[0]
	if approval.OperationID != "run/turn/01/call_q" || !approval.Approved || approval.Response != "yes, deploy" || approval.ActorID != "u1" {
		t.Fatalf("unexpected approval: %+v", approval)
	}
	if store.answered["run/turn/01/call_q"] != "yes, deploy" {
		t.Fatalf("row not answered: %+v", store.answered)
	}
}

// TestChannelWebhookDeduplicatesRetries: Slack redelivers the same event on
// retries — the reply signals once, the second delivery is a no-op the
// channel sees acknowledged.
func TestChannelWebhookDeduplicatesRetries(t *testing.T) {
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "run/turn/01/call_r", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
			Channel: "slackbot", Status: "delivered", Question: "Ready?",
			CreatedAt: time.Now().Add(-5 * time.Minute),
		}},
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "slackbot", Address: "C1", SenderID: "U1", Enabled: true}},
		}},
	}
	sig := &recordedSignal{}
	receiver := newWebhookReceiver(t, store, sig)

	for i := 0; i < 2; i++ {
		rec := slackWebhookPost(t, receiver, "EvDUP", "U1", "C1", "yes")
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d: expected 200, got %d", i, rec.Code)
		}
	}
	if sig.count() != 1 {
		t.Fatalf("retry must not re-route the reply, got %d signals", sig.count())
	}
}

// TestChannelWebhookRejectsBadSignature: unsigned or wrongly signed requests
// never reach the pipeline.
func TestChannelWebhookRejectsBadSignature(t *testing.T) {
	store := &fakeInboundStore{}
	receiver := newWebhookReceiver(t, store, &recordedSignal{})

	payload := `{"type":"event_callback","event_id":"EvX","event":{"type":"message","text":"hi","user":"U1","channel":"C1","ts":"1697000000.000100"}}`
	req, _ := http.NewRequest(http.MethodPost, "/v1/channels/slackbot/events", strings.NewReader(payload))
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("X-Slack-Signature", "v0=deadbeef")
	rec := httptest.NewRecorder()
	receiver.handle(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if len(store.inbound) != 0 {
		t.Fatalf("unverified message must not be recorded, got %+v", store.inbound)
	}
}

// TestChannelWebhookInitiativeMessageEntersIntake: a message that answers no
// open ask is an initiative message — the intake pipeline records and
// rejects it (closed world: the sender identity does not match the channel
// owner's registered one here).
func TestChannelWebhookInitiativeMessageEntersIntake(t *testing.T) {
	store := &fakeInboundStore{
		users: []workspace.User{{
			ID: "u1", Name: "Human",
			Channels: []workspace.UserChannel{{Type: "slackbot", Address: "C1", SenderID: "U1", Enabled: true}},
		}},
	}
	emit := &recordedEmit{}
	receiver := newWebhookReceiver(t, store, &recordedSignal{})
	receiver.loop.emit = emit.record
	receiver.loop.intake = func(context.Context, agent.InboundMessage, string) (string, error) {
		t.Fatal("intake must not start for an unregistered sender")
		return "", nil
	}

	rec := slackWebhookPost(t, receiver, "EvI", "U2", "C1", "подготовь смету")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	row, ok := store.inboundRow("EvI")
	if !ok {
		t.Fatal("message not recorded")
	}
	if latest, _ := store.lastUpdate(row.ID); latest.Status != workspace.InboundStatusRejected {
		t.Fatalf("row not rejected: %+v", latest)
	}
	if emit.count() != 1 || emit.rawData[0]["reason"] != "unknown_sender" {
		t.Fatalf("unexpected events: %v %+v", emit.types, emit.rawData)
	}
}
