package main

// The learning catalog (docs/ai-guided-onboarding-and-learning.md §10.1):
// the lesson definitions and the capability registry ship WITH the feature —
// a developer adds a capability + lesson here when shipping functionality
// (§10.2), and the supervisor picks them up for the right users (P4).
// Definitions are declarative (§11.2): what to teach and how — the
// execution state lives in workspace.learning_* tables, never here.

import "strings"

// LearningStepType — §4.3.
const (
	StepExplain       = "explain"
	StepNavigate      = "navigate"
	StepHighlight     = "highlight"
	StepExample       = "example"
	StepUserAction    = "user_action"
	StepConfiguration = "configuration"
	StepVerification  = "verification"
	StepSummary       = "summary"
)

// LearningStep is one declarative lesson step (§4.3): the frontend executes
// it (navigate via the router, highlight via the spotlight overlay,
// user_action waits for the user's done click), the supervisor may adapt.
type LearningStep struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Text     string `json:"text,omitempty"`
	TargetID string `json:"target_id,omitempty"`
	Route    string `json:"route,omitempty"`
	Example  string `json:"example,omitempty"`
	// user_action steps: what the user is expected to do before «Готово».
	ActionHint string `json:"action_hint,omitempty"`
}

// LearningLesson is one teachable topic (§4.2): a self-contained unit
// startable from the program, an indicator, a notification or directly.
type LearningLesson struct {
	ID          string         `json:"id"`
	Version     int            `json:"version"`
	Title       string         `json:"title"`
	Goal        string         `json:"goal,omitempty"`
	Description string         `json:"description,omitempty"`
	Audience    []string       `json:"audience,omitempty"` // roles that benefit: admin, operator, …
	Page        string         `json:"page"`               // the route the lesson lives on
	FeatureID   string         `json:"feature_id"`
	Steps       []LearningStep `json:"steps"`
	// Verification criteria (§7.7): checked against the actual configuration,
	// not against the user's word (§4.5 mastery).
	VerifyFeature string `json:"verify_feature,omitempty"`
	Priority      int    `json:"priority"` // lower = earlier in the program
	DurationMin   int    `json:"duration_min,omitempty"`
}

// LearningCapability is the feature registry entry (§10.1): what the feature
// is, whether it APPLIES to this organization, and how mastery is checked.
type LearningCapability struct {
	ID           string                                              `json:"id"`
	Title        string                                              `json:"title"`
	About        string                                              `json:"about,omitempty"`
	IntroducedIn string                                              `json:"introduced_in,omitempty"` // new-features indicator (P4)
	AppliesTo    func(ctx appContext) bool                           `json:"-"`
	Mastered     func(ctx appContext) (status string, detail string) `json:"-"`
}

// appContext is the factual configuration snapshot the planner and the
// verification checks read (§5.2 — only what the org actually has).
type appContext struct {
	Agents        int
	Projects      int
	Tasks         int
	Artifacts     int
	Reviews       int
	HasReviewer   bool
	HasDelegation bool
}

// learningCapabilities is the capability registry. Applicability and mastery
// checks read the live configuration — a human verdict (accepted/reviewed)
// may also land in learning_progress via the board verdicts.
var learningCapabilities = []LearningCapability{
	{
		ID: "delegation", Title: "Делегирование между агентами",
		About:     "Большая задача разбивается на подзадачи для других агентов: широта, глубина и пул агентов настраиваются на агенте.",
		AppliesTo: func(c appContext) bool { return c.Agents >= 2 },
		Mastered: func(c appContext) (string, string) {
			if c.HasDelegation {
				return "practiced", "делегирование настроено на агенте"
			}
			return "unknown", "делегирование не настроено"
		},
	},
	{
		ID: "quality-control", Title: "Контроль качества и критерии приёмки",
		About:     "Reviewer-агент проверяет результаты по критериям приёмки до передачи человеку.",
		AppliesTo: func(c appContext) bool { return c.Agents >= 2 },
		Mastered: func(c appContext) (string, string) {
			if c.HasReviewer {
				return "practiced", "reviewer-агент настроен"
			}
			return "unknown", "reviewer-агент не найден"
		},
	},
	{
		ID: "artifact-review", Title: "Согласование артефактов",
		About:     "Документы проходят круги согласования: агент готовит черновик, человек утверждает или возвращает на доработку.",
		AppliesTo: func(c appContext) bool { return c.Artifacts > 0 || c.Projects > 0 },
		Mastered: func(c appContext) (string, string) {
			if c.Reviews > 0 {
				return "verified", "проведён круг согласования"
			}
			if c.Artifacts > 0 {
				return "introduced", "артефакты созданы, согласование ещё не проводилось"
			}
			return "unknown", "артефактов пока нет"
		},
	},
	{
		ID: "work-board", Title: "Работа: доска руководителя",
		About:     "Единая доска работы организации: задачи по стадиям, проблемы, разбор и принятие результатов.",
		AppliesTo: func(c appContext) bool { return c.Tasks > 0 },
		Mastered: func(c appContext) (string, string) {
			if c.Tasks > 0 {
				return "practiced", "задачи на доске"
			}
			return "unknown", "задач пока нет"
		},
	},
}

// learningLessons ships the initial lessons (§9.1 gives the day-by-day
// program shape; the planner orders by priority and applicability).
var learningLessons = []LearningLesson{
	{
		ID: "org-structure-basics", Version: 1, Priority: 10, DurationMin: 10,
		Title:       "Организация: структура и роли",
		Goal:        "Понимать дерево организации: подразделения, роли, участники.",
		Description: "Как устроена организация в Temporality и где она редактируется.",
		Audience:    []string{"admin", "operator"},
		Page:        "/org",
		FeatureID:   "org-structure",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Зачем структура", Text: "Структура определяет видимость и ответственность: кто какие раны видит, кто согласовывает, кто управляет подразделением."},
			{Type: StepNavigate, Title: "Открываем организацию", Route: "/org", Text: "Дерево подразделений с ролями на узлах."},
			{Type: StepHighlight, Title: "Подразделение", TargetID: "org.tree", Text: "Каждый узел — подразделение: к нему привязаны ресурсы и участники."},
			{Type: StepSummary, Title: "Итог", Text: "Структура задаёт контур ответственности — дальше роли и политики."},
		},
		VerifyFeature: "org-structure",
	},
	{
		ID: "quality-control", Version: 1, Priority: 20, DurationMin: 12,
		Title:       "Контроль качества: reviewer и критерии",
		Goal:        "Настроить reviewer-агента, который проверяет работу по критериям приёмки.",
		Description: "Качество как процесс: агент-исполнитель отдает результат на проверку reviewer-агенту.",
		Audience:    []string{"admin", "operator"},
		Page:        "/agent-config",
		FeatureID:   "quality-control",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Зачем reviewer", Text: "Reviewer — отдельный агент, который проверяет результат по критериям приёмки. Исполнитель и проверяющий — разные агенты."},
			{Type: StepNavigate, Title: "Открываем агентов", Route: "/agent-config", Text: "Страница агентов."},
			{Type: StepHighlight, Title: "Reviewer", TargetID: "agent.roles.reviewer", Text: "Вот где задаётся reviewer: цели и критерии проверки."},
			{Type: StepExample, Title: "Пример", TargetID: "agent.roles.reviewer", Text: "Проверь: (1) критерии приёмки выполнены; (2) тесты прошли; (3) ничего вне задачи не изменено.", Example: "Роль reviewer создаётся как отдельный агент с ограничением modify_files=false."},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим фактическую конфигурацию: reviewer-агент существует."},
			{Type: StepSummary, Title: "Итог", Text: "Reviewer настроен — исполнение и проверка разделены."},
		},
		VerifyFeature: "quality-control",
	},
	{
		ID: "delegation-basics", Version: 1, Priority: 30, DurationMin: 10,
		Title:       "Делегирование: большие задачи по частям",
		Goal:        "Настроить агенту делегирование: подзадачи, широта и глубина.",
		Description: "Делегирование — основной способ выполнять крупные задачи: агент ставит подзадачи другим агентам по плану.",
		Audience:    []string{"admin", "operator"},
		Page:        "/agent-config",
		FeatureID:   "delegation",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Как это работает", Text: "Агент с делегированием создаёт подзадачи другим агентам и собирает результаты. Ширина — сколько подзадач параллельно, глубина — сколько уровней вложенности."},
			{Type: StepNavigate, Title: "Открываем агентов", Route: "/agent-config", Text: "Делегирование настраивается на карточке агента."},
			{Type: StepHighlight, Title: "Delegation", TargetID: "agent.delegation.configuration", Text: "Пул агентов и лимиты делегирования."},
			{Type: StepUserAction, Title: "Попробуйте", TargetID: "agent.delegation.configuration", Text: "Включите делегирование и задайте ширину. Нажмите «Готово», когда сохраните.", ActionHint: "включить делегирование и сохранить"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим, что делегирование включено в конфигурации агента."},
			{Type: StepSummary, Title: "Итог", Text: "Теперь крупные задачи агент разбивает сам."},
		},
		VerifyFeature: "delegation",
	},
	{
		ID: "artifact-approval", Version: 1, Priority: 40, DurationMin: 10,
		Title:       "Артефакты и согласование",
		Goal:        "Провести документ через круг согласования.",
		Description: "Артефакты — документы с версиями и согласованием: агент готовит, человек утверждает.",
		Audience:    []string{"admin", "operator", "writer"},
		Page:        "/artifacts",
		FeatureID:   "artifact-review",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Черновик → согласование → утверждён", Text: "Агент готовит документ, но утверждает его человек: круг согласования с ревьюерами и режимом all/any."},
			{Type: StepNavigate, Title: "Открываем артефакты", Route: "/artifacts", Text: "Реестр артефактов."},
			{Type: StepHighlight, Title: "Отправить на согласование", TargetID: "artifact.approval.rules", Text: "Здесь задаются ревьюеры и режим: все должны одобрить или достаточно одного."},
			{Type: StepUserAction, Title: "Попробуйте", TargetID: "artifact.approval.rules", Text: "Создайте черновик и отправьте на согласование. Нажмите «Готово» после отправки.", ActionHint: "отправить артефакт на согласование"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим, что круг согласования открыт."},
			{Type: StepSummary, Title: "Итог", Text: "Результат утверждает человек — история согласования сохраняется."},
		},
		VerifyFeature: "artifact-review",
	},
	{
		ID: "work-board", Version: 1, Priority: 50, DurationMin: 8,
		Title:       "Доска работы: управление организацией",
		Goal:        "Видеть работу организации на одной доске и управлять ею.",
		Description: "Доска работы показывает задачи по стадиям, проблемы и позволяет принимать результаты.",
		Audience:    []string{"admin", "operator"},
		Page:        "/org/work",
		FeatureID:   "work-board",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Что на доске", Text: "Колонки — стадии: запланировано, в работе, ожидание, ревью, завершено. Упадок в «Ревью» требует вашего решения."},
			{Type: StepNavigate, Title: "Открываем доску", Route: "/org/work", Text: "Контроль-тауэр работы организации."},
			{Type: StepHighlight, Title: "Ревью", TargetID: "orgwork.column.review", Text: "Здесь собираются задачи, требующие вашего взгляда: упавшие или ожидающие проверки."},
			{Type: StepUserAction, Title: "Попробуйте", TargetID: "orgwork.column.review", Text: "Откройте карточку в «Ревью» и примите работу кнопкой «Принять: в завершённые» — тикет не теряется, а переезжает. Нажмите «Готово» после.", ActionHint: "принять работу из ревью"},
			{Type: StepSummary, Title: "Итог", Text: "Доска — рабочее место руководителя: от картины к трассе конкретного рана."},
		},
		VerifyFeature: "work-board",
	},
}

// findLearningLesson by id.
func findLearningLesson(id string) *LearningLesson {
	for i := range learningLessons {
		if learningLessons[i].ID == id {
			return &learningLessons[i]
		}
	}
	return nil
}

// findLearningCapability by feature id (lesson.VerifyFeature).
func findLearningCapability(id string) *LearningCapability {
	for i := range learningCapabilities {
		if learningCapabilities[i].ID == id {
			return &learningCapabilities[i]
		}
	}
	return nil
}

// audienceMatches: the user's role gates the lesson (§13 — roles differ).
func audienceMatches(audience []string, role string) bool {
	if len(audience) == 0 {
		return true
	}
	for _, r := range audience {
		if strings.EqualFold(r, role) {
			return true
		}
	}
	return false
}
