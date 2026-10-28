package workspace

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestHashArtifactContentStable(t *testing.T) {
	require.Equal(t, HashArtifactContent("body"), HashArtifactContent("body"))
	require.NotEqual(t, HashArtifactContent("body"), HashArtifactContent("body2"))
	require.Len(t, HashArtifactContent("x"), 64) // sha256 hex
}

// normalize is the derived-fields contract: builtin artifacts carry hash and
// size, reference-only providers must not pretend they have a local body.
func TestArtifactNormalizeByProvider(t *testing.T) {
	builtin := &Artifact{Provider: "builtin", Content: "правило"}
	builtin.normalize()
	require.Equal(t, HashArtifactContent("правило"), builtin.ContentHash)
	require.Equal(t, int64(len("правило")), builtin.SizeBytes)
	require.Equal(t, "text/markdown", builtin.MimeType) // default

	external := &Artifact{Provider: "url", Content: "ignored", ContentHash: "ignored", SizeBytes: 42}
	external.normalize()
	require.Empty(t, external.Content)
	require.Empty(t, external.ContentHash)
	require.Zero(t, external.SizeBytes)
}

// chunk must never split a multi-byte character: a cut continuation byte
// decodes as RuneError and the loop backs off to the last complete rune.
func TestArtifactChunkUTF8Boundary(t *testing.T) {
	// "миграция " = 8 two-byte runes + 1-byte space = 17 bytes per group.
	russian := strings.Repeat("миграция ", 100)
	a := Artifact{Content: russian}

	// 100 = 17*5+15: the cut lands after the FIRST byte of "я" — mid-rune,
	// so the boundary backs off one byte to 99.
	a.chunk(0, 100)
	require.Equal(t, 99, len(a.Content))
	require.True(t, utf8.ValidString(a.Content))

	// 102 = 17*6: rune-aligned cut passes through unchanged.
	a6 := Artifact{Content: russian}
	a6.chunk(0, 102)
	require.Equal(t, 102, len(a6.Content))
	require.True(t, utf8.ValidString(a6.Content))

	// 4-byte runes (emoji) back off up to 3 bytes.
	emoji := strings.Repeat("\U0001F600", 100)
	a3 := Artifact{Content: emoji}
	a3.chunk(0, 6) // would cut the second emoji in half
	require.True(t, utf8.ValidString(a3.Content))
	require.Equal(t, "\U0001F600", a3.Content)

	// Offset past the end clamps instead of panicking.
	a4 := Artifact{Content: "short"}
	a4.chunk(1000, 10)
	require.Empty(t, a4.Content)

	// limit<=0 returns the rest of the body from offset.
	a5 := Artifact{Content: "abcdef"}
	a5.chunk(3, 0)
	require.Equal(t, "def", a5.Content)
}

// ValidateArtifactReviewers is the shared ask_human recipient contract: at
// least one, non-empty, unique. org:/role: entries pass through unresolved —
// expansion happens at notification time.
func TestValidateArtifactReviewers(t *testing.T) {
	require.Error(t, ValidateArtifactReviewers(nil))
	require.Error(t, ValidateArtifactReviewers([]string{}))
	require.Error(t, ValidateArtifactReviewers([]string{"", "user:ivanov"}))
	require.Error(t, ValidateArtifactReviewers([]string{"user:ivanov", "user:ivanov"}))
	require.NoError(t, ValidateArtifactReviewers([]string{"user:ivanov"}))
	require.NoError(t, ValidateArtifactReviewers([]string{"org:platform", "role:lead", "project_owner", "writer"}))
}

// MatchArtifactReviewers is the decision-side half of the ask_human recipient
// syntax: entries are satisfied by the principal's profile, group entries by
// any member, and an account without an active row satisfies nothing.
func TestMatchArtifactReviewers(t *testing.T) {
	users := []User{
		{ID: "writer", Name: "Olga", Role: "writer", OrgUnitID: "core", Active: true},
		{ID: "admin", Name: "Ruslan", Role: "admin", OrgUnitID: "core", Active: true},
		{ID: "ghost", Name: "Ghost", Role: "writer", Active: false},
	}
	entries := []string{"user:writer", "writer", "Olga", "org:core", "role:admin", "project_owner", "user:admin"}

	matched := MatchArtifactReviewers("writer", users, entries)
	require.Contains(t, matched, "user:writer")
	require.Contains(t, matched, "writer")
	require.Contains(t, matched, "Olga")
	require.Contains(t, matched, "org:core")
	require.NotContains(t, matched, "role:admin")
	require.NotContains(t, matched, "project_owner")

	matched = MatchArtifactReviewers("admin", users, entries)
	require.Contains(t, matched, "org:core")
	require.Contains(t, matched, "role:admin")
	require.Contains(t, matched, "project_owner")
	require.Contains(t, matched, "user:admin")

	// Name-based principal resolution: the token subject may be the login name.
	matched = MatchArtifactReviewers("Ruslan", users, entries)
	require.Contains(t, matched, "role:admin")

	require.Empty(t, MatchArtifactReviewers("nobody", users, entries))
	// Deactivated accounts never decide.
	require.Empty(t, MatchArtifactReviewers("ghost", users, []string{"user:ghost", "role:writer"}))
	require.Empty(t, MatchArtifactReviewers("writer", []User{{ID: "writer"}}, entries))

	// Case-insensitive matching: reviewer entries are model-typed, and
	// "Ivanov" vs "ivanov" must not void a review.
	caseUsers := []User{
		{ID: "user-2d08", Name: "Ivan Petrov", Role: "Writer", OrgUnitID: "Core", Active: true},
	}
	caseEntries := []string{"user:USER-2D08", "ivan petrov", "role:WRITER", "org:CORE"}
	matched = MatchArtifactReviewers("USER-2D08", caseUsers, caseEntries)
	require.Contains(t, matched, "user:USER-2D08")
	require.Contains(t, matched, "ivan petrov")
	require.Contains(t, matched, "role:WRITER")
	require.Contains(t, matched, "org:CORE")
}

// CheckArtifactRefsApproved names the offending manifest references: drafts
// and missing rows both block the skill write with a 422.
func TestCheckArtifactRefsApproved(t *testing.T) {
	approved := []Artifact{
		{ID: "coding-standard", Status: ArtifactApproved},
		{ID: "release-checklist", Status: ArtifactApproved},
	}
	require.NoError(t, CheckArtifactRefsApproved(approved, []string{"coding-standard"}))
	require.NoError(t, CheckArtifactRefsApproved(approved, nil))

	draftInList := []Artifact{
		{ID: "req-spec", Status: ArtifactDraft},
	}
	err := CheckArtifactRefsApproved(draftInList, []string{"req-spec"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "req-spec: draft")

	err = CheckArtifactRefsApproved(nil, []string{"ghost"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "ghost: missing")
}
