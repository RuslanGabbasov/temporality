package workspace

// A live-database test for UpdateArtifactMeta — the metadata-only edit path
// that must work in ANY status (a review locks the body, never the name) and
// must keep the open revision draft in sync so promotion cannot clobber a
// rename with its stale snapshot. Skipped unless TEMPORALITY_TEST_DATABASE_URL
// points at a migrated schema.

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateArtifactMetaLive(t *testing.T) {
	dsn := os.Getenv("TEMPORALITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEMPORALITY_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, dsn)
	require.NoError(t, err)

	artifact := &Artifact{
		ID: "meta-live-test", Title: "До", Description: "было",
		Provider: "builtin", Content: "тело", Status: ArtifactApproved, Kind: ArtifactKindReference,
	}
	require.NoError(t, store.CreateArtifact(ctx, artifact))
	t.Cleanup(func() { _ = store.DeleteArtifact(ctx, artifact.ID) })

	// An open revision (draft v2 with a body edit) — the rename must reach
	// both the live row and the pending draft that promotion later writes back.
	require.NoError(t, store.SaveArtifactNewVersion(ctx, Artifact{
		ID: artifact.ID, Title: "До", Description: "было",
		Provider: "builtin", Content: "тело v2", Kind: ArtifactKindReference,
	}))

	renamed := *artifact
	renamed.Title = "После"
	renamed.Description = "стало"
	renamed.Kind = ArtifactKindWork
	renamed.Tags = []string{"live"}
	require.NoError(t, store.UpdateArtifactMeta(ctx, renamed))

	live, err := store.GetArtifactRange(ctx, artifact.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, "После", live.Title)
	require.Equal(t, "стало", live.Description)
	require.Equal(t, ArtifactKindWork, live.Kind)
	require.Equal(t, []string{"live"}, live.Tags)
	require.Equal(t, "тело", live.Content, "the body stays untouched — the revision carries the edit")
	require.Equal(t, ArtifactApproved, live.Status, "a rename must not move the version state machine")

	pending, err := store.PendingArtifactRevision(ctx, artifact.ID)
	require.NoError(t, err)
	require.Equal(t, "После", pending.Title, "the open revision carries the rename to promotion")

	missing := renamed
	missing.ID = "meta-live-missing"
	require.ErrorIs(t, store.UpdateArtifactMeta(ctx, missing), ErrNotFound)
}
