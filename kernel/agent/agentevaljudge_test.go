package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/kernel/llm"
)

// The judge is an external model: nothing that leaves for it may carry
// credentials (§10.3, CWE-200). The provider stub captures the exact
// request body; the suite input and the candidate answer carry secrets
// that must arrive masked.
func TestJudgePayloadMasksCredentialsBeforeProvider(t *testing.T) {
	captured := make(chan []byte, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured <- body
		w.Header().Set("Content-Type", "application/json")
		content := `{"score":80,"rationale":"looks good"}`
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":`+strconv.Quote(content)+`}}]}`)
	}))
	defer provider.Close()

	client := llm.New(llm.Config{BaseURL: provider.URL, Model: "judge-model", Timeout: 10 * time.Second})
	input := "Task: rotate the key. password=hunter2 and bearer sk-live-abc123"
	answer := "Done: api_key=super-secret sent to https://user:pass@provider.example/v1"
	reply, err := (&Activities{}).runJudgeCase(context.Background(), client, "", input, answer)
	require.NoError(t, err)
	require.Equal(t, 80, reply.Verdict.Score)

	body := string(<-captured)
	require.Contains(t, body, "[REDACTED]")
	require.NotContains(t, body, "hunter2")
	require.NotContains(t, body, "sk-live-abc123")
	require.NotContains(t, body, "super-secret")
	require.NotContains(t, body, "user:pass@")
}
