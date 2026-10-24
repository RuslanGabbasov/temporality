package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/observation"
)

// embeddingStoreFake records the last upsert; the handler type-asserts the
// store to the embeddingsStore interface, so the fake must satisfy exactly it.
type embeddingStoreFake struct {
	observation.Store // panics on use — only the embeddings surface is real
	stored            map[string][]float32
	model             string
}

func (f *embeddingStoreFake) UpsertKnowledgeEmbeddings(_ context.Context, model string, items map[string][]float32) error {
	f.model = model
	f.stored = items
	return nil
}

func (f *embeddingStoreFake) ListKnowledgeEmbeddingGaps(context.Context, string, int) ([]observation.KnowledgeEmbeddingGap, error) {
	return nil, nil
}

// A realistic vector batch (256 × 384 float32 as JSON) is several megabytes —
// well past the default 1 MiB decode cap. The batch endpoint must accept it:
// a cap-truncated body decoded as "unexpected EOF" and every backfill pass
// failed with HTTP 400 (kernel log: embedding backfill: publish).
func TestPutKnowledgeEmbeddingsLargeBatch(t *testing.T) {
	store := &embeddingStoreFake{}
	gate, err := controlplane.NewGate("writertoken-1234567890:kernel:writer:*")
	require.NoError(t, err)
	handler := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), gate)

	const count, dim = 512, 384
	items := make(map[string][]float32, count)
	for i := 0; i < count; i++ {
		vector := make([]float32, dim)
		for j := range vector {
			// Full-precision mantissas: a float32 marshals with up to ~17
			// significant digits, which is exactly how real embeddings travel.
			vector[j] = float32(i*7+j%53) / 53.0 + float32(j%11)*0.1234567890123
		}
		items[strconv.Itoa(i)] = vector
	}
	payload, err := json.Marshal(map[string]any{"model": "e5-small", "items": items})
	require.NoError(t, err)
	require.Greater(t, len(payload), 1<<20, "the fixture must exceed the default decode cap for the test to mean anything")

	request := httptest.NewRequest(http.MethodPut, "/v1/observations/knowledge/embeddings", strings.NewReader(string(payload)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer writertoken-1234567890")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, "body = %s", recorder.Body.String())
	require.Len(t, store.stored, count)
	require.Equal(t, "e5-small", store.model)
}
