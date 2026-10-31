package workspace

// A live-database test for ListArtifactReviewInbox — the ONLY coverage the
// inbox scan ever had was unit-level, and both production failures (ambiguous
// id, NULL into *string) were SQL/scan-shape bugs no unit test could see.
// Skipped unless TEMPORALITY_TEST_DATABASE_URL points at a migrated schema.

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListArtifactReviewInboxLive(t *testing.T) {
	dsn := os.Getenv("TEMPORALITY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEMPORALITY_TEST_DATABASE_URL not set")
	}
	store, err := Open(context.Background(), dsn)
	require.NoError(t, err)

	users, err := store.ListUsers(context.Background())
	require.NoError(t, err)

	entries, err := store.ListArtifactReviewInbox(context.Background(), "user-1", users, nil)
	require.NoError(t, err, "the inbox scan must survive NULL origin_run / nullable artifact columns")

	byReview := map[string]ArtifactInboxEntry{}
	for _, e := range entries {
		byReview[e.Review.ID] = e
	}
	// The admin satisfies project_owner (role admin) and org:rd (head+member).
	e, ok := byReview["rev-null"]
	require.True(t, ok, "the NULL-origin_run round must be in the inbox")
	require.Contains(t, e.Matched, "project_owner")
	require.Empty(t, e.OriginRun)
	require.Empty(t, e.OriginUser)

	e, ok = byReview["rev-full"]
	require.True(t, ok, "the round with full origin fields must be in the inbox")
	require.Contains(t, e.Matched, "user:user-1")
	require.Contains(t, e.Matched, "org:rd")
	require.Equal(t, "run-1", e.OriginRun)
}
