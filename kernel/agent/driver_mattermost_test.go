package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mattermostSettings() map[string]string {
	return map[string]string{"base_url": "https://mm.example.com", "token": "tok", "outgoing_token": "out-tok"}
}

func mattermostForm(values url.Values) (string, []byte) {
	body := values.Encode()
	req, _ := http.NewRequest(http.MethodPost, "/v1/channels/mattermost/hooks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return body, []byte(body)
}

func TestMattermostOutgoingVerifyRequest(t *testing.T) {
	hook := mattermostOutgoingWebhook{}
	newRequest := func(body string) *http.Request {
		req, _ := http.NewRequest(http.MethodPost, "/v1/channels/mattermost/hooks", strings.NewReader(body))
		return req
	}

	body, raw := mattermostForm(url.Values{
		"token": {"out-tok"}, "channel_id": {"ch1"}, "user_id": {"u1"}, "text": {"сделай отчёт"},
	})
	if err := hook.VerifyRequest(newRequest(body), raw, mattermostSettings()); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	body, raw = mattermostForm(url.Values{
		"token": {"wrong"}, "channel_id": {"ch1"}, "user_id": {"u1"}, "text": {"x"},
	})
	if err := hook.VerifyRequest(newRequest(body), raw, mattermostSettings()); err == nil {
		t.Fatal("wrong token accepted")
	}
	body, raw = mattermostForm(url.Values{
		"token": {"out-tok"}, "channel_id": {"ch1"}, "user_id": {"u1"}, "text": {"x"},
	})
	if err := hook.VerifyRequest(newRequest(body), raw, map[string]string{"base_url": "https://mm", "token": "tok"}); err == nil {
		t.Fatal("missing outgoing token accepted")
	}
}

func TestMattermostOutgoingParse(t *testing.T) {
	hook := mattermostOutgoingWebhook{}

	t.Run("trigger word is stripped", func(t *testing.T) {
		_, raw := mattermostForm(url.Values{
			"token": {"out-tok"}, "channel_id": {"ch1"}, "user_id": {"u9"},
			"text": {"!agent сделай смету на разработку"}, "trigger_word": {"!agent"},
			"post_id": {"post1"}, "timestamp": {"2026-10-12T10:00:00Z"},
		})
		messages := hook.Parse(nil, raw)
		if len(messages) != 1 {
			t.Fatalf("expected one message, got %+v", messages)
		}
		m := messages[0]
		if m.ID != "post1" || m.Channel != "mattermost" || m.ChatID != "ch1" || m.Sender != "u9" {
			t.Fatalf("unexpected message: %+v", m)
		}
		if m.Text != "сделай смету на разработку" {
			t.Fatalf("trigger word not stripped: %q", m.Text)
		}
		if !m.Timestamp.Equal(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)) {
			t.Fatalf("timestamp not parsed: %v", m.Timestamp)
		}
	})

	t.Run("missing identity yields nothing", func(t *testing.T) {
		_, raw := mattermostForm(url.Values{"token": {"out-tok"}, "channel_id": {"ch1"}, "text": {"x"}})
		if got := hook.Parse(nil, raw); len(got) != 0 {
			t.Fatalf("expected none, got %+v", got)
		}
	})

	t.Run("synthetic id without post_id", func(t *testing.T) {
		_, raw := mattermostForm(url.Values{
			"token": {"out-tok"}, "channel_id": {"ch1"}, "user_id": {"u1"}, "text": {"hello"},
		})
		messages := hook.Parse(nil, raw)
		if len(messages) != 1 || !strings.HasPrefix(messages[0].ID, "mm-") {
			t.Fatalf("expected synthetic id, got %+v", messages)
		}
	})
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
