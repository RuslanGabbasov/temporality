package agent

import (
	"fmt"
	"strings"
)

// Channel-facing strings are bilingual (ru/en): the dialog follows the
// language the user writes in — detected from their message text — and HITL
// envelopes follow the question's language (the model answers in the
// conversation's language). No user-setting schema involved; a stored
// per-user language can override detection later.

const (
	LangEN = "en"
	LangRU = "ru"
)

// DetectLang picks the dialog language from free text: any meaningful share
// of Cyrillic letters makes it Russian, everything else is English. Emoji,
// code and punctuation never flip the decision.
func DetectLang(text string) string {
	cyrillic, latin := 0, 0
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'а' && r <= 'я' || r == 'ё':
			cyrillic++
		case r >= 'a' && r <= 'z':
			latin++
		}
	}
	if cyrillic > 0 && cyrillic*3 >= latin {
		return LangRU
	}
	return LangEN
}

// channelMessages holds every canned channel-facing line per language. Keys
// are stable identifiers; a missing translation falls back to English.
var channelMessages = map[string][2]string{
	// [en, ru]
	"reply_hint":             {"Reply in this room to answer.", "Ответьте в этом чате, чтобы продолжить."},
	"on_it":                  {"On it — working on your message…", "Принято — работаю над вашим сообщением…"},
	"phase_explore":          {"🔍 Searching the project knowledge base…", "🔍 Ищу в базе знаний проекта…"},
	"phase_start_task":       {"🚀 Starting the task in project «%s»…", "🚀 Завожу задачу в проекте «%s»…"},
	"phase_start_task_plain": {"🚀 Starting the task…", "🚀 Завожу задачу…"},
	"phase_status":           {"📊 Checking the running tasks…", "📊 Смотрю статус запущенных задач…"},
	"phase_remember":         {"🧠 Saving to the knowledge base…", "🧠 Сохраняю в базу знаний…"},
	"phase_tool":             {"⚙️ Running %s…", "⚙️ Выполняю %s…"},
	"note_added":             {"Added to the running task — the agent takes it into account at the next step.", "Добавил к текущей задаче — агент учтёт это на следующем шаге."},
	"notice_room_owner": {"Temporality: this room accepts answers to agent questions, but new tasks are only accepted from the registered account of the room owner. Open the app to create a task, or register this channel identity in your profile.",
		"Temporality: эта комната принимает ответы на вопросы агентов, но новые задачи — только от зарегистрированного аккаунта владельца комнаты. Откройте приложение, чтобы создать задачу, или привяжите эту учётную запись канала в профиле."},
	"notice_rate_limit": {"Temporality: rate limit reached (%d messages per hour). Please retry later; urgent tasks can be created in the app.",
		"Temporality: достигнут лимит сообщений (%d в час). Попробуйте позже; срочные задачи можно создать в приложении."},
	"ask_header":      {"Temporality: an agent is waiting for your answer", "Temporality: агент ждёт вашего ответа"},
	"ask_context":     {"Context:", "Контекст:"},
	"ask_options":     {"Options:", "Варианты:"},
	"ask_project_run": {"Project %s · run %s", "Проект %s · ран %s"},
	"ask_waiting":     {"waiting %ds", "ждёт %dс"},
	"ask_answer_here": {"Answer here:", "Ответьте здесь:"},
}

// ChannelText renders one canned line in the dialog language, English as the
// fallback for unknown languages and missing keys.
func ChannelText(lang, key string, args ...any) string {
	entry, ok := channelMessages[key]
	if !ok {
		return ""
	}
	pattern := entry[0]
	if lang == LangRU && entry[1] != "" {
		pattern = entry[1]
	}
	return fmt.Sprintf(pattern, args...)
}
