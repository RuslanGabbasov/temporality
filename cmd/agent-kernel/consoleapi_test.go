package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
)

// consoleTestServer mounts the console routes the way main wires them: the
// real RBAC gate in front, a registry seeded by the test behind it.
func consoleTestServer(t *testing.T, console *agent.Console) *httptest.Server {
	t.Helper()
	gate, err := controlplane.NewGate(
		"admintoken:admin:admin:*\n" +
			"readertoken:reader:reader:*\n" +
			"projecttoken:scoped:reader:proj-a")
	require.NoError(t, err)
	mux := http.NewServeMux()
	registerConsoleRoutes(mux, consoleDeps{console: console, gate: gate})
	server := httptest.NewServer(gate.Authenticate(mux))
	t.Cleanup(server.Close)
	return server
}

func consoleGet(t *testing.T, server *httptest.Server, token, path string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
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

func TestConsoleSnapshotEndpointContract(t *testing.T) {
	console := agent.NewConsole()
	console.Write("p1", "op-json", "stdout", []byte("out-line\n"))
	console.Write("p1", "op-json", "stderr", []byte("err-line\n"))
	code := 3
	console.Done("p1", "op-json", code)
	server := consoleTestServer(t, console)

	status, body := consoleGet(t, server, "readertoken", "/v1/agent/console/op-json?project=p1")
	require.Equal(t, 200, status, body)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &payload), body)
	require.Equal(t, "op-json", payload["operation_id"])
	require.Equal(t, "console://op-json", payload["console_uri"], "the approved §2 scheme must ride the response")
	require.Equal(t, "out-line\n", payload["stdout_tail"])
	require.Equal(t, "err-line\n", payload["stderr_tail"])
	require.InDelta(t, float64(len("out-line\n")+len("err-line\n")), payload["bytes"], 0)
	require.InDelta(t, float64(2), payload["lines"], 0)
	require.Equal(t, false, payload["truncated"])
	require.Equal(t, true, payload["done"])
	require.InDelta(t, float64(code), payload["exit_code"], 0)
}

func TestConsoleSnapshotUnknownOperationIs404(t *testing.T) {
	server := consoleTestServer(t, agent.NewConsole())
	status, body := consoleGet(t, server, "readertoken", "/v1/agent/console/missing?project=p1")
	require.Equal(t, 404, status, body)
}

func TestConsoleStreamDeliversFramesAndDoneWithExitCode(t *testing.T) {
	console := agent.NewConsole()
	console.Write("p1", "op-live", "stdout", []byte("first\n"))
	server := consoleTestServer(t, console)

	responseCh := make(chan string, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/agent/console/op-live/stream?project=p1", nil)
		request.Header.Set("Authorization", "Bearer readertoken")
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		responseCh <- string(raw)
	}()
	// Give the handler time to attach its subscription, then stream and finish.
	time.Sleep(200 * time.Millisecond)
	console.Write("p1", "op-live", "stderr", []byte("boom\n"))
	console.Done("p1", "op-live", 7)

	select {
	case body := <-responseCh:
		require.Contains(t, body, "event: stdout\ndata: {\"chunk\":\"first\\n\"}", body)
		require.Contains(t, body, "event: stderr\ndata: {\"chunk\":\"boom\\n\"}", body)
		require.Contains(t, body, "event: done\ndata: {\"exit_code\":7}", body)
		// done must close the stream: it is the last frame.
		require.True(t, strings.HasSuffix(body, "event: done\ndata: {\"exit_code\":7}\n\n"))
	case <-time.After(5 * time.Second):
		t.Fatal("the stream must end once the operation is done")
	}
}

func TestConsoleStreamTailTrueSkipsBacklog(t *testing.T) {
	console := agent.NewConsole()
	console.Write("p1", "op-tail", "stdout", []byte("old\n"))
	server := consoleTestServer(t, console)

	responseCh := make(chan string, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/agent/console/op-tail/stream?project=p1&tail=true", nil)
		request.Header.Set("Authorization", "Bearer readertoken")
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		responseCh <- string(raw)
	}()
	time.Sleep(200 * time.Millisecond)
	console.Write("p1", "op-tail", "stdout", []byte("new\n"))
	console.Done("p1", "op-tail", 0)

	select {
	case body := <-responseCh:
		require.NotContains(t, body, "old\n", "tail=true must not replay the backlog")
		require.Contains(t, body, "event: stdout\ndata: {\"chunk\":\"new\\n\"}", body)
		// Exit code 0 is a real result, not "unknown": the done frame must
		// carry it explicitly (the §6 contract — done-фрейм c exit_code).
		require.Contains(t, body, "event: done\ndata: {\"exit_code\":0}", body)
	case <-time.After(5 * time.Second):
		t.Fatal("the stream must end once the operation is done")
	}
}

func TestConsoleStreamFinishedOperationStillEndsWithDone(t *testing.T) {
	// A subscriber arriving after completion gets the tail backlog and the
	// done frame — no hang, no error (the §6 "buffer tail after completion").
	console := agent.NewConsole()
	console.Write("p1", "op-finished", "stdout", []byte("final\n"))
	console.Done("p1", "op-finished", 2)
	server := consoleTestServer(t, console)

	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/agent/console/op-finished/stream?project=p1", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer readertoken")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	body := string(raw)
	require.Contains(t, body, "event: stdout\ndata: {\"chunk\":\"final\\n\"}", body)
	require.Contains(t, body, "event: done\ndata: {\"exit_code\":2}", body)
}

func TestConsoleStreamSubscribesBeforeFirstChunk(t *testing.T) {
	// §6 lazy flow: the UI attaches when the run_command row expands —
	// typically before the command has produced any output (the journal
	// event only appears after the tool returns). The subscription must
	// register the operation and go live with the first chunk; a framed
	// error here would break the primary "watch it run" scenario.
	console := agent.NewConsole()
	server := consoleTestServer(t, console)

	responseCh := make(chan string, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/agent/console/op-early/stream?project=p1", nil)
		request.Header.Set("Authorization", "Bearer readertoken")
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		responseCh <- string(raw)
	}()
	time.Sleep(200 * time.Millisecond)
	console.Write("p1", "op-early", "stdout", []byte("live\n"))
	console.Done("p1", "op-early", 0)

	select {
	case body := <-responseCh:
		require.Contains(t, body, "event: stdout\ndata: {\"chunk\":\"live\\n\"}", body)
		require.Contains(t, body, "event: done\ndata: {\"exit_code\":0}", body)
	case <-time.After(5 * time.Second):
		t.Fatal("the stream must end once the operation is done")
	}
}

func TestConsoleStreamTailParamAcceptsClientSpelling(t *testing.T) {
	// The stream client sends ?tail=1 (consoleStream.ts); the contract
	// spelling "true" must behave identically — the backlog stays out.
	console := agent.NewConsole()
	console.Write("p1", "op-one", "stdout", []byte("old\n"))
	server := consoleTestServer(t, console)

	responseCh := make(chan string, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/agent/console/op-one/stream?project=p1&tail=1", nil)
		request.Header.Set("Authorization", "Bearer readertoken")
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		responseCh <- string(raw)
	}()
	time.Sleep(200 * time.Millisecond)
	console.Write("p1", "op-one", "stdout", []byte("new\n"))
	console.Done("p1", "op-one", 0)

	select {
	case body := <-responseCh:
		require.NotContains(t, body, "old\n", "?tail=1 must skip the backlog like ?tail=true")
		require.Contains(t, body, "event: stdout\ndata: {\"chunk\":\"new\\n\"}", body)
	case <-time.After(5 * time.Second):
		t.Fatal("the stream must end once the operation is done")
	}
}

func TestConsoleStreamNilRegistryFramedError(t *testing.T) {
	// Only a nil registry can fail a subscribe now (an unknown operation
	// auto-registers); SSE headers are already out, so the miss is a framed
	// error event.
	server := consoleTestServer(t, nil)
	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/agent/console/missing/stream?project=p1", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer readertoken")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(raw), "event: error")
}

// ── Authorization: the same wall as GET /v1/agent/runs ───────────────────────

func TestConsoleAuthorization(t *testing.T) {
	console := agent.NewConsole()
	console.Write("p1", "op-auth", "stdout", []byte("secret-ish output"))
	server := consoleTestServer(t, console)

	t.Run("enabled gate rejects anonymous", func(t *testing.T) {
		status, _ := consoleGet(t, server, "", "/v1/agent/console/op-auth?project=p1")
		require.Equal(t, 401, status)
	})
	t.Run("foreign project token is forbidden", func(t *testing.T) {
		status, _ := consoleGet(t, server, "projecttoken", "/v1/agent/console/op-auth?project=p1")
		require.Equal(t, 403, status)
	})
	t.Run("missing project parameter is 400", func(t *testing.T) {
		status, _ := consoleGet(t, server, "readertoken", "/v1/agent/console/op-auth")
		require.Equal(t, 400, status)
	})
	t.Run("authorized reader sees the tail", func(t *testing.T) {
		status, body := consoleGet(t, server, "readertoken", "/v1/agent/console/op-auth?project=p1")
		require.Equal(t, 200, status)
		require.Contains(t, body, "secret-ish output")
	})
	t.Run("stream endpoint enforces the same wall", func(t *testing.T) {
		status, _ := consoleGet(t, server, "projecttoken", "/v1/agent/console/op-auth/stream?project=p1")
		require.Equal(t, 403, status)
		status, _ = consoleGet(t, server, "", "/v1/agent/console/op-auth/stream?project=p1")
		require.Equal(t, 401, status)
	})
}

func TestConsoleProjectIsolationOverHTTP(t *testing.T) {
	console := agent.NewConsole()
	console.Write("p1", "op-shared", "stdout", []byte("from-p1"))
	server := consoleTestServer(t, console)
	status, body := consoleGet(t, server, "readertoken", "/v1/agent/console/op-shared?project=p2")
	require.Equal(t, 404, status, "another project's operation id must not resolve: %s", body)
}
