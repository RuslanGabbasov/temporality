package main

// The learning catalog (docs/ai-guided-onboarding-and-learning.md §10.1):
// the lesson definitions and the capability registry ship WITH the feature —
// a developer adds a capability + lesson here when shipping functionality
// (§10.2), and the supervisor picks them up for the right users (P4).
// Definitions are declarative (§11.2): what to teach and how — the
// execution state lives in workspace.learning_* tables, never here.

import (
	"context"
	"strings"

	"github.com/temporality-project/temporality/workspace"
)

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
	ID           string `json:"id"`
	Title        string `json:"title"`
	About        string `json:"about,omitempty"`
	IntroducedIn string `json:"introduced_in,omitempty"` // new-features indicator (P4)
	// Observational marks capabilities whose mastery is a suggestion signal,
	// not a configuration fact: their Mastered check never flips to
	// practiced, so a verification step gated on them would block forever
	// (the learner could only skip). Generated lessons drop verification for
	// these; the static catalog simply ships no verification step for them.
	Observational bool                                                `json:"-"`
	AppliesTo     func(ctx appContext) bool                           `json:"-"`
	Mastered      func(ctx appContext) (status string, detail string) `json:"-"`
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

	// Setup facts (the onboarding-ladder verification gates read these).
	Providers        int  // model providers configured
	Secrets          int  // secrets stored
	MCPServers       int  // MCP servers registered
	Skills           int  // skills in the catalog
	Triggers         int  // enabled triggers
	Teams            int  // teams configured
	Users            int  // users in the workspace
	OrgUnits         int  // org tree nodes
	Policies         int  // org policies
	SandboxBuilt     int  // sandbox profiles with a built image
	HasJudge         bool // a supervisor judge model is configured
	ChannelsReady    bool // a delivery channel is configured
	AgentsConfigured int  // agents with a model assigned
}

// learningAppContext gathers the factual configuration snapshot (§5.2) the
// planner and the mastery checks read.
func learningAppContext(ctx context.Context, ws *workspace.Store, project string, units []string) appContext {
	c := appContext{}
	if agents, err := ws.ListAgentsVisible(ctx, units); err == nil {
		c.Agents = len(agents)
		for _, a := range agents {
			id := strings.ToLower(a.ID + " " + a.Name)
			if strings.Contains(id, "review") {
				c.HasReviewer = true
			}
			if strings.TrimSpace(a.Model) != "" {
				c.AgentsConfigured++
			}
			if a.Definition != nil && a.Definition.Capabilities.Delegation != nil && *a.Definition.Capabilities.Delegation {
				c.HasDelegation = true
			}
		}
	}
	if projects, err := ws.ListProjects(ctx); err == nil {
		c.Projects = len(projects)
	}
	if project != "" {
		if tasks, err := ws.ListTasks(ctx, project); err == nil {
			c.Tasks = len(tasks)
		}
	} else if projects, err := ws.ListProjects(ctx); err == nil {
		// No project context (verification gate, state endpoint, needs pass):
		// count across projects so a Tasks-based mastery check is a fact, not
		// a permanent zero. Brief rows are light; the project list is small.
		for i, p := range projects {
			if i >= 50 {
				break
			}
			if tasks, err := ws.ListTasksBrief(ctx, p.ID); err == nil {
				c.Tasks += len(tasks)
			}
		}
	}
	if artifacts, err := ws.ListArtifactsFiltered(ctx, units, workspace.ArtifactFilter{Status: "in_review"}); err == nil {
		c.Reviews = len(artifacts)
	}
	if artifacts, err := ws.ListArtifactsVisible(ctx, units); err == nil {
		c.Artifacts = len(artifacts)
	}

	// Setup facts for the verification gates. Best-effort: a store hiccup
	// must not fail the learning endpoints — the gate just reports the
	// feature as unverified and the learner can retry later.
	if providers, err := ws.ListProvidersVisible(ctx, units); err == nil {
		for _, p := range providers {
			if p.Enabled {
				c.Providers++
			}
		}
	}
	if secrets, err := ws.ListSecretsVisible(ctx, units); err == nil {
		for _, s := range secrets {
			if s.Enabled {
				c.Secrets++
			}
		}
	}
	if servers, err := ws.ListMCPServersVisible(ctx, units); err == nil {
		for _, m := range servers {
			if m.Enabled {
				c.MCPServers++
			}
		}
	}
	if skills, err := ws.ListSkillsVisible(ctx, units); err == nil {
		for _, s := range skills {
			if s.Enabled {
				c.Skills++
			}
		}
	}
	if triggers, err := ws.ListAllTriggers(ctx); err == nil {
		c.Triggers = len(triggers) // the query already returns enabled rows
	}
	if teams, err := ws.ListTeamsVisible(ctx, units); err == nil {
		for _, t := range teams {
			if t.Enabled {
				c.Teams++
			}
		}
	}
	if users, err := ws.ListUsers(ctx); err == nil {
		c.Users = len(users)
	}
	if orgUnits, err := ws.ListOrgUnits(ctx); err == nil {
		c.OrgUnits = len(orgUnits)
	}
	if policies, err := ws.ListPolicies(ctx); err == nil {
		c.Policies = len(policies)
	}
	if profiles, err := ws.ListSandboxProfiles(ctx); err == nil {
		for _, p := range profiles {
			if p.Image != "" {
				c.SandboxBuilt++
			}
		}
	}
	if settings, err := ws.SupervisorSettings(ctx); err == nil {
		c.HasJudge = strings.TrimSpace(settings.JudgeModel) != ""
	}
	if transport, err := ws.ChannelTransport(ctx); err == nil && len(transport.Payload) > 0 {
		c.ChannelsReady = true
	}
	if !c.ChannelsReady {
		if users, err := ws.ListUsers(ctx); err == nil {
			for _, u := range users {
				for _, ch := range u.Channels {
					if ch.Enabled {
						c.ChannelsReady = true
						break
					}
				}
				if c.ChannelsReady {
					break
				}
			}
		}
	}
	return c
}

// learningCapabilities is the capability registry. Applicability and mastery
// checks read the live configuration — a human verdict (accepted/reviewed)
// may also land in learning_progress via the board verdicts.
//
// Two kinds of checks live here:
//   - setup facts (providers, secrets, triggers, …): «practiced» means the
//     organization actually configured the thing — the lesson's verification
//     step gates on it;
//   - observational facts (knowledge, experience, …): the check stays
//     «unknown» until the learner touches the surface, so the needs pass
//     suggests the lesson once and never repeats a dismissed slot.
var learningCapabilities = []LearningCapability{
	{
		ID: "orientation", Title: "Ориентация в платформе",
		About:         "Где что лежит: дашборд, рабочий стол, запуски, организация, знания и настройки.",
		Observational: true,
		AppliesTo:     func(c appContext) bool { return true },
		Mastered: func(c appContext) (string, string) {
			return "unknown", "обзор интерфейса ещё не пройден"
		},
	},
	{
		ID: "org-structure", Title: "Организация: структура и роли",
		About:     "Дерево подразделений задаёт видимость, ответственность и контур согласований.",
		AppliesTo: func(c appContext) bool { return true },
		Mastered: func(c appContext) (string, string) {
			if c.OrgUnits > 0 {
				return "practiced", "структура заведена: узлов — " + itoa(c.OrgUnits)
			}
			return "unknown", "структура организации пуста"
		},
	},
	{
		ID: "execution", Title: "Первый запуск агента",
		About:     "Задача из чата уходит в ран, а результат возвращается в переписку и на доску.",
		AppliesTo: func(c appContext) bool { return c.Projects > 0 },
		Mastered: func(c appContext) (string, string) {
			if c.Tasks > 0 {
				return "practiced", "задачи ставятся: " + itoa(c.Tasks)
			}
			return "unknown", "задач агентам ещё не было"
		},
	},
	{
		ID: "run-observability", Title: "Запуски и трасса",
		About:     "Трасса рана: ходы, вызовы инструментов, токены и ошибки — точка диагностики.",
		AppliesTo: func(c appContext) bool { return true },
		Mastered: func(c appContext) (string, string) {
			if c.Tasks > 0 {
				return "practiced", "по запускам есть что смотреть"
			}
			return "unknown", "запусков пока нет"
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
	{
		ID: "providers", Title: "Провайдеры моделей",
		About:     "Подключение моделей: base URL, ключ, список моделей. Без провайдера раны не стартуют.",
		AppliesTo: func(c appContext) bool { return true },
		Mastered: func(c appContext) (string, string) {
			if c.Providers > 0 {
				return "practiced", "провайдеры подключены: " + itoa(c.Providers)
			}
			return "unknown", "провайдеры не настроены"
		},
	},
	{
		ID: "secrets", Title: "Секреты",
		About:     "API-ключи и токены хранятся в workspace и попадают агентам только по ссылке secret://.",
		AppliesTo: func(c appContext) bool { return true },
		Mastered: func(c appContext) (string, string) {
			if c.Secrets > 0 {
				return "practiced", "секреты хранятся: " + itoa(c.Secrets)
			}
			return "unknown", "секретов пока нет"
		},
	},
	{
		ID: "mcp", Title: "MCP-серверы",
		About:     "Внешние инструменты агента: stdio/sse/http, белые списки инструментов и таймауты.",
		AppliesTo: func(c appContext) bool { return c.Agents >= 1 },
		Mastered: func(c appContext) (string, string) {
			if c.MCPServers > 0 {
				return "practiced", "MCP-серверы подключены: " + itoa(c.MCPServers)
			}
			return "unknown", "MCP-серверы не подключены"
		},
	},
	{
		ID: "agent-authoring", Title: "Карточка агента",
		About:     "Роль, модель, права и инструменты агента — место, где агент становится исполнителем.",
		AppliesTo: func(c appContext) bool { return c.Agents >= 1 },
		Mastered: func(c appContext) (string, string) {
			if c.AgentsConfigured > 0 {
				return "practiced", "агентов с моделью: " + itoa(c.AgentsConfigured)
			}
			return "unknown", "ни один агент ещё не получил модель"
		},
	},
	{
		ID: "skills", Title: "Навыки агентов",
		About:     "SKILL-файлы с инструкциями и манифестами: версии, черновики и эволюция предлагаются агентами.",
		AppliesTo: func(c appContext) bool { return c.Agents >= 1 },
		Mastered: func(c appContext) (string, string) {
			if c.Skills > 0 {
				return "practiced", "навыки в каталоге: " + itoa(c.Skills)
			}
			return "unknown", "каталог навыков пуст"
		},
	},
	{
		ID: "sandbox", Title: "Песочницы",
		About:     "Изолированные контейнеры для выполнения кода: профили, сборка образов, ресурсные лимиты.",
		AppliesTo: func(c appContext) bool { return c.Agents >= 1 },
		Mastered: func(c appContext) (string, string) {
			if c.SandboxBuilt > 0 {
				return "practiced", "профили собраны: " + itoa(c.SandboxBuilt)
			}
			return "unknown", "образы песочниц ещё не собраны"
		},
	},
	{
		ID: "storage", Title: "Файловое хранилище",
		About:         "Файлы проекта в workspace: директории, размеры, поиск и выдача агентам.",
		Observational: true,
		AppliesTo:     func(c appContext) bool { return c.Projects > 0 },
		Mastered: func(c appContext) (string, string) {
			return "unknown", "хранилище не осмотрено"
		},
	},
	{
		ID: "triggers", Title: "Триггеры",
		About:     "Расписание, вебхуки и события: организация запускает агентов сама, без человека.",
		AppliesTo: func(c appContext) bool { return c.Projects > 0 },
		Mastered: func(c appContext) (string, string) {
			if c.Triggers > 0 {
				return "practiced", "триггеры настроены: " + itoa(c.Triggers)
			}
			return "unknown", "автозапусков пока нет"
		},
	},
	{
		ID: "users-access", Title: "Пользователи и доступы",
		About:     "Роли reader/writer/operator/admin, членство в подразделениях и выдача токенов.",
		AppliesTo: func(c appContext) bool { return true },
		Mastered: func(c appContext) (string, string) {
			if c.Users > 1 {
				return "practiced", "участников: " + itoa(c.Users)
			}
			return "unknown", "в workspace только один пользователь"
		},
	},
	{
		ID: "channels", Title: "Каналы доставки",
		About:     "Вопросы агентов и статусы уходят людям в telegram/matrix/slack — настройка транспорта.",
		AppliesTo: func(c appContext) bool { return true },
		Mastered: func(c appContext) (string, string) {
			if c.ChannelsReady {
				return "practiced", "канал доставки настроен"
			}
			return "unknown", "каналы доставки не настроены"
		},
	},
	{
		ID: "teams", Title: "Команды агентов",
		About:     "Манифест роли: набор агентов, который запускается одной командой и собирает общий результат.",
		AppliesTo: func(c appContext) bool { return c.Agents >= 2 },
		Mastered: func(c appContext) (string, string) {
			if c.Teams > 0 {
				return "practiced", "команды собраны: " + itoa(c.Teams)
			}
			return "unknown", "команд пока нет"
		},
	},
	{
		ID: "knowledge", Title: "Хранилище знаний",
		About:         "Что агенты запомнили: поиск, карта, жизненный цикл и оспорение сомнительных фактов.",
		Observational: true,
		AppliesTo:     func(c appContext) bool { return c.Projects > 0 },
		Mastered: func(c appContext) (string, string) {
			return "unknown", "исследование знаний ещё не начиналось"
		},
	},
	{
		ID: "experience", Title: "Лента опыта",
		About:         "Хронология того, что организация узнала: цепочки знаний, активации и устаревания.",
		Observational: true,
		AppliesTo:     func(c appContext) bool { return c.Projects > 0 },
		Mastered: func(c appContext) (string, string) {
			return "unknown", "лента опыта ещё не открывалась"
		},
	},
	{
		ID: "supervision", Title: "Супервайзер",
		About:         "Очередь проблем организации: упавшие раны, аномалии, конфликты знаний и рекомендации.",
		Observational: true,
		AppliesTo:     func(c appContext) bool { return true },
		Mastered: func(c appContext) (string, string) {
			return "unknown", "очередь супервайзера не разбиралась"
		},
	},
	{
		ID: "supervisor-config", Title: "Модели супервайзера",
		About:     "Судья, vision, compaction: какие модели судят, суммируют и оценивают работу организации.",
		AppliesTo: func(c appContext) bool { return c.Agents >= 1 },
		Mastered: func(c appContext) (string, string) {
			if c.HasJudge {
				return "practiced", "модель судьи настроена"
			}
			return "unknown", "судья не настроен — вердикты супервайзера выключены"
		},
	},
	{
		ID: "org-operations", Title: "Операции организации",
		About:         "Активность подразделений и межагентные сигналы: кто о ком знает и что передаёт.",
		Observational: true,
		AppliesTo:     func(c appContext) bool { return c.OrgUnits > 0 },
		Mastered: func(c appContext) (string, string) {
			return "unknown", "операции организации не просматривались"
		},
	},
	{
		ID: "learning-meta", Title: "Центр обучения",
		About:         "Программа, освоение возможностей и подсказки: как пользоваться самим обучением.",
		Observational: true,
		AppliesTo:     func(c appContext) bool { return true },
		Mastered: func(c appContext) (string, string) {
			return "unknown", "центр обучения не освоен"
		},
	},
}

// learningLessons ships the initial lessons (§9.1 gives the day-by-day
// program shape; the planner orders by priority and applicability).
//
// The ladder runs: orientation → first run → quality loop → setup (providers
// and friends) → observation (knowledge, experience, supervision) →
// organization operations. Priority bands are 10 apart so a new lesson can
// be slotted without renumbering.
var learningLessons = []LearningLesson{
	// ── Orientation ────────────────────────────────────────────────────
	{
		ID: "dashboard-tour", Version: 1, Priority: 5, DurationMin: 6,
		Title:       "Обзор: где что лежит",
		Goal:        "Понимать карту платформы и знать, куда идти за ответом.",
		Description: "Быстрый обзор всех разделов: дашборд, рабочий стол, запуски, организация, знания и настройки.",
		Audience:    []string{"reader", "writer", "operator", "admin"},
		Page:        "/dashboard",
		FeatureID:   "orientation",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Карта платформы", Text: "Temporality устроен так: Дашборд — состояние организации, Рабочий стол — переписка с агентами, Запуски — трассы работ, Организация — структура и доска, Знания — память агентов, Настройки — провайдеры, секреты, MCP, навыки, песочницы и доступы."},
			{Type: StepNavigate, Title: "Открываем дашборд", Route: "/dashboard", Text: "Главная страница: метрики, работа, активность и очередь супервайзера."},
			{Type: StepHighlight, Title: "Панель состояния", TargetID: "page.dashboard", Text: "Здесь живут ключевые счётчики и вкладки — с них начинается любой рабочий день руководителя."},
			{Type: StepUserAction, Title: "Пройдите по разделам", TargetID: "page.dashboard", Text: "Загляните в меню слева: Рабочий стол, Запуски, Организация, Знания. Нажмите «Готово», когда вернётесь сюда.", ActionHint: "открыть 3-4 раздела из меню"},
			{Type: StepSummary, Title: "Итог", Text: "Вы знаете карту: дальше каждый урок ведёт в один конкретный раздел."},
		},
		VerifyFeature: "",
	},
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
			{Type: StepUserAction, Title: "Попробуйте", TargetID: "org.tree", Text: "Создайте подразделение или откройте существующее. Нажмите «Готово» после.", ActionHint: "создать или открыть подразделение"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим фактическую конфигурацию: структура заведена."},
			{Type: StepSummary, Title: "Итог", Text: "Структура задаёт контур ответственности — дальше роли и политики."},
		},
		VerifyFeature: "org-structure",
	},
	{
		ID: "first-run", Version: 1, Priority: 15, DurationMin: 8,
		Title:       "Первая задача агенту",
		Goal:        "Поставить агенту задачу из чата и увидеть результат.",
		Description: "Рабочий стол: переписка с агентом, запуск задачи и путь результата обратно к вам.",
		Audience:    []string{"writer", "operator", "admin"},
		Page:        "/workspace",
		FeatureID:   "execution",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Как это работает", Text: "Сообщение в чате становится задачей: ядро создаёт ран, агент работает по шагам, ответ возвращается в переписку. Позже этот же запуск видно в трассе и на доске."},
			{Type: StepNavigate, Title: "Открываем рабочий стол", Route: "/workspace", Text: "Слева — диалоги, справа — переписка с выбранным агентом."},
			{Type: StepHighlight, Title: "Переписка", TargetID: "page.workspace", Text: "Поле ввода здесь: любая задача уходит агенту и превращается в запуск."},
			{Type: StepUserAction, Title: "Поставьте задачу", TargetID: "page.workspace", Text: "Напишите агенту простую задачу (например, «сделай краткий обзор проекта») и отправьте. Нажмите «Готово», когда ответ придёт.", ActionHint: "отправить агенту сообщение в чате"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: задача агенту действительно создана."},
			{Type: StepSummary, Title: "Итог", Text: "Первый запуск состоялся — дальше смотрим, как его читать."},
		},
		VerifyFeature: "execution",
	},
	{
		ID: "runs-trace", Version: 1, Priority: 20, DurationMin: 8,
		Title:       "Запуски и трасса",
		Goal:        "Читать трассу рана: ходы, инструменты, ошибки.",
		Description: "Страница запусков — точка диагностики: что агент делал, где споткнулся, сколько стоило.",
		Audience:    []string{"reader", "writer", "operator", "admin"},
		Page:        "/agents",
		FeatureID:   "run-observability",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Что такое трасса", Text: "Каждый запуск — последовательность ходов: сообщения модели, вызовы инструментов, ответы и ошибки. Трасса отвечает на вопрос «почему так», а не «что случилось»."},
			{Type: StepNavigate, Title: "Открываем запуски", Route: "/agents", Text: "Список запусков с фильтрами по проекту, агенту и статусу."},
			{Type: StepHighlight, Title: "Список запусков", TargetID: "page.runs", Text: "Упавшие и отменённые раны видны здесь сразу — по ним и начинают разбор."},
			{Type: StepUserAction, Title: "Откройте запуск", TargetID: "page.runs", Text: "Найдите любой запуск и раскройте его трассу. Нажмите «Готово» после.", ActionHint: "открыть трассу последнего запуска"},
			{Type: StepSummary, Title: "Итог", Text: "Трасса — ваш основной инструмент диагностики; ошибки агента читаются именно здесь."},
		},
		VerifyFeature: "",
	},

	// ── The quality loop ───────────────────────────────────────────────
	{
		ID: "quality-control", Version: 1, Priority: 30, DurationMin: 12,
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
		ID: "delegation-basics", Version: 1, Priority: 40, DurationMin: 10,
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
		ID: "artifact-approval", Version: 1, Priority: 50, DurationMin: 10,
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
		ID: "work-board", Version: 1, Priority: 60, DurationMin: 8,
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

	// ── Setup: the platform legs ───────────────────────────────────────
	{
		ID: "providers-setup", Version: 1, Priority: 70, DurationMin: 8,
		Title:       "Провайдеры: подключить модель",
		Goal:        "Подключить провайдера моделей, чтобы раны вообще стартовали.",
		Description: "Провайдер — base URL, ключ и список моделей. Без него ни один агент не запустится.",
		Audience:    []string{"admin", "operator"},
		Page:        "/providers",
		FeatureID:   "providers",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Зачем провайдер", Text: "Агенты не знают про ключи: они просят модель у ядра, а ядро маршрутизирует вызов на провайдера. Провайдер выключается единым переключателем — раны падают явно, а не крутятся в бесконечных ретраях."},
			{Type: StepNavigate, Title: "Открываем провайдеров", Route: "/providers", Text: "Список провайдеров и их моделей."},
			{Type: StepHighlight, Title: "Список провайдеров", TargetID: "page.providers", Text: "Каждая карточка — подключение: base URL, ключ, обнаруженные модели."},
			{Type: StepExample, Title: "Пример", TargetID: "page.providers", Text: "OpenAI-совместимый провайдер: base_url + api_key_ref; Claude-подписка — отдельный тип аутентификации, который говорит по /v1/messages.", Example: "base_url: https://api.example.com/v1\napi_key_ref: secret://openai-key\nмодели: обновляются кнопкой «Обновить модели»"},
			{Type: StepUserAction, Title: "Подключите провайдера", TargetID: "page.providers", Text: "Нажмите «Новый провайдер», заполните base URL и ключ, сохраните. Нажмите «Готово» после.", ActionHint: "создать провайдера и сохранить"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: провайдер подключён и включён."},
			{Type: StepSummary, Title: "Итог", Text: "Модели подключены — дальше агенты могут реально работать."},
		},
		VerifyFeature: "providers",
	},
	{
		ID: "secrets-setup", Version: 1, Priority: 80, DurationMin: 8,
		Title:       "Секреты: где живут ключи",
		Goal:        "Завести секрет и понять, как агенты получают доступ без утечки.",
		Description: "Секреты хранятся в workspace и выдаются агентам только по ссылке secret:// — в чат и логи значение не попадает.",
		Audience:    []string{"admin", "operator"},
		Page:        "/secrets",
		FeatureID:   "secrets",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Как работают секреты", Text: "Значение шифруется и хранится отдельно. Агент видит только имя: в промпте и инструментах он ссылается на secret://name, а ядро подставляет значение в момент вызова. Появление значения в выводе — сигнал утечки, за этим следит canary."},
			{Type: StepNavigate, Title: "Открываем секреты", Route: "/secrets", Text: "Реестр секретов: имя, провайдер, описание."},
			{Type: StepHighlight, Title: "Реестр секретов", TargetID: "page.secrets", Text: "Кнопка «Новый секрет» здесь: имя и значение — всё, что нужно."},
			{Type: StepUserAction, Title: "Создайте секрет", TargetID: "page.secrets", Text: "Создайте секрет с осмысленным именем (например, github-token). Нажмите «Готово» после сохранения.", ActionHint: "создать секрет и сохранить"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: секрет хранится в workspace."},
			{Type: StepSummary, Title: "Итог", Text: "Ключи больше не в промптах: агенты ссылаются на имена, значения остаются в workspace."},
		},
		VerifyFeature: "secrets",
	},
	{
		ID: "mcp-setup", Version: 1, Priority: 90, DurationMin: 10,
		Title:       "MCP: внешние инструменты агента",
		Goal:        "Подключить MCP-сервер и ограничить, чем им можно пользоваться.",
		Description: "MCP-серверы дают агентам внешние инструменты: stdio/sse/http, белые списки и таймауты.",
		Audience:    []string{"admin", "operator"},
		Page:        "/mcp",
		FeatureID:   "mcp",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Что даёт MCP", Text: "Сервер MCP — это набор инструментов вне ядра: база знаний, трекер, репозиторий. Важна не только связь, но и ограничение: allowed_tools белый список, approval_tools — инструменты, требующие подтверждения человека, timeout — защита от зависших вызовов."},
			{Type: StepNavigate, Title: "Открываем MCP", Route: "/mcp", Text: "Список серверов: тип, адрес, статус."},
			{Type: StepHighlight, Title: "Список серверов", TargetID: "page.mcp", Text: "Каждый сервер включается и выключается единым переключателем."},
			{Type: StepExample, Title: "Пример", TargetID: "page.mcp", Text: "Сервер с ограниченным списком инструментов надёжнее широкого: агенту хватает двух команд, а не целого каталога.", Example: "type: stdio\ncommand: npx\nallowed_tools: [search, fetch]\ntimeout_ms: 15000"},
			{Type: StepUserAction, Title: "Подключите сервер", TargetID: "page.mcp", Text: "Добавьте MCP-сервер (или откройте существующий и проверьте allowed_tools). Нажмите «Готово» после.", ActionHint: "добавить или открыть MCP-сервер"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: MCP-сервер зарегистрирован и включён."},
			{Type: StepSummary, Title: "Итог", Text: "Инструменты подключены и ограничены — агент получает ровно столько, сколько нужно."},
		},
		VerifyFeature: "mcp",
	},
	{
		ID: "agent-card", Version: 1, Priority: 100, DurationMin: 10,
		Title:       "Карточка агента: модель и права",
		Goal:        "Назначить агенту модель и понять, где живут его права.",
		Description: "Карточка агента — роль, цель, модель, системный промпт, навыки и ограничения.",
		Audience:    []string{"admin", "operator"},
		Page:        "/agent-config",
		FeatureID:   "agent-authoring",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Из чего состоит агент", Text: "Модель — кто думает, системный промпт — как думает, навыки — что умеет, права (modify_files, сеть) — где границы. Один и тот же промпт на разной модели даёт разное качество — модель назначается здесь."},
			{Type: StepNavigate, Title: "Открываем агентов", Route: "/agent-config", Text: "Список агентов и их карточки."},
			{Type: StepHighlight, Title: "Список агентов", TargetID: "page.agent-config", Text: "Каждый агент открывается в карточку с вкладками настроек."},
			{Type: StepUserAction, Title: "Назначьте модель", TargetID: "page.agent-config", Text: "Откройте агента и выберите ему провайдер и модель. Нажмите «Готово» после сохранения.", ActionHint: "назначить агенту модель"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: у агента есть конфигурация для исполнения."},
			{Type: StepSummary, Title: "Итог", Text: "Агент получил модель — он готов к первому запуску."},
		},
		VerifyFeature: "agent-authoring",
	},
	{
		ID: "skills-basics", Version: 1, Priority: 110, DurationMin: 8,
		Title:       "Навыки: инструкции, которые агенты обновляют",
		Goal:        "Разобраться в каталоге навыках и их версиях.",
		Description: "Навыки — SKILL-файлы с манифестом: версии, черновики и эволюция от самих агентов.",
		Audience:    []string{"admin", "operator"},
		Page:        "/skills",
		FeatureID:   "skills",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Что такое навык", Text: "Навык — переиспользуемая инструкция с манифестом (какие тулы нужны, кого видит). Агенты сами предлагают улучшения: версия-черновик ждёт вашего применения — контент никогда не меняется молча."},
			{Type: StepNavigate, Title: "Открываем навыки", Route: "/skills", Text: "Каталог навыков с версиями и статусами."},
			{Type: StepHighlight, Title: "Каталог", TargetID: "page.skills", Text: "Здесь видны ожидающие применения черновики — их надо читать, а не пропускать."},
			{Type: StepUserAction, Title: "Осмотрите навык", TargetID: "page.skills", Text: "Откройте любой навык и посмотрите его версию. Нажмите «Готово» после.", ActionHint: "открыть карточку навыка"},
			{Type: StepSummary, Title: "Итог", Text: "Навыки — знание в виде инструкций: их читают, применяют и улучшают."},
		},
		VerifyFeature: "",
	},
	{
		ID: "sandbox-setup", Version: 1, Priority: 120, DurationMin: 10,
		Title:       "Песочницы: изоляция выполнения кода",
		Goal:        "Понять профили песочниц и собрать образ для выполнения команд.",
		Description: "Песочницы — контейнеры, в которых агент исполняет код: профили, сборка образов и лимиты.",
		Audience:    []string{"admin", "operator"},
		Page:        "/sandbox",
		FeatureID:   "sandbox",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Зачем песочница", Text: "Код агента исполняется не на хосте, а в контейнере с ограничениями по памяти, CPU и диску. Профиль описывает образ и лимиты; без собранного образа команда падает явно — молчаливой подмены нет."},
			{Type: StepNavigate, Title: "Открываем песочницы", Route: "/sandbox", Text: "Профили: образы, лимиты, статус сборки."},
			{Type: StepHighlight, Title: "Профили", TargetID: "page.sandbox", Text: "Профиль без образа помечен — его нужно собрать перед первым run_command."},
			{Type: StepUserAction, Title: "Соберите образ", TargetID: "page.sandbox", Text: "Запустите сборку любого профиля и дождитесь результата. Нажмите «Готово», когда сборка завершится.", ActionHint: "запустить сборку образа профиля"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: хотя бы один образ песочницы собран."},
			{Type: StepSummary, Title: "Итог", Text: "Изоляция готова — агенты могут безопасно исполнять код."},
		},
		VerifyFeature: "sandbox",
	},
	{
		ID: "storage-tour", Version: 1, Priority: 130, DurationMin: 6,
		Title:       "Хранилище: файлы проекта",
		Goal:        "Найти нужный файл в workspace и понять, как агенты им пользуются.",
		Description: "Файловое хранилище проекта: директории, размеры, поиск и выдача файлов агентам.",
		Audience:    []string{"reader", "writer", "operator", "admin"},
		Page:        "/storage",
		FeatureID:   "storage",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Где лежат файлы", Text: "Workspace — файловая система проекта, которую видят и вы, и агенты. Результаты работы агентов попадают сюда, отсюда же берутся исходники для следующих задач."},
			{Type: StepNavigate, Title: "Открываем хранилище", Route: "/storage", Text: "Дерево файлов проекта с размерами."},
			{Type: StepHighlight, Title: "Дерево файлов", TargetID: "page.storage", Text: "Поиск по именам — самый быстрый способ найти нужный документ."},
			{Type: StepUserAction, Title: "Найдите файл", TargetID: "page.storage", Text: "Откройте любую директорию и найдите файл через поиск. Нажмите «Готово» после.", ActionHint: "найти файл через поиск в хранилище"},
			{Type: StepSummary, Title: "Итог", Text: "Хранилище — общий стол для вас и агентов: что лежит здесь, то видно всем."},
		},
		VerifyFeature: "",
	},
	{
		ID: "triggers-setup", Version: 1, Priority: 140, DurationMin: 10,
		Title:       "Триггеры: организация сама запускает работу",
		Goal:        "Настроить расписание или вебхук для автозапуска агентов.",
		Description: "Триггеры: schedule, webhook, event — организация просыпается без участия человека.",
		Audience:    []string{"admin", "operator"},
		Page:        "/triggers",
		FeatureID:   "triggers",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Виды триггеров", Text: "Расписание (cron) — регулярные задачи, вебхук — внешний сигнал, событие — внутренний толчок. Триггер запускает ран под своей учётной записью, а не вашей: права автоматизации не наследуются от создателя."},
			{Type: StepNavigate, Title: "Открываем триггеры", Route: "/triggers", Text: "Список триггеров с типами и расписанием."},
			{Type: StepHighlight, Title: "Список триггеров", TargetID: "page.triggers", Text: "Каждый триггер включается и выключается без удаления — пауза это нормальный режим."},
			{Type: StepUserAction, Title: "Создайте триггер", TargetID: "page.triggers", Text: "Создайте простой schedule-триггер (например, раз в сутки). Нажмите «Готово» после сохранения.", ActionHint: "создать расписание-триггер"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: триггер настроен и включён."},
			{Type: StepSummary, Title: "Итог", Text: "Организация теперь работает и без вас: расписание запускает агентов само."},
		},
		VerifyFeature: "triggers",
	},
	{
		ID: "users-access", Version: 1, Priority: 150, DurationMin: 8,
		Title:       "Пользователи: роли и доступы",
		Goal:        "Пригласить участника и понять, что даёт каждая роль.",
		Description: "Роли reader/writer/operator/admin, членство в подразделениях и выдача токенов.",
		Audience:    []string{"admin"},
		Page:        "/users",
		FeatureID:   "users-access",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Лестница ролей", Text: "Reader — читает, Writer — ставит задачи и комментирует, Operator — настраивает и управляет запусками, Admin — всё вместе плюс пользователи и политики. Роль ограничивает действия, подразделение — видимость: вместе они дают нужную степень доступа."},
			{Type: StepNavigate, Title: "Открываем пользователей", Route: "/users", Text: "Список участников, их роли и членство в узлах."},
			{Type: StepHighlight, Title: "Список участников", TargetID: "page.users", Text: "Роль меняется прямо в строке; токен выдаётся отдельной кнопкой."},
			{Type: StepUserAction, Title: "Пригласите участника", TargetID: "page.users", Text: "Создайте второго пользователя с ролью reader. Нажмите «Готово» после создания.", ActionHint: "создать пользователя с ролью reader"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: в организации больше одного участника."},
			{Type: StepSummary, Title: "Итог", Text: "Доступы настроены — работа может делиться между людьми."},
		},
		VerifyFeature: "users-access",
	},
	{
		ID: "channels-inbound", Version: 1, Priority: 160, DurationMin: 10,
		Title:       "Каналы: вопросы агентов людям",
		Goal:        "Настроить доставку вопросов агентов в мессенджер.",
		Description: "Каналы доставки (telegram/matrix/slack) и входящие сообщения: двусторонняя связь с организацией.",
		Audience:    []string{"admin", "operator"},
		Page:        "/channels",
		FeatureID:   "channels",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Зачем каналы", Text: "Когда агенту нужно решение человека, он не ждёт в фоне — он пишет вам в мессенджер. Здесь же настраиваются креды транспорта: они переопределяют переменные окружения и вступают в силу сразу."},
			{Type: StepNavigate, Title: "Открываем каналы", Route: "/channels", Text: "Карточки каналов: готовность, транспорт, поля."},
			{Type: StepHighlight, Title: "Каналы доставки", TargetID: "page.channels", Text: "Каждый канал показывает состояние готовности — настраивайте от него."},
			{Type: StepNavigate, Title: "Входящие", Route: "/inbound", Text: "Сюда попадают инициативные сообщения из каналов и то, куда их направил пайплайн."},
			{Type: StepHighlight, Title: "Входящие", TargetID: "page.inbound", Text: "Инбокс — вход ворот в организацию: каждое сообщение видно с маршрутом."},
			{Type: StepUserAction, Title: "Настройте канал", TargetID: "page.channels", Text: "Откройте карточку канала и заполните транспортные поля. Нажмите «Готово» после сохранения.", ActionHint: "заполнить поля канала доставки"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: канал доставки настроен."},
			{Type: StepSummary, Title: "Итог", Text: "Организация вышла на связь: вопросы агентов теперь приходят людям."},
		},
		VerifyFeature: "channels",
	},
	{
		ID: "teams-basics", Version: 1, Priority: 170, DurationMin: 8,
		Title:       "Команды: роль как набор агентов",
		Goal:        "Собрать команду агентов, которая запускается одной командой.",
		Description: "Команды — манифест роли: агенты, инструменты и правила, запускаемые единым вызовом.",
		Audience:    []string{"admin", "operator"},
		Page:        "/teams",
		FeatureID:   "teams",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Что такое команда", Text: "Команда — это не просто список: манифест описывает, кто входит, какие инструменты доступны и как распределяется работа. Один запуск команды — и несколько агентов работают по общему плану."},
			{Type: StepNavigate, Title: "Открываем команды", Route: "/teams", Text: "Список команд и мастер сборки."},
			{Type: StepHighlight, Title: "Список команд", TargetID: "page.teams", Text: "Мастер сборки помогает собрать команду из существующих агентов."},
			{Type: StepUserAction, Title: "Соберите команду", TargetID: "page.teams", Text: "Создайте команду или запустите мастера. Нажмите «Готово» после сохранения.", ActionHint: "создать команду агентов"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: команда собрана и включена."},
			{Type: StepSummary, Title: "Итог", Text: "Роль теперь исполняется не одним агентом, а командой."},
		},
		VerifyFeature: "teams",
	},

	// ── Observation ────────────────────────────────────────────────────
	{
		ID: "knowledge-explorer", Version: 1, Priority: 180, DurationMin: 10,
		Title:       "Знания: поиск и карта",
		Goal:        "Найти нужный факт в базе знаний и понять его происхождение.",
		Description: "Хранилище знаний: семантический поиск, карта связей и жизненный цикл фактов.",
		Audience:    []string{"reader", "writer", "operator", "admin"},
		Page:        "/knowledge",
		FeatureID:   "knowledge",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Что хранится", Text: "Агенты запоминают факты, решения и связи между ними. Знание живёт по жизненному циклу: активно → устарело → аннулировано, и любое можно оспорить, если оно показалось сомнительным."},
			{Type: StepNavigate, Title: "Открываем знания", Route: "/knowledge", Text: "Исследователь знаний: поиск, карта, фильтры."},
			{Type: StepHighlight, Title: "Поиск", TargetID: "page.knowledge", Text: "Обычный поиск — по словам, семантический — по смыслу: опишите вопрос своими словами."},
			{Type: StepUserAction, Title: "Найдите факт", TargetID: "page.knowledge", Text: "Выполните поиск по теме вашего проекта и откройте любое знание. Нажмите «Готово» после.", ActionHint: "поиск по базе знаний"},
			{Type: StepSummary, Title: "Итог", Text: "База знаний — память организации: её читают, проверяют и оспаривают."},
		},
		VerifyFeature: "",
	},
	{
		ID: "experience-timeline", Version: 1, Priority: 190, DurationMin: 8,
		Title:       "Лента опыта: что организация узнала",
		Goal:        "Читать хронологию знаний: когда, кто и на каком основании узнал.",
		Description: "Лента опыта — времяслед всего, что организация выучила: цепочки, активации, устаревания.",
		Audience:    []string{"reader", "writer", "operator", "admin"},
		Page:        "/experience",
		FeatureID:   "experience",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Зачем лента", Text: "Отдельное знание отвечает на вопрос «что», лента — на «когда и почему». Цепочки показывают, какие факты вытекли из каких запусков, а фильтры состояния — что уже устарело."},
			{Type: StepNavigate, Title: "Открываем ленту", Route: "/experience", Text: "Группировка по подразделениям, агентам и темам."},
			{Type: StepHighlight, Title: "Хронология", TargetID: "page.experience", Text: "Счётчики состояний наверху: активные, устаревшие, аннулированные, архивные."},
			{Type: StepUserAction, Title: "Попробуйте фильтр", TargetID: "page.experience", Text: "Переключите группировку или фильтр состояния. Нажмите «Готово» после.", ActionHint: "переключить фильтр или группировку ленты"},
			{Type: StepSummary, Title: "Итог", Text: "Лента показывает непрерывность обучения: знания не появляются из ниоткуда."},
		},
		VerifyFeature: "",
	},
	{
		ID: "supervisor-tour", Version: 1, Priority: 200, DurationMin: 8,
		Title:       "Супервайзер: очередь проблем",
		Goal:        "Разобрать очередь супервайзера и понять, что от вас требуется.",
		Description: "Супервайзер собирает упавшие раны, аномалии и конфликты знаний в одну очередь и готовит рекомендации.",
		Audience:    []string{"operator", "admin"},
		Page:        "/supervisor",
		FeatureID:   "supervision",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Что делает супервайзер", Text: "Он смотрит на всю организацию и складывает проблемы в одну очередь: что упало, где аномалия, какие знания противоречат друг другу. Рекомендации готовит, но не применяет — решение за человеком."},
			{Type: StepNavigate, Title: "Открываем супервайзера", Route: "/supervisor", Text: "Очередь кейсов с причинами и предложениями."},
			{Type: StepHighlight, Title: "Очередь кейсов", TargetID: "page.supervisor", Text: "Каждый кейс — конкретная проблема с контекстом: что случилось и что предлагаётся сделать."},
			{Type: StepUserAction, Title: "Разберите кейс", TargetID: "page.supervisor", Text: "Откройте любой кейс и прочитайте причину и рекомендацию. Нажмите «Готово» после.", ActionHint: "открыть кейс супервайзера"},
			{Type: StepSummary, Title: "Итог", Text: "Супервайзер — это не автопилот, а штаб: он готовит решения, вы их принимаете."},
		},
		VerifyFeature: "",
	},
	{
		ID: "supervisor-config", Version: 1, Priority: 210, DurationMin: 10,
		Title:       "Модели супервайзера: судья и роли",
		Goal:        "Настроить модель судью, чтобы вердикты супервайзера заработали.",
		Description: "Роли моделей: судья выносит вердикты, vision — картинки, compaction — сжатие контекста. Без судьи супервайзер молчит.",
		Audience:    []string{"admin"},
		Page:        "/supervisor-model",
		FeatureID:   "supervisor-config",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Роли моделей", Text: "Супервайзер работает на отдельных моделях: судья оценивает результаты и разрешает конфликты знаний, vision подключается для картинок, compaction сжатие контекста. Роли живут отдельно от моделей агентов — их можно дешевить и менять независимо."},
			{Type: StepNavigate, Title: "Открываем настройки", Route: "/supervisor-model", Text: "Форма ролей моделей и коэффициентов оценки."},
			{Type: StepHighlight, Title: "Роль судьи", TargetID: "page.supervisor-model", Text: "Пустое поле судьи означает: вердикты супервайзера выключены."},
			{Type: StepNavigate, Title: "Бюджеты харнесса", Route: "/harness", Text: "Там же живут бюджеты и оценки стоимости — соседняя страница настроек."},
			{Type: StepHighlight, Title: "Харнесс", TargetID: "page.harness", Text: "Бюджеты ограничивают, сколько организация может потратить на запуски."},
			{Type: StepUserAction, Title: "Настройте судью", TargetID: "page.supervisor-model", Text: "Выберите модель для роли судьи и сохраните. Нажмите «Готово» после.", ActionHint: "выбрать модель судью и сохранить"},
			{Type: StepVerification, Title: "Проверяем", Text: "Проверим: модель судьи настроена."},
			{Type: StepSummary, Title: "Итог", Text: "Супервайзер заговорил: у него есть чем судить."},
		},
		VerifyFeature: "supervisor-config",
	},

	// ── Organization operations ────────────────────────────────────────
	{
		ID: "org-operations", Version: 1, Priority: 220, DurationMin: 10,
		Title:       "Операции: активность и межагентные сигналы",
		Goal:        "Видеть, что происходит в подразделениях и кто о ком знает.",
		Description: "Активность организации — события и запуски по узлам; поток осведомленности — сигналы между агентами.",
		Audience:    []string{"operator", "admin"},
		Page:        "/org/activity",
		FeatureID:   "org-operations",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Две призмы операций", Text: "Активность отвечает на «что происходит»: события и запуски, привязанные к подразделениям. Поток осведомленности — на «кто кого знает»: какие сигналы агенты передали друг другу и что из этого вышло."},
			{Type: StepNavigate, Title: "Активность", Route: "/org/activity", Text: "События и активные запуски организации."},
			{Type: StepHighlight, Title: "Доска активности", TargetID: "page.org-activity", Text: "Фильтры по узлам позволяют смотреть на одно подразделение изолированно."},
			{Type: StepNavigate, Title: "Межагентные сигналы", Route: "/org/flow", Text: "Поток осведомленности: кто передал сигнал и что им стало."},
			{Type: StepHighlight, Title: "Поток сигналов", TargetID: "page.org-flow", Text: "Здесь видно, как знания расходятся по организации — и где они застряли."},
			{Type: StepSummary, Title: "Итог", Text: "Операции — глаза руководителя: и события, и коммуникации агентов в одном месте."},
		},
		VerifyFeature: "",
	},

	// ── Meta ───────────────────────────────────────────────────────────
	{
		ID: "learning-meta", Version: 1, Priority: 230, DurationMin: 6,
		Title:       "Центр обучения: как этим пользоваться",
		Goal:        "Уметь строить программу, читать освоение и принимать подсказки.",
		Description: "Программа — ваш индивидуальный курс, освоение — фактические отметки, подсказки — что делать дальше.",
		Audience:    []string{"reader", "writer", "operator", "admin"},
		Page:        "/learning",
		FeatureID:   "learning-meta",
		Steps: []LearningStep{
			{Type: StepExplain, Title: "Три слоя центра", Text: "Программа — следующий урок с объяснением «зачем именно вам». Освоение возможностей — не клики, а факты: ядро проверяет конфигурацию и ставит отметку само. Подсказки — рекомендации супервайзера, их можно принять или отклонить."},
			{Type: StepNavigate, Title: "Открываем центр", Route: "/learning", Text: "Программа, возможности и каталог уроков."},
			{Type: StepHighlight, Title: "Следующий шаг", TargetID: "page.learning", Text: "Карточка «Следующий шаг» — ваше единственное обязательное действие: одна кнопка, один урок."},
			{Type: StepUserAction, Title: "Соберите программу", TargetID: "page.learning", Text: "Нажмите «Собрать программу», если её ещё нет, и взгляните на список уроков. Нажмите «Готово» после.", ActionHint: "собрать программу обучения"},
			{Type: StepSummary, Title: "Итог", Text: "Обучение ведёт себя как работа: один следующий шаг, честные отметки, никакого шума."},
		},
		VerifyFeature: "",
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
