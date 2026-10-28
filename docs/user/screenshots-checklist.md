# Чек-лист скриншотов для сайта и гайда

Куда класть: `pages/assets/` (плоско, без подпапок). Формат — **webp**
(`cwebp -q 82 in.png -o out.webp`), ширина 1280–1600px (retina ×2, если можно),
тема — та, что выглядит лучше (светлая сейчас дефолт). Данные на скринах —
не боевые: тестовый проект, вымышленные имена.

Гайд (RU и EN) ссылается на имена из колонки «Файл» — `npm run build:docs`
пересобирает `pages/docs/` из `docs/user/`.

## Что уже есть и где используется

| Файл | Что на скрине | Где виден |
|---|---|---|
| `timeline.webp` | Таймлайн опыта | герой главной, `operations` |
| `dashboard.webp` | Дашборд | `intro` (RU+EN), `chat` (EN) |
| `chat-run.webp` | Чат с живым ходом | главная, `chat`, `hitl-forms` |
| `run.webp` | Трасса рана | главная, `chat`, `observability` |
| `agent-edit.webp` | Карточка агента | `agents` |
| `agent-evolution.webp` | Эволюция: гейт, версии | главная, `agents`, `evolution` |
| `skill-edit.webp` | Редактор навыка | главная, `skills` |
| `skill-evaluation.webp` | Оценки навыка | `skills` |
| `knowledge.webp` | Карта знаний | `knowledge` |
| `trigger.webp` | Триггеры | главная, `triggers` |
| `team.webp` | Пользователи и роли | `administration` |
| `storage.webp` | Хранилища | `administration` |
| `sandbox.webp` | Песочницы | `administration` |

## Чего не хватает (места уже помечены комментариями в md)

| Файл | Что снять | Куда встанет |
|---|---|---|
| `chat-file-chip.webp` | чип «📎 report.md» после `send_file` | `files` (RU+EN, «В чате») |
| `telegram-sendfile.webp` | файл, пришедший в Telegram | `files` (RU+EN, «В мессенджерах») |
| `hosted-form-desktop.webp` | страница `/kernel-api/forms/<token>`, desktop-ширина | `hitl-forms` (RU+EN, «Универсальный путь») |
| `agents-candidates.webp` | очередь golden-кандидатов + «Accept all» | `evolution` (RU+EN, «Откуда берутся кейсы») |

Опционально для насыщенности (сейчас эти разделы без скринов):
`matrix-card.webp` (HTML-карточка вопроса в Element), `telegram-form-button.webp`
(inline-кнопка «Заполнить форму»), `hosted-form-mobile.webp` (форма на ~390px),
`users-job-title.webp` (поле «Должность» в карточке пользователя), `job-titles`
- любой список/карточку с должностями.

## Как снимать аккуратно

1. Тестовые данные: проект «demo», вымышленный клиент, файл `report.md`.
2. Перед скрином закрыть тултипы/уведомления (или наоборот — если они и есть
   объект, как у override-панели).
3. Браузер без лишних вкладок в кадре; масштаб 100%.
4. Для мобильной ширины — DevTools → device toolbar → iPhone 14.
5. Не кадрировать слишком плотно: оставь контекст (заголовок страницы).
