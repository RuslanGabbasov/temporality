package postgres

import (
	"reflect"
	"strings"
	"testing"
)

// The vector listing is built from pure query/argument framing so the filter
// is testable without a database: a pinned model filters by it, an empty model
// means the installation's single embedding space (any stored model), and a
// non-empty id list adds the knowledge_id = ANY(...) project filter that keeps
// one hints call from loading every project's vectors.
func TestKnowledgeEmbeddingSelect(t *testing.T) {
	const base = `SELECT knowledge_id, dim, embedding FROM observation_knowledge_embedding WHERE `

	t.Run("model and ids", func(t *testing.T) {
		query, args := knowledgeEmbeddingSelect("e5-small", []string{"k1", "k2"})
		if query != base+`model = $1 AND knowledge_id = ANY($2)` {
			t.Fatalf("query = %s", query)
		}
		if len(args) != 2 || args[0] != "e5-small" {
			t.Fatalf("args = %v", args)
		}
		if ids, ok := args[1].([]string); !ok || !reflect.DeepEqual(ids, []string{"k1", "k2"}) {
			t.Fatalf("id bind = %v", args[1])
		}
	})

	t.Run("empty model with ids matches any stored model", func(t *testing.T) {
		query, args := knowledgeEmbeddingSelect("  ", []string{"k1"})
		if query != base+`model <> '' AND knowledge_id = ANY($1)` {
			t.Fatalf("query = %s", query)
		}
		if ids, ok := args[1-1].([]string); !ok || !reflect.DeepEqual(ids, []string{"k1"}) {
			t.Fatalf("id bind = %v", args)
		}
	})

	t.Run("model without ids stays unfiltered by knowledge", func(t *testing.T) {
		query, args := knowledgeEmbeddingSelect("e5-small", nil)
		if query != base+`model = $1` {
			t.Fatalf("query = %s", query)
		}
		if len(args) != 1 || args[0] != "e5-small" {
			t.Fatalf("args = %v", args)
		}
		if where := strings.TrimPrefix(query, base); strings.Contains(where, "knowledge_id") {
			t.Fatal("an empty id filter must not constrain knowledge_id in the WHERE clause")
		}
	})

	t.Run("nothing pinned lists everything", func(t *testing.T) {
		query, args := knowledgeEmbeddingSelect("", nil)
		if query != base+`model <> ''` {
			t.Fatalf("query = %s", query)
		}
		if len(args) != 0 {
			t.Fatalf("args = %v", args)
		}
	})
}

// The id cap degrades to chunked ANY() arrays, never to dropping the filter.
func TestKnowledgeEmbeddingChunks(t *testing.T) {
	if got := knowledgeEmbeddingChunks(nil); len(got) != 0 {
		t.Fatalf("empty ids must produce no chunks, got %v", got)
	}
	ids := make([]string, knowledgeEmbeddingIDChunk+2)
	for i := range ids {
		ids[i] = "k"
	}
	chunks := knowledgeEmbeddingChunks(ids)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if len(chunks[0]) != knowledgeEmbeddingIDChunk {
		t.Fatalf("first chunk = %d ids, want %d", len(chunks[0]), knowledgeEmbeddingIDChunk)
	}
	if len(chunks[1]) != 2 {
		t.Fatalf("tail chunk = %d ids, want 2", len(chunks[1]))
	}
	// Exactly at the cap: one chunk, no empty tail.
	exact := make([]string, knowledgeEmbeddingIDChunk)
	if chunks := knowledgeEmbeddingChunks(exact); len(chunks) != 1 || len(chunks[0]) != knowledgeEmbeddingIDChunk {
		t.Fatalf("exact cap must be one chunk, got %d chunks", len(chunks))
	}
}
