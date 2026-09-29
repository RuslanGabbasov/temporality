import { createContext, useContext, useState, useCallback, type ReactNode } from 'react'

type Locale = 'en' | 'ru'

interface I18nContextValue {
  locale: Locale
  setLocale: (l: Locale) => void
  t: (key: string, vars?: Record<string, string>) => string
}

const translations: Record<Locale, Record<string, string>> = {
  en: {
    // Navigation
    'nav.timeline': 'Timeline',
    'nav.runs': 'Runs',
    'nav.operations': 'Operations',
    'nav.knowledge': 'Knowledge',
    'nav.agents': 'Agents',
    'nav.triggers': 'Triggers',
    'nav.providers': 'Providers',
    'nav.users': 'Users',

    // Common actions
    'action.create': 'Create',
    'action.save': 'Save',
    'action.cancel': 'Cancel',
    'action.delete': 'Delete',
    'action.edit': 'Edit',
    'action.refresh': 'Refresh',
    'action.close': 'Close',
    'action.copy': 'Copy',
    'action.skip': 'Skip',
    'action.search': 'Search',
    'action.confirm': 'Confirm',
    'action.regenerate': 'Regenerate',
    'action.add': 'Add',

    // New entity buttons
    'new.project': 'New project',
    'new.agent': 'New agent',
    'new.provider': 'Add provider',
    'new.user': 'New user',
    'new.trigger': 'New trigger',
    'new.chat': 'New chat',
    'new.conversation': 'New conversation',

    // Workspace / Chat
    'chat.send': 'Send',
    'chat.stop': 'Stop',
    'chat.branch': 'Branch',
    'chat.streaming': 'streaming…',
    'chat.thinking': 'Thinking',
    'chat.default_agent': 'Default agent',
    'chat.no_default': 'No default',
    'chat.project_default': 'Project default',
    'chat.agent': 'Agent',
    'chat.select_agent': 'Select agent',
    'chat.no_messages': 'Send a message to start the conversation.',
    'chat.title': 'Temporality Agent',
    'chat.run_started': 'Run started',
    'chat.run_cancelled': 'Run cancelled by user',
    'chat.run_failed': 'Run failed',
    'chat.no_answer': 'No answer received',

    // Runs
    'runs.title': 'Runs',
    'runs.select': 'Select a run',
    'runs.select_hint': 'Click a run on the left to see its trace.',
    'runs.no_runs': 'No runs for this project',
    'runs.answer': 'Agent Answer',

    // Operations
    'operations.title': 'Operations',
    'operations.no_ops': 'No unresolved operations',
    'operations.no_ops_hint': 'All tool executions have been confirmed.',
    'operations.select': 'Select an operation',
    'operations.select_hint': 'Click an operation on the left to inspect it and record your verdict.',
    'operations.occurred': 'Occurred',
    'operations.not_occurred': 'Did NOT occur',
    'operations.unknown': 'Unknown',
    'operations.batch_occurred': 'All occurred',
    'operations.batch_none': 'All none',
    'operations.batch_unknown': 'All unknown',
    'operations.batch_clear': 'Clear',
    'operations.selected': 'selected',
    'operations.unresolved': 'unresolved',

    // Agents
    'agents.title': 'Agents',
    'agents.from_template': 'From template',
    'agents.project_default': 'project default',
    'agents.no_model': 'no model',
    'agents.default': 'default',
    'agents.delete_confirm': 'Delete agent "{{name}}"?',

    // Providers
    'providers.title': 'Model Providers',
    'providers.add': 'Add provider',
    'providers.connected': 'Connected',
    'providers.no_providers': 'No providers yet',
    'providers.no_providers_hint': 'Connect a model provider to start running agents.',
    'providers.name': 'Provider name',
    'providers.url': 'Base URL',
    'providers.key': 'API key',
    'providers.models': 'Models (comma-separated)',
    'providers.delete_confirm': 'Delete provider "{{name}}"?',

    // Users
    'users.title': 'Users',
    'users.token_for': 'Token for {{name}}',
    'users.token_hint': 'Token stored in database. User can log in immediately after kernel restart.',
    'users.copy_close': 'Copy & Close',
    'users.regenerate_confirm': 'Regenerate token?',
    'users.regenerate_warning': '{{name}} already has an active token. Generating a new one will invalidate the current token immediately — the user will be logged out.',
    'users.active': 'active',
    'users.inactive': 'inactive',

    // Triggers
    'triggers.title': 'Triggers',
    'triggers.no_triggers': 'No triggers configured',
    'triggers.enabled': 'enabled',
    'triggers.disabled': 'disabled',
    'triggers.schedule': 'Schedule',
    'triggers.webhook': 'Webhook',
    'triggers.event': 'Event',

    // Projects
    'projects.title': 'Projects',
    'projects.delete_confirm': 'Delete project?',
    'projects.delete_warning': 'Project "{{name}}" and all its runs, tasks, and knowledge will be permanently deleted.',

    // Knowledge
    'knowledge.title': 'Knowledge',
    'knowledge.overview': 'Overview',
    'knowledge.patterns': 'Patterns',
    'knowledge.alive': 'Alive',
    'knowledge.stale': 'Stale',
    'knowledge.invalidated': 'Invalidated',
    'knowledge.archived': 'Archived',

    // Timeline
    'timeline.title': 'Experience Timeline',
    'timeline.all': 'All',
    'timeline.active_only': 'Active only',
    'timeline.activated': 'Activated',
    'timeline.cross_scope': 'Cross-scope',
    'timeline.strong_recent': 'Strong & recent',
    'timeline.dead': 'Dead',

    // Onboarding
    'onboarding.welcome': 'Welcome to Temporality',
    'onboarding.welcome_desc': 'Let\'s set up your workspace in a few steps.',
    'onboarding.get_started': 'Get started',
    'onboarding.project_title': 'Create your first project',
    'onboarding.project_desc': 'A project groups your agents, runs, and knowledge.',
    'onboarding.provider_title': 'Add a model provider',
    'onboarding.provider_desc': 'Connect an OpenAI-compatible API to power your agents.',
    'onboarding.agent_title': 'Create an agent',
    'onboarding.agent_desc': 'Configure how your agent behaves and which model it uses.',
    'onboarding.done_title': 'You\'re all set!',
    'onboarding.done_desc': 'Your workspace is ready. Start a conversation with your agent.',
    'onboarding.open_workspace': 'Open workspace',
    'onboarding.create_project': 'Create project',
    'onboarding.project_name': 'Project name',
    'onboarding.project_placeholder': 'e.g. my-team, backend-agents',

    // Settings
    'settings.language': 'Language',

    // Status
    'status.completed': 'completed',
    'status.failed': 'failed',
    'status.running': 'running',
    'status.cancelled': 'cancelled',
    'status.pending': 'pending',
  },
  ru: {
    // Navigation
    'nav.timeline': 'Таймлайн',
    'nav.runs': 'Запуски',
    'nav.operations': 'Операции',
    'nav.knowledge': 'Знания',
    'nav.agents': 'Агенты',
    'nav.triggers': 'Триггеры',
    'nav.providers': 'Провайдеры',
    'nav.users': 'Пользователи',

    // Common actions
    'action.create': 'Создать',
    'action.save': 'Сохранить',
    'action.cancel': 'Отмена',
    'action.delete': 'Удалить',
    'action.edit': 'Изменить',
    'action.refresh': 'Обновить',
    'action.close': 'Закрыть',
    'action.copy': 'Копировать',
    'action.skip': 'Пропустить',
    'action.search': 'Поиск',
    'action.confirm': 'Подтвердить',
    'action.regenerate': 'Пересоздать',
    'action.add': 'Добавить',

    // New entity buttons
    'new.project': 'Новый проект',
    'new.agent': 'Новый агент',
    'new.provider': 'Добавить провайдер',
    'new.user': 'Новый пользователь',
    'new.trigger': 'Новый триггер',
    'new.chat': 'Новый чат',
    'new.conversation': 'Новый разговор',

    // Workspace / Chat
    'chat.send': 'Отправить',
    'chat.stop': 'Стоп',
    'chat.branch': 'Ветка',
    'chat.streaming': 'загрузка…',
    'chat.thinking': 'Размышление',
    'chat.default_agent': 'Агент по умолчанию',
    'chat.no_default': 'Не задан',
    'chat.project_default': 'Агент проекта',
    'chat.agent': 'Агент',
    'chat.select_agent': 'Выберите агента',
    'chat.no_messages': 'Отправьте сообщение, чтобы начать разговор.',
    'chat.title': 'Агент Temporality',
    'chat.run_started': 'Запуск начат',
    'chat.run_cancelled': 'Запуск отменён пользователем',
    'chat.run_failed': 'Запуск завершился ошибкой',
    'chat.no_answer': 'Ответ не получен',

    // Runs
    'runs.title': 'Запуски',
    'runs.select': 'Выберите запуск',
    'runs.select_hint': 'Нажмите на запуск слева, чтобы увидеть трассировку.',
    'runs.no_runs': 'В проекте пока нет запусков',
    'runs.answer': 'Ответ агента',

    // Operations
    'operations.title': 'Операции',
    'operations.no_ops': 'Нет нерешённых операций',
    'operations.no_ops_hint': 'Все вызовы инструментов подтверждены.',
    'operations.select': 'Выберите операцию',
    'operations.select_hint': 'Нажмите на операцию слева, чтобы проверить её и дать оценку.',
    'operations.occurred': 'Выполнено',
    'operations.not_occurred': 'Не выполнено',
    'operations.unknown': 'Неизвестно',
    'operations.batch_occurred': 'Все выполнены',
    'operations.batch_none': 'Все нет',
    'operations.batch_unknown': 'Все неизвестно',
    'operations.batch_clear': 'Сбросить',
    'operations.selected': 'выбрано',
    'operations.unresolved': 'нерешённых',

    // Agents
    'agents.title': 'Агенты',
    'agents.from_template': 'Из шаблона',
    'agents.project_default': 'по умолчанию',
    'agents.no_model': 'нет модели',
    'agents.default': 'по умолчанию',
    'agents.delete_confirm': 'Удалить агента «{{name}}»?',

    // Providers
    'providers.title': 'Провайдеры моделей',
    'providers.add': 'Добавить провайдер',
    'providers.connected': 'Подключён',
    'providers.no_providers': 'Провайдеров пока нет',
    'providers.no_providers_hint': 'Подключите провайдер моделей, чтобы запускать агентов.',
    'providers.name': 'Название провайдера',
    'providers.url': 'Базовый URL',
    'providers.key': 'API-ключ',
    'providers.models': 'Модели (через запятую)',
    'providers.delete_confirm': 'Удалить провайдер «{{name}}»?',

    // Users
    'users.title': 'Пользователи',
    'users.token_for': 'Токен для {{name}}',
    'users.token_hint': 'Токен сохранён в базе. Пользователь может войти сразу после перезагрузки ядра.',
    'users.copy_close': 'Копировать и закрыть',
    'users.regenerate_confirm': 'Пересоздать токен?',
    'users.regenerate_warning': 'У {{name}} уже есть активный токен. Создание нового немедленно аннулирует текущий — пользователь будет вынужден войти заново.',
    'users.active': 'активен',
    'users.inactive': 'неактивен',

    // Triggers
    'triggers.title': 'Триггеры',
    'triggers.no_triggers': 'Триггеры не настроены',
    'triggers.enabled': 'включён',
    'triggers.disabled': 'отключён',
    'triggers.schedule': 'Расписание',
    'triggers.webhook': 'Вебхук',
    'triggers.event': 'Событие',

    // Projects
    'projects.title': 'Проекты',
    'projects.delete_confirm': 'Удалить проект?',
    'projects.delete_warning': 'Проект «{{name}}» и все его запуски, задачи и знания будут удалены безвозвратно.',

    // Knowledge
    'knowledge.title': 'Знания',
    'knowledge.overview': 'Обзор',
    'knowledge.patterns': 'Паттерны',
    'knowledge.alive': 'Живые',
    'knowledge.stale': 'Устаревшие',
    'knowledge.invalidated': 'Инвалидированные',
    'knowledge.archived': 'Архивные',

    // Timeline
    'timeline.title': 'Таймлайн опыта',
    'timeline.all': 'Все',
    'timeline.active_only': 'Только активные',
    'timeline.activated': 'Активированные',
    'timeline.cross_scope': 'Межобластные',
    'timeline.strong_recent': 'Сильные и свежие',
    'timeline.dead': 'Мёртвые',

    // Onboarding
    'onboarding.welcome': 'Добро пожаловать в Temporality',
    'onboarding.welcome_desc': 'Настроим ваше рабочее пространство за несколько шагов.',
    'onboarding.get_started': 'Начать',
    'onboarding.project_title': 'Создайте первый проект',
    'onboarding.project_desc': 'Проект объединяет агентов, запуски и знания.',
    'onboarding.provider_title': 'Добавьте провайдер моделей',
    'onboarding.provider_desc': 'Подключите OpenAI-совместимый API для ваших агентов.',
    'onboarding.agent_title': 'Создайте агента',
    'onboarding.agent_desc': 'Настройте поведение агента и выберите модель.',
    'onboarding.done_title': 'Всё готово!',
    'onboarding.done_desc': 'Рабочее пространство настроено. Начните разговор с агентом.',
    'onboarding.open_workspace': 'Открыть рабочее пространство',
    'onboarding.create_project': 'Создать проект',
    'onboarding.project_name': 'Название проекта',
    'onboarding.project_placeholder': 'например, my-team, backend-agents',

    // Settings
    'settings.language': 'Язык',

    // Status
    'status.completed': 'завершён',
    'status.failed': 'ошибка',
    'status.running': 'выполняется',
    'status.cancelled': 'отменён',
    'status.pending': 'ожидание',
  },
}

const LOCALE_STORAGE_KEY = 'temporality_locale'

function getInitialLocale(): Locale {
  const stored = localStorage.getItem(LOCALE_STORAGE_KEY) as Locale | null
  if (stored && (stored === 'en' || stored === 'ru')) return stored
  return navigator.language.startsWith('ru') ? 'ru' : 'en'
}

const I18nContext = createContext<I18nContextValue>({
  locale: 'en',
  setLocale: () => {},
  t: (key) => key,
})

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(getInitialLocale)

  const setLocale = useCallback((l: Locale) => {
    setLocaleState(l)
    localStorage.setItem(LOCALE_STORAGE_KEY, l)
  }, [])

  const t = useCallback((key: string, vars?: Record<string, string>) => {
    let text = translations[locale][key] ?? translations.en[key] ?? key
    if (vars) {
      Object.entries(vars).forEach(([k, v]) => {
        text = text.replace(`{{${k}}}`, v)
      })
    }
    return text
  }, [locale])

  return <I18nContext.Provider value={{ locale, setLocale, t }}>{children}</I18nContext.Provider>
}

export function useI18n() {
  return useContext(I18nContext)
}

export function useT() {
  return useI18n().t
}

export type { Locale }