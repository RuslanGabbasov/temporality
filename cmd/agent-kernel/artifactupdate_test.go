package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/temporality-project/temporality/workspace"
)

// A fragment patch carries only the body — the create-time validator inside
// toArtifact must not demand a title for an UPDATE (the incident:
// artifact_update old_string/new_string answered 422 «title is required»),
// and omitted fields must KEEP the stored values instead of blanking them.
func TestKeepArtifactEditsOnEmptyFillsBeforeValidation(t *testing.T) {
	current := workspace.Artifact{ID: "doc", Title: "Регламент", Description: "описание", Provider: "builtin", Content: "# тело", MimeType: "text/markdown", Kind: "work"}

	// The patch case: only the body travels.
	req := artifactRequest{Content: "# тело\nновый абзац"}
	keepArtifactEditsOnEmpty(&req, current)
	artifact, err := req.toArtifact(current.ID)
	require.NoError(t, err, "a title-less patch must pass validation after keep-on-empty")
	require.Equal(t, "Регламент", artifact.Title, "an omitted title must be kept, not blanked")
	require.Equal(t, "описание", artifact.Description)
	require.Equal(t, "text/markdown", artifact.MimeType)
	require.Equal(t, "# тело\nновый абзац", artifact.Content)

	// Explicit values win over the kept ones.
	req = artifactRequest{Title: "Новое имя", Content: "# тело"}
	keepArtifactEditsOnEmpty(&req, current)
	artifact, err = req.toArtifact(current.ID)
	require.NoError(t, err)
	require.Equal(t, "Новое имя", artifact.Title)

	// A url artifact keeps its body outside the registry: no content
	// backfill (it would trip the "content must be empty" rule), and an
	// omitted external_ref keeps the stored one.
	urlCurrent := workspace.Artifact{ID: "ext", Title: "Внешний", Provider: "url", ExternalRef: "https://example.com/doc"}
	req = artifactRequest{}
	keepArtifactEditsOnEmpty(&req, urlCurrent)
	_, err = req.toArtifact(urlCurrent.ID)
	require.NoError(t, err, "a url artifact edit must demand neither title nor content")
	require.Equal(t, "https://example.com/doc", req.ExternalRef)
	require.Empty(t, req.Content)
}

// Omitted tags keep the stored set; an explicit array still replaces it.
// The incident: the agent's artifact_update never sends tags, and the edit
// path wrote the payload unconditionally — every body patch silently wiped
// the catalog labels the filters search by.
func TestKeepArtifactEditsOnEmptyKeepsTags(t *testing.T) {
	current := workspace.Artifact{ID: "doc", Title: "Регламент", Provider: "builtin", Content: "# тело", Tags: []string{"go", "quality"}}

	// Omitted (nil) — the agent-tool case: tags survive the edit.
	req := artifactRequest{Content: "# тело v2"}
	keepArtifactEditsOnEmpty(&req, current)
	require.Equal(t, []string{"go", "quality"}, req.Tags)

	// An explicit array replaces the set — "[]" clears, like SetArtifactTags.
	req = artifactRequest{Content: "# тело v2", Tags: []string{}}
	keepArtifactEditsOnEmpty(&req, current)
	require.NotNil(t, req.Tags)
	require.Empty(t, req.Tags)

	req = artifactRequest{Content: "# тело v2", Tags: []string{"renewed"}}
	keepArtifactEditsOnEmpty(&req, current)
	require.Equal(t, []string{"renewed"}, req.Tags)
}
