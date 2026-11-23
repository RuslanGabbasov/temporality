package main

// Learning highlights — the supervisor's expressiveness feed
// (docs/ai-guided-onboarding-and-learning.md §10.2 extended): instead of
// shipping every lesson pre-written in code, the catalog ships two compact
// FACT feeds the generation case turns into per-user lessons:
//
//   - page highlights: what each interface IS and which semantic targets it
//     exposes — the model's understanding of the product's screens;
//   - version highlights: what changed in each release and where — the
//     product increment the model turns into «what's new» lessons for users
//     who already know the old behaviour.
//
// A developer adds ONE entry here when shipping a feature (the same rhythm
// as the capability registry) — the lesson itself is generated per user,
// against their factual progress delta, not pre-baked for everyone.

// pageHighlight is the model-facing description of one screen.
type pageHighlight struct {
	Route   string   // in-app route, e.g. "/providers"
	Title   string   // human title, e.g. "Провайдеры моделей"
	About   string   // what this interface is for and when a user needs it
	Targets []string // semantic data-learn ids exposed on the page
}

// learningPageHighlights is the interface map: every teachable screen with
// its purpose and the targets the spotlight can resolve on it. The lesson
// generation case picks the relevant page and turns this into steps.
var learningPageHighlights = []pageHighlight{
	{
		Route: "/dashboard", Title: "Дашборд",
		About:   "Состояние организации: метрики, работа, активность, саги, очередь супервайзера, пользователи и расходы. Рабочая точка входа руководителя.",
		Targets: []string{"page.dashboard"},
	},
	{
		Route: "/workspace", Title: "Рабочий стол",
		About:   "Переписка с агентами: диалоги слева, сообщения справа. Отсюда ставятся задачи и приходят ответы — первый экран реальной работы.",
		Targets: []string{"page.workspace"},
	},
	{
		Route: "/agents", Title: "Запуски",
		About:   "Список запусков с трассами: ходы, вызовы инструментов, токены, ошибки. Точка диагностики — почему ран упал или что он делал.",
		Targets: []string{"page.runs"},
	},
	{
		Route: "/agent-config", Title: "Агенты",
		About:   "Конфигурация агентов: роль, цель, модель, системный промпт, навыки, права и делегирование. Карточка агента — место настройки исполнителя.",
		Targets: []string{"page.agent-config", "agent.delegation.configuration", "agent.roles.reviewer"},
	},
	{
		Route: "/org", Title: "Организация",
		About:   "Дерево подразделений с ролями: структура задаёт видимость, ответственность и контур согласований.",
		Targets: []string{"org.tree"},
	},
	{
		Route: "/org/work", Title: "Доска работы",
		About:   "Контроль-тауэр: задачи по стадиям (запланировано, в работе, ожидание, ревью, завершено), приём результатов и разбор упавших.",
		Targets: []string{"orgwork.column.review"},
	},
	{
		Route: "/org/activity", Title: "Активность организации",
		About:   "События и активные запуски по подразделениям: что происходит в организации прямо сейчас.",
		Targets: []string{"page.org-activity"},
	},
	{
		Route: "/org/flow", Title: "Межагентные сигналы",
		About:   "Поток осведомленности: какие сигналы агенты передали друг другу, что решили и где сигнал застрял.",
		Targets: []string{"page.org-flow"},
	},
	{
		Route: "/artifacts", Title: "Артефакты",
		About:   "Реестр документов с версиями и кругами согласования: агент готовит черновик, человек утверждает или возвращает.",
		Targets: []string{"artifact.approval.rules"},
	},
	{
		Route: "/knowledge", Title: "Хранилище знаний",
		About:   "Поиск и карта того, что агенты запомнили: семантический поиск, жизненный цикл фактов, оспорение сомнительных знаний.",
		Targets: []string{"page.knowledge"},
	},
	{
		Route: "/experience", Title: "Лента опыта",
		About:   "Хронология обучения организации: цепочки знаний, активации, устаревания. Отвечает на «когда и почему узнали».",
		Targets: []string{"page.experience"},
	},
	{
		Route: "/supervisor", Title: "Супервайзер",
		About:   "Очередь проблем: упавшие раны, аномалии, конфликты знаний. Готовит рекомендации, но не применяет их без человека.",
		Targets: []string{"page.supervisor"},
	},
	{
		Route: "/supervisor-model", Title: "Модели супервайзера",
		About:   "Роли моделей: судья (вердикты и конфликты), vision, compaction, заголовки. Без судьи вердикты супервайзера выключены.",
		Targets: []string{"page.supervisor-model"},
	},
	{
		Route: "/harness", Title: "Харнесс",
		About:   "Бюджеты запусков, лимиты и коэффициенты оценки human-attention: сколько организация может потратить и как считаются оценки.",
		Targets: []string{"page.harness"},
	},
	{
		Route: "/providers", Title: "Провайдеры моделей",
		About:   "Подключение моделей: base URL, ключ, список моделей. Без провайдера раны не стартуют; выключенный провайдер падает явно.",
		Targets: []string{"page.providers"},
	},
	{
		Route: "/secrets", Title: "Секреты",
		About:   "API-ключи и токены: хранятся в workspace, выдаются агентам только по ссылке secret:// — в чат и логи значение не попадает.",
		Targets: []string{"page.secrets"},
	},
	{
		Route: "/mcp", Title: "MCP-серверы",
		About:   "Внешние инструменты агента: stdio/sse/http, белые списки allowed_tools, инструменты с подтверждением, таймауты.",
		Targets: []string{"page.mcp"},
	},
	{
		Route: "/skills", Title: "Навыки",
		About:   "Каталог SKILL-инструкций с версиями: агенты предлагают улучшения как черновики, человек применяет или отклоняет.",
		Targets: []string{"page.skills"},
	},
	{
		Route: "/sandbox", Title: "Песочницы",
		About:   "Изолированные контейнеры для кода агентов: профили, сборка образов, лимиты памяти/CPU. Без собранного образа run_command падает явно.",
		Targets: []string{"page.sandbox"},
	},
	{
		Route: "/storage", Title: "Файловое хранилище",
		About:   "Файлы проекта в workspace: дерево директорий, размеры, поиск. Общий стол для вас и агентов.",
		Targets: []string{"page.storage"},
	},
	{
		Route: "/triggers", Title: "Триггеры",
		About:   "Автозапуски: расписание, вебхуки, события. Работают под своей учётной записью — права автоматизации не наследуются от создателя.",
		Targets: []string{"page.triggers"},
	},
	{
		Route: "/users", Title: "Пользователи",
		About:   "Участники и роли (reader/writer/operator/admin), членство в подразделениях, токены и пароли.",
		Targets: []string{"page.users"},
	},
	{
		Route: "/teams", Title: "Команды агентов",
		About:   "Манифест роли: набор агентов, запускаемый одной командой по общему плану. Мастер сборки помогает собрать команду.",
		Targets: []string{"page.teams"},
	},
	{
		Route: "/channels", Title: "Каналы доставки",
		About:   "Куда агенты шлют вопросы людям: telegram/matrix/slack, транспортные креды переопределяют env и вступают в силу сразу.",
		Targets: []string{"page.channels"},
	},
	{
		Route: "/inbound", Title: "Входящие",
		About:   "Инбокс инициативных сообщений из каналов и маршрутизация пайплайна: куда направилось каждое сообщение.",
		Targets: []string{"page.inbound"},
	},
	{
		Route: "/learning", Title: "Центр обучения",
		About:   "Программа (следующий шаг), освоение возможностей по фактам конфигурации, подсказки супервайзера и каталог уроков.",
		Targets: []string{"page.learning"},
	},
}

// versionChange is one release increment tied to the screen it touched.
type versionChange struct {
	Page   string // route the change lives on, "" = app-wide
	Title  string // one line: what changed
	Detail string // one-two sentences: what a user should now do differently
}

// releaseHighlights is the product increment of one release: the model turns
// these into «what's new» lessons for users who already learned the old
// behaviour (§10.2 — a developer appends an entry when cutting a release).
type releaseHighlights struct {
	Version string
	Changes []versionChange
}

var learningVersionHighlights = []releaseHighlights{
	{
		Version: "v2026.6.3",
		Changes: []versionChange{
			{Page: "/mcp", Title: "Таймаут у каждого MCP-сервера", Detail: "На карточке сервера появилось поле максимального времени вызова — зависший инструмент больше не держит весь ран."},
			{Page: "/dashboard", Title: "Расходы супервайзера", Detail: "На вкладке Supervisor видны токены и доллары по моделям судьи — понятно, во сколько обходится наблюдение."},
			{Page: "/users", Title: "Досье человека", Detail: "Вкладка Users получила липкую шапку, фильтры и досье за период: кем человек был занят и что он сделал."},
		},
	},
	{
		Version: "v2026.6.2",
		Changes: []versionChange{
			{Page: "/dashboard", Title: "Расходы переехали на дашборд", Detail: "Панель Использование стала вкладкой дашборда: менеджер читает расходы рядом с работой, а не в настройках организации."},
			{Page: "", Title: "Расширенный каталог визуализаций", Detail: "Scatter, heatmap, гистограммы, радар, Санкей, swimlane и treemap — презентации агентов стали читаться как отчёт, а не как таблица."},
			{Page: "/org", Title: "Месячные лимиты политики", Detail: "У политики появился бюджет на календарный месяц: трата считается по месяцу и упирается в лимит."},
			{Page: "/org/flow", Title: "Сигналы переименованы", Detail: "Вкладка теперь называется «Межагентные сигналы», карточки локализованы, кнопки действий — только на живых сигналах."},
		},
	},
	{
		Version: "v2026.6.1",
		Changes: []versionChange{
			{Page: "/learning", Title: "Наставник первым", Detail: "Контекстные подсказки уроков сворачиваются в круглую кнопку академической шапки; обучение больше не занимает пункт меню."},
			{Page: "/sandbox", Title: "Прогресс сборки образов", Detail: "Живая панель показывает, что и как собирается в образе песочницы — сборка перестала быть чёрным ящиком."},
			{Page: "/agent-config", Title: "Шаблон Lead по умолчанию", Detail: "Галерея шаблонов агентов переносится в несколько колонок, а первым выбран шаблон Lead-агента."},
			{Page: "/artifacts", Title: "Предпросмотры файлов", Detail: "Маркдаун рендерится как документ, код — с подсветкой; ссылки на файлы workspace проверяют существование, прежде чем стать кликабельными."},
			{Page: "/org/work", Title: "Отменённые раны уходят в завершённые", Detail: "Отменённый запуск больше не висит в колонке «В работе» — доска показывает только живое."},
		},
	},
	{
		Version: "v2026.6.0",
		Changes: []versionChange{
			{Page: "/learning", Title: "AI-обучение целиком", Detail: "Программа обучения, уроки с verification-гейтами, проактивные подсказки и освоение возможностей по фактам конфигурации."},
			{Page: "/org/work", Title: "Вердикты ревью", Detail: "Единственный исход ревью — «Принять: в завершённые»; кнопка действует с одного клика и ничего не уничтожает."},
			{Page: "/knowledge", Title: "Очередь разбора знаний", Detail: "Оспаривания разбираются пачкой и владеют экраном — thousands строк больше не живут в узкой колонке."},
			{Page: "", Title: "Установка одной строкой", Detail: "install.sh ставит весь стек сам, пропуская занятые порты и диагностируя docker."},
		},
	},
}

// pageHighlightFor returns the highlight of one route ("" when unknown).
func pageHighlightFor(route string) *pageHighlight {
	for i := range learningPageHighlights {
		if learningPageHighlights[i].Route == route {
			return &learningPageHighlights[i]
		}
	}
	return nil
}

// releaseFor returns the highlights of one release version: an exact match
// (a dev build runs the code of the newest entry) or, as a fallback, the
// newest entry — «what's new» must always have material to teach.
func releaseFor(version string) *releaseHighlights {
	for i := range learningVersionHighlights {
		if learningVersionHighlights[i].Version == version {
			return &learningVersionHighlights[i]
		}
	}
	if len(learningVersionHighlights) > 0 {
		return &learningVersionHighlights[0]
	}
	return nil
}

// teachableRoutes is the list of pages a lesson may be generated for: what
// the GET /lessons response advertises to the nudge and the learning center.
func teachableRoutes() []string {
	routes := make([]string, 0, len(learningPageHighlights))
	for _, h := range learningPageHighlights {
		routes = append(routes, h.Route)
	}
	return routes
}

// allLearningRoutes is the whitelist of navigable routes for generated
// lessons: page highlights plus the routes the static catalog teaches.
func allLearningRoutes() []string {
	seen := map[string]bool{}
	routes := []string{}
	for _, h := range learningPageHighlights {
		if !seen[h.Route] {
			seen[h.Route] = true
			routes = append(routes, h.Route)
		}
	}
	for _, l := range learningLessons {
		if l.Page != "" && !seen[l.Page] {
			seen[l.Page] = true
			routes = append(routes, l.Page)
		}
	}
	return routes
}

// allLearningTargets is the whitelist of semantic target ids a generated
// lesson may highlight: the supervisor catalog plus every page marker.
func allLearningTargets() []string {
	seen := map[string]bool{}
	ids := []string{}
	for id := range learningTargetCatalog {
		seen[id] = true
		ids = append(ids, id)
	}
	for _, h := range learningPageHighlights {
		for _, t := range h.Targets {
			if !seen[t] {
				seen[t] = true
				ids = append(ids, t)
			}
		}
	}
	return ids
}
