package agent

import "testing"

// The priming hint query used to be the user's phrase verbatim — on old
// conversations that is «Продолжай» / «Исправил», and matching knowledge on
// glue words returned unrelated hints (the «бесполезные подсказки» noise).
// The query now falls back to the TASK TITLE when the phrase carries no
// retrieval signal, and skips entirely when nothing carries signal.
func TestBuildHintQuery(t *testing.T) {
	cases := []struct {
		name  string
		input RunInput
		want  string
	}{
		{
			name:  "substantive phrase wins",
			input: RunInput{Prompt: "Рефакторинг selectVisibleThreadUsers: приём threadEvent вместо lastReplySender, обнови тесты"},
			want:  "Рефакторинг selectVisibleThreadUsers: приём threadEvent вместо lastReplySender, обнови тесты",
		},
		{
			name:  "glue phrase falls back to the task title",
			input: RunInput{Prompt: "Продолжай", Title: "RD-1898: рефакторинг selectVisibleThreadUsers на приём threadEvent"},
			want:  "RD-1898: рефакторинг selectVisibleThreadUsers на приём threadEvent. Продолжай",
		},
		{
			name:  "hint query override is primary",
			input: RunInput{Prompt: "Продолжай", HintQuery: "ошибка компиляции undefined variable threadEvent", Title: "Старая тема"},
			want:  "ошибка компиляции undefined variable threadEvent",
		},
		{
			name:  "glue everywhere: empty query (skip retrieval)",
			input: RunInput{Prompt: "Продолжай", Title: "Продолжай"},
			want:  "",
		},
		{
			name:  "empty prompt with substantive title",
			input: RunInput{Prompt: "", Title: "Настройка MCP сервера для документации"},
			want:  "Настройка MCP сервера для документации",
		},
	}
	for _, tc := range cases {
		got := buildHintQuery(tc.input)
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHintQuerySignal(t *testing.T) {
	if n := hintQuerySignal("Продолжай"); n != 0 {
		t.Fatalf("«Продолжай» must carry zero signal, got %d", n)
	}
	if n := hintQuerySignal("не работает нифига исправляй"); n != 0 {
		t.Fatalf("pure glue must carry zero signal, got %d", n)
	}
	if n := hintQuerySignal("Исправил ошибки сборки тесты"); n < 1 {
		t.Fatalf("a substantive phrase must carry signal, got %d", n)
	}
}
