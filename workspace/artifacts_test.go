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
