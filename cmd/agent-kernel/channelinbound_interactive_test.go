package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
	"github.com/temporality-project/temporality/workspace"
)

// telegramLoopDriver implements the interactive extensions over a recorded
// Bot API: the loop's ack/edit/widget calls land here as plain HTTP, no real
// Telegram involved. Calls record into a plain slice — the routing under
// test is single-goroutine, no lock (a lock field would fail vet copylocks).
type telegramLoopDriver struct {
	agent.ChannelDriver
	server *httptest.Server
	calls  []string // method paths, in order
}

func (d *telegramLoopDriver) Type() string { return "telegram" }

func (d *telegramLoopDriver) record(path string) {
	d.calls = append(d.calls, path)
}

func (d *telegramLoopDriver) recorded() []string {
	return append([]string(nil), d.calls...)
}

func (d *telegramLoopDriver) settings() map[string]string {
	return map[string]string{"bot_token": "tok", "api_base": d.server.URL}
}

func (telegramLoopDriver) IsConfigured(map[string]string) bool { return true }

func (d *telegramLoopDriver) AnswerCallback(ctx context.Context, _ *http.Client, _ map[string]string, queryID, text string) error {
	d.record("/answerCallbackQuery")
	return nil
}

func (d *telegramLoopDriver) EditMessage(ctx context.Context, _ *http.Client, _ map[string]string, address, messageID, text string, buttons [][]agent.OutboundButton) error {
	d.record("/editMessageText")
	return nil
}

func (d *telegramLoopDriver) SendWidget(ctx context.Context, _ *http.Client, _ map[string]string, address string, w agent.OutboundWidget) (string, error) {
	d.record("/sendWidget")
	return "77", nil
}

func (d *telegramLoopDriver) EditWidgetData(ctx context.Context, _ *http.Client, _ map[string]string, address, messageID, model string) error {
	d.record("/editMessageWidgetData")
	return nil
}

func (d *telegramLoopDriver) AnswerWidget(ctx context.Context, _ *http.Client, _ map[string]string, queryID, data string) error {
	d.record("/answerWidgetQuery")
	return nil
}

// newInteractiveLoop builds the loop against the telegram driver without the
// poll machinery: routing is exercised directly.
func newInteractiveLoop(t *testing.T, store *fakeInboundStore, sig *recordedSignal) (*channelInboundLoop, *telegramLoopDriver) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(server.Close)
	driver := &telegramLoopDriver{ChannelDriver: agent.ChannelDriverByTypePublic("telegram"), server: server}
	loop := &channelInboundLoop{
		driver:     driver,
		poller:     driver.Poller(),
		httpClient: server.Client(),
		store:      store,
		sourceID:   "src",
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		signal:     sig.record,
	}
	return loop, driver
}

func interactiveSettings() map[string]string { return map[string]string{"bot_token": "tok"} }

// TestChannelCallbackRoutesOption pins Phase 1: a button press resolves the
// ask with the option text, answers the callback and edits the message in
// place — the same signal path a text reply takes.
func TestChannelCallbackRoutesOption(t *testing.T) {
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "run/turn/01/call_ask", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
			Channel: "telegram", Status: workspace.HumanStatusPending,
			Question:  "Deploy to which channel?",
			Options:   []string{"canary", "stable"},
			CreatedAt: time.Now().Add(-time.Minute),
		}},
		users: []workspace.User{{
			ID: "u1", Channels: []workspace.UserChannel{{Type: "telegram", Address: "555", Enabled: true, SenderID: "555"}},
		}},
	}
	sig := &recordedSignal{}
	loop, driver := newInteractiveLoop(t, store, sig)

	loop.handleCallback(context.Background(), interactiveSettings(), agent.InboundMessage{
		ID: "200", Channel: "telegram", ChatID: "555", Sender: "555",
		Kind: "callback", CallbackData: "a:run/turn/01/call_ask:0",
		QueryID: "cbq-1", SourceMessageID: "42", Timestamp: time.Now(),
	})

	calls := sig.calls
	if len(calls) != 1 || calls[0].OperationID != "run/turn/01/call_ask" || !calls[0].Approved || calls[0].Response != "canary" {
		t.Fatalf("approval = %+v", calls)
	}
	if store.answered["run/turn/01/call_ask"] != "canary" {
		t.Fatalf("row not answered: %+v", store.answered)
	}
	paths := driver.recorded()
	if len(paths) != 2 || paths[0] != "/answerCallbackQuery" || paths[1] != "/editMessageText" {
		t.Fatalf("driver calls = %v", paths)
	}
	if len(store.audits) == 0 || store.audits[0] != workspace.AuditHumanAnswered {
		t.Fatalf("audits = %v", store.audits)
	}
}

// TestChannelCallbackRejectsClosedAndForeign pins the guards: a closed ask is
// never resurrected by a tap; a chat outside the resolved user's addresses is
// refused without routing.
func TestChannelCallbackRejectsClosedAndForeign(t *testing.T) {
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "op1", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
			Channel: "telegram", Status: workspace.HumanStatusAnswered, // closed
			Question: "Deploy?", Options: []string{"yes"}, CreatedAt: time.Now().Add(-time.Minute),
		}},
		users: []workspace.User{{
			ID: "u1", Channels: []workspace.UserChannel{{Type: "telegram", Address: "555", Enabled: true}},
		}},
	}
	sig := &recordedSignal{}
	loop, driver := newInteractiveLoop(t, store, sig)

	// Closed ask: ack, no signal.
	loop.handleCallback(context.Background(), interactiveSettings(), agent.InboundMessage{
		ID: "201", Channel: "telegram", ChatID: "555", Sender: "555",
		Kind: "callback", CallbackData: "a:op1:0", QueryID: "cbq-2", SourceMessageID: "42", Timestamp: time.Now(),
	})
	if sig.count() != 0 {
		t.Fatalf("closed ask must not signal: %+v", sig.calls)
	}

	// Foreign chat: refused.
	store2 := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "op2", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
			Channel: "telegram", Status: workspace.HumanStatusPending,
			Question: "Deploy?", Options: []string{"yes"}, CreatedAt: time.Now(),
		}},
		users: []workspace.User{{
			ID: "u1", Channels: []workspace.UserChannel{{Type: "telegram", Address: "555", Enabled: true}},
		}},
	}
	loop2, _ := newInteractiveLoop(t, store2, sig)
	loop2.handleCallback(context.Background(), interactiveSettings(), agent.InboundMessage{
		ID: "202", Channel: "telegram", ChatID: "999", Sender: "999",
		Kind: "callback", CallbackData: "a:op2:0", QueryID: "cbq-3", SourceMessageID: "42", Timestamp: time.Now(),
	})
	if sig.count() != 0 {
		t.Fatalf("foreign chat must not signal: %+v", sig.calls)
	}

	// Malformed token: ack only.
	loop.handleCallback(context.Background(), interactiveSettings(), agent.InboundMessage{
		ID: "203", Channel: "telegram", ChatID: "555", Sender: "555",
		Kind: "callback", CallbackData: "garbage", QueryID: "cbq-4", SourceMessageID: "42", Timestamp: time.Now(),
	})
	if !strings.Contains(strings.Join(driver.recorded(), ","), "/answerCallbackQuery") {
		t.Fatalf("ack missing: %v", driver.recorded())
	}
}

// TestChannelWidgetQuerySubmitsForm pins Phase 3 end-to-end over the neutral
// protocol: set ops accumulate in the authoritative state, the ack + data
// push land per op, and the submit resolves the ask with the canonical JSON —
// exactly what a web-form submission produces.
func TestChannelWidgetQuerySubmitsForm(t *testing.T) {
	schema := `{"type":"object","properties":{
		"channel":{"type":"string","enum":["canary","stable"],"title":"Канал"},
		"confirm":{"type":"boolean","title":"Подтверждаю"}},"required":["channel","confirm"]}`
	model, err := agent.BuildWidgetModel("Deploy?", json.RawMessage(schema), agent.WidgetState{Values: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := model.Encode()
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeInboundStore{
		requests: []workspace.HumanRequest{{
			ID: "run/turn/01/call_form", RunID: "run", ProjectID: "proj", ResolvedUser: "u1",
			Channel: "telegram", Status: workspace.HumanStatusPending,
			Question: "Deploy?", Schema: json.RawMessage(schema), CreatedAt: time.Now().Add(-time.Minute),
		}},
		users: []workspace.User{{
			ID: "u1", Channels: []workspace.UserChannel{{Type: "telegram", Address: "555", Enabled: true}},
		}},
		widgets: []workspace.ChannelWidget{{
			Channel: "telegram", ChatID: "555", MessageID: "77", RequestID: "run/turn/01/call_form",
			State: json.RawMessage(encoded),
		}},
	}
	sig := &recordedSignal{}
	loop, driver := newInteractiveLoop(t, store, sig)

	widgetMsg := func(id, op string) agent.InboundMessage {
		return agent.InboundMessage{
			ID: id, Channel: "telegram", ChatID: "555", Sender: "555",
			Kind: "widget_query", CallbackData: op, QueryID: "wq-" + id,
			SourceMessageID: "77", Timestamp: time.Now(),
		}
	}

	// set channel=canary
	loop.handleWidgetQuery(context.Background(), interactiveSettings(), widgetMsg("1", `{"op":"set","i":0,"v":"canary"}`))
	// set confirm=true
	loop.handleWidgetQuery(context.Background(), interactiveSettings(), widgetMsg("2", `{"op":"set","i":1,"v":true}`))
	if sig.count() != 0 {
		t.Fatalf("sets must not resolve: %+v", sig.calls)
	}
	// submit
	loop.handleWidgetQuery(context.Background(), interactiveSettings(), widgetMsg("3", `{"op":"submit"}`))

	if sig.count() != 1 {
		t.Fatalf("submit must resolve once: %+v", sig.calls)
	}
	if sig.calls[0].Response != `{"channel":"canary","confirm":true}` {
		t.Fatalf("response = %q", sig.calls[0].Response)
	}
	if store.answered["run/turn/01/call_form"] != `{"channel":"canary","confirm":true}` {
		t.Fatalf("row not answered: %+v", store.answered)
	}
	paths := driver.recorded()
	got := strings.Join(paths, ",")
	for _, want := range []string{"/answerWidgetQuery", "/editMessageWidgetData"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %v", want, paths)
		}
	}
	// The authoritative state advanced: the widget row now shows done.
	row, err := store.GetChannelWidget(context.Background(), "telegram", "555", "77")
	if err != nil {
		t.Fatal(err)
	}
	final, err := agent.ParseWidgetModel(string(row.State))
	if err != nil || !final.State.Done {
		t.Fatalf("final state = %s err=%v", row.State, err)
	}
}

// TestChannelWidgetQueryExpiredAndValidation pins the failure surfaces: an
// unknown widget acks expired (never crashes), an invalid submit pushes the
// field errors back into the widget instead of resolving.
func TestChannelWidgetQueryExpiredAndValidation(t *testing.T) {
	schema := `{"type":"object","properties":{"confirm":{"type":"boolean"}},"required":["confirm"]}`
	model, _ := agent.BuildWidgetModel("Deploy?", json.RawMessage(schema), agent.WidgetState{Values: map[string]any{}})
	encoded, _ := model.Encode()
	store := &fakeInboundStore{
		widgets: []workspace.ChannelWidget{{
			Channel: "telegram", ChatID: "555", MessageID: "77", RequestID: "gone", State: json.RawMessage(encoded),
		}},
	}
	sig := &recordedSignal{}
	loop, driver := newInteractiveLoop(t, store, sig)

	// Unknown widget placement.
	loop.handleWidgetQuery(context.Background(), interactiveSettings(), agent.InboundMessage{
		ID: "1", Channel: "telegram", ChatID: "888", Sender: "555",
		Kind: "widget_query", CallbackData: `{"op":"submit"}`, QueryID: "wq-x", SourceMessageID: "404", Timestamp: time.Now(),
	})
	if sig.count() != 0 {
		t.Fatalf("unknown widget must not resolve")
	}

	// Invalid submit (required missing): state pushed with errors, no resolve.
	loop.handleWidgetQuery(context.Background(), interactiveSettings(), agent.InboundMessage{
		ID: "2", Channel: "telegram", ChatID: "555", Sender: "555",
		Kind: "widget_query", CallbackData: `{"op":"submit"}`, QueryID: "wq-y", SourceMessageID: "77", Timestamp: time.Now(),
	})
	if sig.count() != 0 {
		t.Fatalf("invalid submit must not resolve: %+v", sig.calls)
	}
	row, err := store.GetChannelWidget(context.Background(), "telegram", "555", "77")
	if err != nil {
		t.Fatal(err)
	}
	final, err := agent.ParseWidgetModel(string(row.State))
	if err != nil {
		t.Fatal(err)
	}
	if final.Fields[0].Error == "" {
		t.Fatalf("field error must ride the pushed state: %s", row.State)
	}
	if !strings.Contains(strings.Join(driver.recorded(), ","), "/editMessageWidgetData") {
		t.Fatalf("state push missing: %v", driver.recorded())
	}
}

// TestChannelWidgetThrottle pins the politeness budget: the 6th query inside
// the window is refused with the rate ack.
func TestChannelWidgetThrottle(t *testing.T) {
	loop, _ := newInteractiveLoop(t, &fakeInboundStore{}, &recordedSignal{})
	sender := "555"
	allowed := 0
	for i := 0; i < 7; i++ {
		if loop.widgetThrottle(sender) {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("budget = %d, want 5", allowed)
	}
	if !loop.widgetThrottle("other") {
		t.Fatal("budget must be per sender")
	}
}
