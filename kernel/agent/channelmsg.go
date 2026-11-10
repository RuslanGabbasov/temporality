package agent

import (
	"fmt"
	"strings"
	"time"
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
	"notice_encrypted": {"Temporality: end-to-end encryption is enabled in this room and the bot cannot read encrypted messages — your message was NOT processed. Send the task in an unencrypted room or through the web app.",
		"Temporality: в комнате включено сквозное шифрование, бот не может читать зашифрованные сообщения — ваше сообщение НЕ было обработано. Отправьте задачу в комнату без шифрования или через веб-приложение."},
	"notice_ask_dead": {"Temporality: the question you replied to is no longer active — its run has already ended. Your message was passed on as a new one.",
		"Temporality: вопрос, на который вы ответили, уже неактуален — его ран завершился. Сообщение передано как новое."},
	"ask_header":      {"Temporality: an agent is waiting for your answer", "Temporality: агент ждёт вашего ответа"},
	"ask_context":     {"Context:", "Контекст:"},
	"ask_options":     {"Options:", "Варианты:"},
	"ask_form":        {"Form fields (answer in the web UI): %s", "Поля формы (заполняются в веб-интерфейсе): %s"},
	"ask_form_url":    {"Fill the form here:", "Заполнить форму здесь:"},
	"ask_form_button": {"Open form", "Заполнить форму"},
	"ask_project_run": {"Project %s · run %s", "Проект %s · ран %s"},
	"ask_waiting":     {"waiting %ds", "ждёт %dс"},
	"ask_answer_here": {"Answer here:", "Ответьте здесь:"},
	"ask_cancel":      {"Cancel", "Отмена"},
	"ask_resolved":    {"✅ Answered — the run continues.", "✅ Ответ принят — ран продолжается."},
	"ask_cancelled":   {"✖ Question cancelled.", "✖ Вопрос отменён."},

	// Interactive surfaces (docs/interactive-HITL.md Этап D + widgets): the
	// tap receipts and the widget session lifecycle.
	"callback_ok":     {"Answer accepted — the agent is continuing.", "Ответ принят — агент продолжает работу."},
	"callback_cancel": {"Question cancelled — the run continues without it.", "Вопрос отменён — ран продолжается без него."},
	"callback_closed": {"This question is already closed — answer the active one or send a new message.", "Этот вопрос уже закрыт — ответьте на активный или пришлите новое сообщение."},
	"callback_bad":    {"Could not route this button press — reply with text instead.", "Не удалось разобрать нажатие — ответьте текстом."},
	"widget_expired":  {"The form session has expired — the question was answered or closed.", "Сессия формы истекла — вопрос уже закрыт или отвечен."},
	"widget_invalid":  {"Check the highlighted fields and resubmit.", "Исправьте подсвеченные поля и отправьте снова."},
	"widget_accepted": {"Form accepted — the agent is continuing.", "Форма принята — агент продолжает работу."},
	"widget_rate":     {"Too many updates — wait a couple of seconds.", "Слишком часто — подождите пару секунд."},
	"widget_bad_op":   {"Could not apply this action — reopen the form.", "Не удалось применить действие — переоткройте форму."},

	// Artifact review decisions from the channel (docs/artifact-lifecycle.md
	// §6 Phase 2): the reply router confirms or refuses in the dialog language.
	"review_decided":    {"✅ %q (v%d) — approved%s", "✅ «%s» (v%d) — утверждено%s"},
	"review_decided_re": {"❌ %q (v%d) — rejected%s", "❌ «%s» (v%d) — отклонено%s"},
	"review_by":         {" by %s", " — %s"},
	"review_already":    {"This review round is already resolved — see the Temporality artifacts page.", "Этот круг согласования уже закрыт — смотрите страницу артефактов Temporality."},
	"review_not_yours":  {"You are not a reviewer of this review round.", "Вы не согласующий этого круга ревью."},
	"review_how_to":     {"Reply “approve [comment]” or “reject [comment]” to decide, or use the Temporality artifacts page.", "Ответьте «одобрить [комментарий]» или «отклонить [комментарий]», либо решите на странице артефактов Temporality."},
	"review_left_open":  {"Noted — the review round stays open; decide in the Temporality artifacts page.", "Принято — круг согласования остаётся открытым; решение — на странице артефактов Temporality."},
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

// ElapsedLabel renders a run's elapsed time in the dialog language: units are
// localized («1 мин 05 с» / «1m 05s»), the zero case reads as «0 с» / «0s».
// Hours are shown only once they matter (>= 1h).
func ElapsedLabel(d time.Duration, lang string) string {
	total := int64(d.Seconds())
	if total < 0 {
		total = 0
	}
	hours, rest := total/3600, total%3600
	minutes, seconds := rest/60, rest%60
	if lang == LangRU {
		switch {
		case hours > 0:
			return fmt.Sprintf("%d ч %02d мин", hours, minutes)
		case minutes > 0:
			return fmt.Sprintf("%d мин %02d с", minutes, seconds)
		default:
			return fmt.Sprintf("%d с", seconds)
		}
	}
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}
