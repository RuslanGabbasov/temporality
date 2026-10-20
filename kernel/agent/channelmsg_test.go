package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDetectLang: the dialog follows the user's script — a meaningful share
// of Cyrillic makes it Russian, everything else stays English. Emoji, code
// and project names never flip the decision.
func TestDetectLang(t *testing.T) {
	cases := map[string]string{
		"Что ты знаешь про Directum Omni?": "ru",
		"подготовь смету":                  "ru",
		"и ещё проверь кэш":                "ru",
		"Write a test for the auth flow":   "en",
		"":                                 "en",
		"plain123 !@#":                     "en",
		"Запусти `go test ./...` и скажи что упало": "ru",
		"лом":                "ru", // a single word is still a decision
		"fix the bug потом":  "ru", // a latin word inside russian text keeps ru
		"fix the bug please": "en",
	}
	for text, want := range cases {
		if got := DetectLang(text); got != want {
			t.Errorf("DetectLang(%q) = %q, want %q", text, got, want)
		}
	}
}

// TestChannelTextCatalogParity: every key carries both translations and
// renders its arguments; unknown keys and languages fall back to English.
func TestChannelTextCatalogParity(t *testing.T) {
	for key, entry := range channelMessages {
		require.NotEmpty(t, entry[0], "key %q misses the English line", key)
		require.NotEmpty(t, entry[1], "key %q misses the Russian line", key)
	}
	require.Contains(t, ChannelText("ru", "phase_start_task", "omni"), "«omni»")
	require.Contains(t, ChannelText("en", "notice_rate_limit", 7), "7")
	require.Equal(t, "", ChannelText("en", "no_such_key"))
	// Unknown language falls back to English, not to an empty string.
	require.Contains(t, ChannelText("de", "on_it"), "On it")
	// The phase lines never embed newlines (chat one-liners).
	for key := range channelMessages {
		if strings.HasPrefix(key, "phase_") {
			require.NotContains(t, ChannelText("ru", key, "x"), "\n")
		}
	}
}

// TestAskEnvelopeFollowsQuestionLanguage: the HITL envelope (header, labels)
// is rendered in the language of the question — the model writes it in the
// conversation's language.
func TestAskEnvelopeFollowsQuestionLanguage(t *testing.T) {
	request := NotifyChannelRequest{
		Question: "Какую базу данных использовать — postgres или sqlite?",
		Context:  "задача про миграции",
		Project:  "omni",
		RunID:    "run-1",
	}
	text := notificationText(request, "http://ui")
	require.Contains(t, text, "агент ждёт вашего ответа")
	require.Contains(t, text, "Контекст: задача про миграции")
	require.Contains(t, text, "Проект omni · ран run-1")
	require.Contains(t, text, "Ответьте здесь:")

	request.Question = "Which database should we use, postgres or sqlite?"
	text = notificationText(request, "http://ui")
	require.Contains(t, text, "an agent is waiting for your answer")
	require.Contains(t, text, "Context: задача про миграции")
	require.Contains(t, text, "Project omni · run run-1")
	require.Contains(t, text, "Answer here:")
}

// TestElapsedLabel: the run-progress timer speaks the dialog's language.
func TestElapsedLabel(t *testing.T) {
	cases := []struct {
		d    time.Duration
		lang string
		want string
	}{
		{35 * time.Second, "en", "35s"},
		{35 * time.Second, "ru", "35 с"},
		{65 * time.Second, "en", "1m 05s"},
		{65 * time.Second, "ru", "1 мин 05 с"},
		{3700 * time.Second, "en", "1h 01m"},
		{3700 * time.Second, "ru", "1 ч 01 мин"},
		{0, "ru", "0 с"},
		{-3 * time.Second, "en", "0s"},
	}
	for _, tc := range cases {
		if got := ElapsedLabel(tc.d, tc.lang); got != tc.want {
			t.Errorf("ElapsedLabel(%v, %q) = %q, want %q", tc.d, tc.lang, got, tc.want)
		}
	}
}
