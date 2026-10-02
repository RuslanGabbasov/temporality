package agent

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestCleanTitle(t *testing.T) {
	cases := []struct{ name, raw, want string }{
		{"plain", "fix login timeout", "fix login timeout"},
		{"quoted", `"Fix login timeout"`, "Fix login timeout"},
		{"single quoted", "'Fix login timeout'", "Fix login timeout"},
		{"guillemets", "«Починить вход в систему»", "Починить вход в систему"},
		{"bold markdown", "**Fix login timeout**", "Fix login timeout"},
		{"title prefix", "Title: Fix login timeout", "Fix login timeout"},
		{"localized prefix", "Название: Починить вход", "Починить вход"},
		{"multiline answer", "Fix login timeout\n\nHere is a longer explanation.", "Fix login timeout"},
		{"think block", "<think>the user wants a title</think>\nfix login timeout", "fix login timeout"},
		{"trailing punctuation", "Fix login timeout.", "Fix login timeout"},
		{"collapsed whitespace", "Fix   login\ttimeout", "Fix login timeout"},
		{"cyrillic passes through", "Починить вход в систему", "Починить вход в систему"},
		{"empty", "   ", ""},
		{"only quotes", `""`, ""},
		{"long title is capped", strings.Repeat("а", 100), strings.Repeat("а", 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, cleanTitle(tc.raw))
		})
	}
}

func TestGenerateRunTitleWithoutModelClient(t *testing.T) {
	title, err := GenerateRunTitle(context.Background(), nil, "do the thing")
	require.NoError(t, err)
	require.Empty(t, title)
}

func TestTruncateRunesNeverSplitsCharacters(t *testing.T) {
	// Cyrillic is 2 bytes per rune: a byte-level cut would produce invalid UTF-8.
	long := strings.Repeat("ф", 10)
	cut := truncateRunes(long, 7)
	require.Equal(t, "ффффффф", cut)
	require.Equal(t, long, truncateRunes(long, 10))
	require.Equal(t, long, truncateRunes(long, 11))
	require.True(t, utf8.ValidString(cut))
}
