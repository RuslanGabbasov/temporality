# Подписки Claude Code и Codex как провайдеры моделей

**Статус:** фаза 1 (Claude по подписке) реализована; Codex — в бэклоге.
**Дата:** 2026-11-16 (факты проверены по официальной документации Anthropic и исходникам `openai/codex` на момент написания).

> **Реализация (фаза 1, 2026-11-16).** Подписка Claude сделана через
> `auth=anthropic_oauth` на провайдере + полный адаптер `/v1/messages` в
> `kernel/llm/client_anthropic.go` — вместо ставки на недокументированный
> compat-слой (§3.1, «открытый вопрос №1» снят: OAuth-токен ходит по
> Messages-проводу, как сам Claude Code). Пресет «Claude (подписка)» в UI
> (Providers + онбординг) с инструкцией по `claude setup-token`, автосекрет
> `secret://claude-subscription-token`, seed-список моделей на случай
> закрытого каталога, «Test generation» ездит по тому же проводу.
> Роутинг отдаёт `auth` в ответе kernel, kernel строит Messages-клиент;
> поток-стражи (watchdogs) вынесены в общий `streamWatchdogs` для обоих
> проводов.

Цель: поддержать в temporality доступ к моделям через **подписки** — Claude Pro/Max/Ultra/Team (через механизм Claude Code) и ChatGPT Plus/Pro/Business/Enterprise (через механизм Codex), — не ломая существующую модель провайдеров `base_url` + `api_key_ref`.

---

## 1. Контекст temporality

Текущая архитектура, на которую ложится задача:

- Провайдер = строка `workspace_provider` (`id, name, base_url, api_key_ref, models, labels, model_limits, max_concurrent_requests, enabled, org_unit_id`). `api_key_ref` — `secret://…`, имя env-переменной или legacy-литерал.
- Ключ резолвится **на границе маршрутизации** (`resolveProviderAPIKey` в `cmd/agent-kernel/main.go`): plaintext-ключи не живут в БД (docs/secrets-management.md §4), есть envelope-секреты с `KERNEL_SECRET_MASTER_KEY`.
- `kernel/llm/client.go` — OpenAI chat-completions клиент с tool calling и стримингом + спец-слой `anthropicCompatHost` для `api.anthropic.com` (каталог `/models` через `x-api-key` + `anthropic-version`, strip неподдерживаемых полей на compat-слое chat/completions).
- Каталог моделей провайдера синхронизируется из `GET {base_url}/models` (`cmd/agent-kernel/providermodels.go`), для Anthropic — с `x-api-key`.
- Ограничения провайдера (`model_limits`, `max_concurrent_requests`) + retry-ladder уже есть; диагностика `credentialSource` в `kernel/agent/activities.go` классифицирует, откуда брался ключ.

## 2. Как подписки устроены на самом деле

Ключевой факт: оба вендора выдают доступ по подписке **через OAuth-токены, живущие внутри их CLI**, а не через обычные API-ключи. API-ключи (pay-per-token) — параллельный, независимый путь.

### 2.1 Claude (Claude Code / claude.ai подписка)

Подтверждено официальной документацией (docs.claude.com → Claude Code → Authentication):

- Логин через `/login` создаёт **subscription OAuth credentials** (по умолчанию для Pro/Max/Ultra/Team/Enterprise). Хранятся в macOS Keychain либо `~/.claude/.credentials.json` (mode 0600; на Windows — в профиле пользователя); `CLAUDE_CONFIG_DIR` переносит путь.
- **`claude setup-token`** — официальный путь для CI/автоматики: тот же browser-flow, на выходе **OAuth-токен сроком на 1 год**, печатается в терминал и нигде не сохраняется. Используется через env `CLAUDE_CODE_OAUTH_TOKEN`. Требует Pro/Max/Ultra/Team/Enterprise. Ограничение: **только model requests** (не работает Remote Control, claude.ai connectors; локальные MCP — работают).
- Приоритет креденшелов CLI: cloud-провайдеры → `ANTHROPIC_AUTH_TOKEN` (Bearer) → `ANTHROPIC_API_KEY` (x-api-key) → `apiKeyHelper` → `CLAUDE_CODE_OAUTH_TOKEN` → профили/federation → subscription OAuth.
- `/login`-токены истекают (за 3 дня до истечения CLI показывает предупреждение) и обновляются самим CLI через refresh-токены.

Из экосистемы (opencode и другие сторонние харнесы; перед реализацией перепроверить):

- Токен имеет вид `sk-ant-oat01-…`; при запросах к `api.anthropic.com` передаётся как `Authorization: Bearer` (не `x-api-key`) + заголовок **`anthropic-beta: oauth-2025-04-20`**.
- Wire-формат — нативный Anthropic **`/v1/messages`** (Claude Code сам так ходит). Работает ли OAuth-токен на OpenAI-compat слое `/v1/chat/completions` — **неизвестно, надо проверять первым делом**.
- OAuth-флоу `/login`: loopback-сервер на порту 54545, authorize на `claude.ai/oauth/authorize`, обмен кода на токены через `platform.claude.com/api/oauth/token`, короткоживущий access + refresh.

### 2.2 Codex (подписка ChatGPT)

Подтверждено исходниками `openai/codex` (модули `codex-rs/login/*`, `codex-rs/model-provider-info/*`):

- README: «Sign in with ChatGPT» — рекомендуемый путь для Plus, Pro, Business, Edu, Enterprise. API-ключ — опция «requires additional setup».
- **Базовый URL при ChatGPT-аутентификации — `https://chatgpt.com/backend-api/codex`** (константа `CHATGPT_CODEX_BASE_URL`), а не `api.openai.com`. При API-key аутентификации — `https://api.openai.com/v1`.
- **Wire API — только Responses API** (`/responses`): `wire_api = "chat"` **удалён** из CLI (ошибка с отсылкой к обсуждению #7782). Это касается и встроенных Ollama/LMStudio провайдеров — экосистема сходится на Responses.
- HTTP-дефолты клиента: заголовки `originator: codex_cli_rs`, User-Agent `codex_cli_rs/<версия> (<ОС>)`, для built-in OpenAI — `version: <версия>`. JWT-клеймы `id_token` лежат под `https://api.openai.com/auth`: `chatgpt_plan_type` (free/plus/pro/business/enterprise/edu), `chatgpt_user_id`, `chatgpt_account_id`, `chatgpt_account_is_fedramp`.
- **Хранение:** `~/.codex/auth.json` = `AuthDotJson { auth_mode, openai_api_key?, tokens: TokenData, last_refresh, … }`, где `TokenData { id_token (JWT), access_token (JWT, короткоживущий), refresh_token, oauth_provenance {issuer, client_id}, account_id }`.
- **OAuth browser-flow:** issuer `https://auth.openai.com`; loopback-сервер на порту **1455** (fallback 1457), redirect `http://127.0.0.1:{port}/auth/callback`; authorize `{issuer}/oauth/authorize`; scope `openid profile email offline_access api.connectors.read api.connectors.invoke`; extra-параметры `id_token_add_organizations=true`, `codex_cli_simplified_flow=true`, `originator=codex_cli_rs`; обмен кода — `POST {issuer}/oauth/token` (form-encoded, PKCE).
- **Device-code flow** (есть в коде, `login/src/device_code_auth.rs`):
  1. `POST {issuer}/api/accounts/deviceauth/usercode` с `{client_id}` → `{device_auth_id, user_code, interval}`.
  2. Пользователь открывает `{issuer}/codex/device` и вводит код (живёт 15 минут).
  3. Клиент опрашивает `POST {issuer}/api/accounts/deviceauth/token` с `{device_auth_id, user_code}` каждые `interval` сек (403/404 = «ещё не подтвердили», таймаут 15 минут) → `{authorization_code, code_challenge, code_verifier}`.
  4. Обычный PKCE-обмен кода на токены через `POST {issuer}/oauth/token` (redirect `{issuer}/deviceauth/callback`).
  - Сервер может ответить 404 на usercode — в CLI это «device code login is not enabled», флоу не гарантированно включён у всех.
- Клиент собирается с **Cloudflare cookie store** для chatgpt.com — бэкенд за Cloudflare.
- Также в коде есть: обмен `id_token` на «API-key style» токен (grant token-exchange, `requested_token=openai-api-key`), PAT-логин, Bedrock через OpenAI-совместимый Mantle endpoint, WebSocket-транспорт Responses (перспективно, для v1 игнорируем).

Не подтверждено из первоисточников (но общеизвестно в экосистеме интеграций): значение публичного `client_id` CLI, заголовок `chatgpt-account-id` на inference-запросах, `OpenAI-Beta: responses=experimental`. Перед реализацией — снять с актуального кода/трафика CLI.

### 2.3 Сводка

| | Claude (подписка) | Codex (подписка ChatGPT) |
|---|---|---|
| Токен | OAuth `sk-ant-oat01-…` | access (JWT) + refresh |
| Официальный путь наружу | **`claude setup-token`** → токен на 1 год | только внутри CLI; device-code есть в коде, но «может быть не включён» |
| Wire-протокол | Anthropic `/v1/messages` (compat chat/completions — под вопросом) | **только Responses API** |
| Endpoint | `api.anthropic.com` | `chatgpt.com/backend-api/codex` |
| Заголовки | `Authorization: Bearer` + `anthropic-beta: oauth-2025-04-20` | `Authorization: Bearer` + `chatgpt-account-id` + `originator`/UA |
| Обновление токена | не нужно (год) | обязательно: access короткий, refresh ротируется |
| Квоты | окна подписки, 429 | окна подписки (5h/week), 429 |

## 3. Варианты интеграции

### 3.1 Claude

**C1 — `claude setup-token` (рекомендуемый, стартовый).** Пользователь на своей машине выполняет `claude setup-token`, вставляет токен в temporality как `secret://claude-oat`; провайдер `base_url=https://api.anthropic.com`. Auth-механику писать не надо — токен ведёт себя как обычный ключ с годовым сроком. Изменения точечные:

- `kernel/llm/client.go`: для OAuth-провайдера слать `Bearer` (уже шлётся) + `anthropic-beta: oauth-2025-04-20`;
- `cmd/agent-kernel/providermodels.go`: каталог `/models` с OAuth-токеном — тоже Bearer вместо `x-api-key`;
- UI: пресет «Claude по подписке» в мастере провайдера с инструкцией из двух команд.

Открытый риск №1: **работает ли OAuth-токен на compat-слое `/v1/chat/completions`** (который сейчас использует клиент). Если нет — нужен переводчик chat-completions ↔ `/v1/messages` (средний, хорошо понятный lift; anthropic-форму usage клиент уже парсит).

**C2 — полный OAuth как у `/login`** (loopback :54545, refresh-цикл ~часы): имеет смысл только если нужен автологин без CLI. Третья сторона здесь заметно более «серая» по ToS и хрупче. Откладываем; setup-token закрывает потребность.

### 3.2 Codex

**O1 — импорт `~/.codex/auth.json` (быстрый промежуточный).** Пользователь логинится в CLI у себя, вставляет JSON (там refresh_token) → дальше temporality рефрешит сама. Убирает OAuth из первого релиза; фокус сразу на главной части — Responses-адаптере.

**O2 — device-code flow (целевой UX).** Схема в §2.2; не требует портов и достижимости kernel из браузера пользователя — правильно для docker-инсталляций. Fallback при 404 — рассказ про `codex login` + импорт (O1). Loopback-флоу (порт 1455) оставляем как опцию для локальных стендов, где kernel достижим на 127.0.0.1.

**O3 — мост через сам CLI** (`codex exec`, `claude -p`, app-server/MCP режимы) — **отклонён** для модельной сетки: это агентный уровень абстракции, tool calling остаётся внутри CLI, в контракт провайдера моделей не ложится.

### 3.3 Общая часть (обе подписки)

- **Хранение:** токены/refresh — в envelope-секретах (master key), в БД только ссылки и метаданные (`plan`, `account_id`, `expires_at`, `last_refresh`). В логи/диагностику — только `maskKey`.
- **Рефрешер (Codex):** проактивно по `exp` из access-JWT + реактивно на 401; singleflight на провайдера; при неудаче — деградация провайдера с внятным статусом в UI («подписка: re-login required»). Расширить `credentialSource`-диагностику oauth-состояниями.
- **UI статусов:** карточка провайдера показывает `chatgpt_plan_type` / срок токена / время последнего рефреша.
- **Каталог моделей:** у Codex бэкенда свой «Codex-native catalog» endpoint (упомянут в исходниках); для v1 проще захардкодить известные слаги и обновлять по расписанию. У Claude каталог `/models` уже умеется.
- **Квоты и стоимость:** подписка = окна и 429, не биллинг. `model_limits`/`max_concurrent_requests` + retry-ladder покрывают; в `kernel/cost` цены отсутствуют → cost=0 (проверить, что парсер цен tolerate).
- **Мульти-аккаунты:** несколько провайдеров (`claude-max-1/2`, `chatgpt-pro-1/2`) с разными подписками пулят квоты через существующую маршрутизацию по лейблам — бесплатная синергия с текущей архитектурой.

## 4. Риски

1. **Cloudflare перед `chatgpt.com/backend-api`.** CLI неспроста держит cookie store; с датацентровых IP вероятны challenge. С локальных docker-стендов (основной quickstart-сценарий) — обычно ок. Операционный риск, кодом заранее не закрывается.
2. **Хрупкость/ToS.** Подписочные эндпоинты Codex — не публичный API, менялись без уведомлений. Anthropic-путь сильно устойчивее (`setup-token` официальный). Честная позиция: подписка = best-effort, API-ключ = SLA-путь, держать рядом как fallback.
3. **Reasoning-items в Responses.** При continuation tool-calls их надо возвращать в `input` — иначе codex-модели ошибаются. Самая коварная часть адаптера.
4. **Устаревание фактов.** Слаги моделей, beta-заголовки, клиентские ID — зафиксировать в одном месте конфигом, не раскидывать по коду.

## 5. Порядок работ

1. **Фаза 0 (спайка, полдня):** вручную проверить два факта — OAuth-токен на `api.anthropic.com/v1/chat/completions` (compat) и на `/v1/messages`; трафик `codex` CLI к `chatgpt.com/backend-api/codex/responses` (заголовки, формат).
2. **Фаза 1 (Claude, дни):** C1 — beta-заголовок, Bearer в каталоге, пресет онбординга.
3. **Фаза 2 (Codex импорт + Responses-адаптер):** главная инженерная часть; wire-тип провайдера как явный атрибут (`chat_completions | anthropic_messages | openai_responses`).
4. **Фаза 3 (Codex device-code):** полный OAuth-UX без ручного импорта.
5. **Фаза 4 (полировка):** рефрешер-демон, статусы подписок в UI, oauth-диагностика, docs.

## 6. Открытые вопросы (для обсуждения)

1. Compat-слой Anthropic + OAuth: работает? Если нет — делать ли messages-адаптер в фазе 1 или отложить Claude-подписку до него?
2. Responses-адаптер: писать полноценный второй wire в `kernel/llm` или ограничиться подмножеством (без reasoning-summary, web_search и пр.) для v1?
3. Device-code у OpenAI: насколько стабильно включён для плюс/pro-аккаунтов? Есть ли смысл закладывать loopback-флоу параллельно сразу?
4. Хранение refresh-токенов: достаточно ли envelope-секретов или нужен отдельный контур ротации/отзыва?
5. Политика по ToS-серой зоне Codex: включать ли фичу по умолчанию или под experimental-флагом с дисклеймером?
6. Нужна ли поддержка нескольких подписок одного вендора как first-class (пул квот) уже в v1, или это бесплатно получится само?
