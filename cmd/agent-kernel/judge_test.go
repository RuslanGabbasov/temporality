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

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
	"github.com/temporality-project/temporality/workspace"
)

// fakeJudgeStore satisfies the judge endpoints' storage surface without a
// database: the resolved config, the provider catalog for validation and the
// settings writes the PUT performs.
type fakeJudgeStore struct {
	config   workspace.JudgeConfig
	models   map[string]bool // models listed by some configured provider
	settings map[string]string
	setErr   error
}

func (f *fakeJudgeStore) JudgeConfig(_ context.Context) (workspace.JudgeConfig, error) {
	return f.config, nil
}

func (f *fakeJudgeStore) ProviderOfModel(_ context.Context, model string) (string, bool, error) {
	return "prov", f.models[model], nil
}

func (f *fakeJudgeStore) SetSetting(_ context.Context, key, value string) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.settings == nil {
		f.settings = map[string]string{}
	}
	f.settings[key] = value
	return nil
}

// captureJudgeOutbox records the config events for assertions.
type captureJudgeOutbox struct {
	events []observation.Event
}

func (o *captureJudgeOutbox) Enqueue(_ context.Context, item observation.Event) error {
	o.events = append(o.events, item)
	return nil
}

// newJudgeServer mounts the judge endpoints the way main wires them — real
// RBAC gate, fake store/outbox.
func newJudgeServer(t *testing.T, store *fakeJudgeStore, events judgeOutbox) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate("readertoken:reader:reader:*\nwritertoken:writer:writer:*\noperatortoken:operator:operator:*")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterJudgeRoutes(mux, gate, store, events, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return httptest.NewServer(gate.Authenticate(mux))
}

func judgeRequest(t *testing.T, server *httptest.Server, method, path, token, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(raw)
}

// The configuration endpoint reports the kernel's judge state: the model
// resolution a judge-less installation turns into configured=false.
func TestJudgeConfigEndpointReportsUnconfigured(t *testing.T) {
	store := &fakeJudgeStore{config: workspace.JudgeConfig{Model: "judge-x", Configured: false}}
	server := newJudgeServer(t, store, nil)
	defer server.Close()

	status, body := judgeRequest(t, server, http.MethodGet, "/v1/workspace/evaluation-judge", "readertoken", "")
	require.Equal(t, 200, status)

	var config workspace.JudgeConfig
	require.NoError(t, json.Unmarshal([]byte(body), &config))
	require.Equal(t, "judge-x", config.Model)
	require.False(t, config.Configured)
}

// The PUT validates the model against the provider catalog before saving —
// an unknown model is a 422 and must not overwrite the setting.
func TestJudgeConfigPutRejectsUnknownModel(t *testing.T) {
	store := &fakeJudgeStore{models: map[string]bool{"known": true}}
	server := newJudgeServer(t, store, &captureJudgeOutbox{})
	defer server.Close()

	status, _ := judgeRequest(t, server, http.MethodPut, "/v1/workspace/evaluation-judge", "operatortoken", `{"model":"unknown"}`)
	require.Equal(t, 422, status)
	require.Empty(t, store.settings)
}

// The PUT saves the setting and journals the config event; operator role.
func TestJudgeConfigPutSavesAndEmits(t *testing.T) {
	store := &fakeJudgeStore{
		models:   map[string]bool{"judge-x": true},
		config:   workspace.JudgeConfig{Model: "judge-x", ProviderID: "prov", Configured: true},
		settings: map[string]string{},
	}
	outbox := &captureJudgeOutbox{}
	server := newJudgeServer(t, store, outbox)
	defer server.Close()

	status, _ := judgeRequest(t, server, http.MethodPut, "/v1/workspace/evaluation-judge", "operatortoken", `{"model":"judge-x"}`)
	require.Equal(t, 200, status)
	require.Equal(t, "judge-x", store.settings[workspace.SettingJudgeModel])
	require.Len(t, outbox.events, 1)
	require.Equal(t, "agent.evaluation.judge.configured", outbox.events[0].Type)

	// A writer may not write the installation setting — operator only.
	status, _ = judgeRequest(t, server, http.MethodPut, "/v1/workspace/evaluation-judge", "writertoken", `{"model":"judge-x"}`)
	require.Equal(t, 403, status)
}
