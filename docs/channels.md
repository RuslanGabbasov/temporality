# Каналы коммуникации: единая абстракция (Channel Drivers)

## 1. Цель

Одна точка расширения для каналов. Matrix, Telegram, Slack, Mattermost,
Teams, вебхуки — любые новые каналы добавляются реализацией драйвера, без
правок в ядре доставки, inbound-конвейере, настройках, UI и модели данных.

Принцип: **ядро не знает каналов, драйверы не знают продукта.** Всё, что
канало-специфично (протокол, payload, идемпотентность, причуды API) — внутри
драйвера. Всё общее (маршрутизация, получатели, идентификация, события,
сущности, UI) — в ядре, написано один раз.

## 2. Проблема сегодняшнего кода

Добавление канала сегодня — правки восьми несвязанных мест:

| Место | Что правится |
|---|---|
| `kernel/agent/channels.go` | поля `TransportSettings`, `TransportSettingsFromEnv`, `OverlayEnv`, `Configured`, запись в `channelRegistry` |
| `cmd/agent-kernel/main.go` | DB-оверлей транспортиров (типизированные поля), админ-API чтения/записи, env в docker-compose |
| `kernel/agent/notify.go` | ветка в `sendToChannel`, свой sender |
| `kernel/agent/sendfile.go` | ветка в switch файловой доставки |
| `cmd/agent-kernel/matrixinbound.go` | копия inbound-лупа под протокол нового канала |
| ядро матчинга | матчинг ответов зашит под матричные комнаты |
| `debugger/src/*` | формы настроек канала, выбор канала у пользователя, локали |
| docker-compose / README | переменные окружения |

Модель доставки тоже зашита: matrix — long-poll `/sync`, telegram — нечто
другое (getUpdates), Slack/Mattermost/Teams — push через webhook. Код
предполагает поллинг и не умеет push вообще.

## 3. Контракт драйвера

```go
// ChannelDriver — единственная точка расширения. Реализация инкапсулирует
// протокол; ядро зовёт только этот контракт.
type ChannelDriver interface {
    // Метаданные
    Type() string            // "matrix", "telegram", "slack", "mattermost", "teams", "webhook"
    Spec() ChannelSpec       // label, address hint, capabilities, схема настроек

    // Настройки
    // Драйвер получает generic map (JSONB из workspace + env fallback) и сам
    // валидирует свои поля. Ядро не знает имён полей.
    Configure(settings map[string]string) error
    IsConfigured() bool

    // Исходящая доставка
    SendText(ctx context.Context, to ChannelAddress, msg OutboundText) error
    SendFile(ctx context.Context, to ChannelAddress, file OutboundFile) error // ErrUnsupported по capabilities

    // Входящие — драйвер реализует ровно одну модель:
    Poller() ChannelPoller    // pull: matrix /sync, telegram getUpdates
    Webhook() ChannelWebhook  // push: slack events, teams, mattermost

    // Идентичность отправителя в этом канале (mxid, chat id, member id…)
    SenderIdentity(user UserChannel) string
}

// ChannelSpec описывает канал для ядра и UI.
type ChannelSpec struct {
    Type         string
    Label        string
    AddressHint  string
    Capabilities Capabilities
    Settings     []SettingField // name, label, secret — UI рендерит generic
}

type Capabilities struct {
    Text       bool // все
    TwoWay     bool // ответ в канале маршрутизируется обратно (подсказка «reply here»)
    FilesOut   bool // отправка файлов агентом
    FilesIn    bool // приём вложений от пользователя
    Threads    bool // ответ должен попасть в тред вопроса (slack/teams/mattermost)
    Interactive bool // кнопки для options (future; MVP рендерит текстом)
    MaxTextLen int  // 0 = без лимита (telegram 4096)
}

// Pull-модель: курсорное долгое ожидание.
type ChannelPoller interface {
    // Poll блокируется до появления событий или таймаута; cursor — opaque
    // строка, ядро персистит её между циклами.
    Poll(ctx context.Context, cursor string) (events []InboundMessage, next string, err error)
    // VerifySelf опциональная самопроверка (matrix whoami — не матчить себя).
    VerifySelf(ctx context.Context) error
}

// Push-модель: канал сам стучится HTTP.
type ChannelWebhook interface {
    Route() string                              // "/v1/channels/slack/events"
    VerifyRequest(r *http.Request, body []byte) error  // подписи/токены канала
    Parse(r *http.Request, body []byte) []InboundMessage
}
```

## 4. Нормализованная модель сообщений

Всё канало-специфичное остаётся в `refs`, ядро работает с общими полями:

```go
type InboundMessage struct {
    ID        string          // канальный идентификатор (matrix event_id, tg update_id) — дедуп
    Channel   string          // тип драйвера
    ChatID    string          // комната/чат — сопоставляется с адресом канала пользователя
    Sender    string          // канальная идентичность автора (mxid, member id, tg user id)
    Text      string
    Files     []InboundFile   // {Ref, Name}; Ref — opaque для ядра (mxc://, file_id, URL)
    Timestamp time.Time
    ReplyTo   string          // если канал несёт ссылку на сообщение-вопрос (треды, reply_to)
}

// Ядро: «дай мне байты вложения» — скачивание инкапсулировано в драйвере
// (matrix — авторизованный download mxc://, telegram — getFile, slack — URL).
FetchFile(ctx context.Context, file InboundFile) ([]byte, error)
```

## 5. Общий inbound-конвейер

Сегодняшний `matrixInboundLoop` — частный случай. Общий конвейер (один на
инсталляцию, по потоку на driver):

```text
[Poller | Webhook] → InboundMessage
  → дедуп по (channel, ID)          -- workspace_inbound_message, UNIQUE
  → идентификация отправителя        -- закрытый мир: Sender == SenderIdentity(user)
  → матчинг ответов                   -- открытые human requests с адресом этого чата
  → иначе intake                      -- docs/inbound-messages.md
```

Матчинг ответов (`MatchMatrixReplies`) обобщается без канальной логики:
«сообщение в чате X отвечает старейшему открытому вопросу, доставленному в
чат X» — правило про адреса и время, не про матрицу. Отдельно живёт
`workspace_channel_cursor (channel, cursor, updated_at)` — телеграмный
`offset` обязан переживать рестарт, матричный `since` — желательно.

Webhook-драйверы регистрируют свой route в общем mux; после Verify+Parse
сообщение попадает в тот же конвейер.

## 6. Настройки: схема вместо типизированной структуры

`TransportSettings` с именованными полями заменяется generic-хранилищем:

* workspace: `channel_transport` → payload JSONB `{"matrix": {"homeserver": …,
  "access_token": …}, "telegram": {"bot_token": …}}` (миграция колонок в
  JSONB один раз, дальше новые каналы без миграций);
* env fallback: `KERNEL_CHANNEL__<TYPE>__<FIELD>`; легаси `KERNEL_MATRIX_*` /
  `KERNEL_TELEGRAM_*` читаются как алиасы;
* `Configured` и «чего не хватает» (NotConfiguredHint) вычисляет драйвер по
  своей схеме — UI рендерит форму настроек из `Settings`, а не из свича.

Секреты по-прежнему не возвращаются API в открытом виде (`SecretHint`).

## 7. Что не меняется

* Разрешение получателя (`ResolveRecipient`), выбор канала (`pickChannel`),
  правила доставки — канало-нейтральны уже сейчас.
* `human_request` как сущность, маршрутизация ответов через Approval-signal,
  таймауты с политиками.
* Отправка из активностей Temporal (25-секундный таймаут, доставка не роняет
  ран) — драйверы обязаны в него укладываться.
* «Web» как безусловный фолбэк: канал не сконфигурирован/упал → web inbox.

## 8. Чеклист нового канала

После рефакторинга добавление Mattermost = один файл-драйвер + одна строка
регистрации. Бесплатно получаете: доставку вопросов, send_file, inbound с
дедупом и идентификацией, матчинг ответов, intake, настройки в UI, фолбэк на
web, события, тестовую обвязку.

Обязанности драйвера: протокол и auth, рендер текста под лимиты/разметку
канала, идемпотентность отправки (matrix txnID — уже так), FetchFile,
VerifyRequest (для push), курсор (для pull).

## 9. Особенности известных каналов

| Канал | Модель | Identity | Специфика |
|---|---|---|---|
| matrix | pull `/sync` | mxid | whoami для пропуска себя; mxc:// download; txn idempotency |
| telegram | pull `getUpdates` | chat id (личка) = адрес | offset-курсор персистить; лимит 4096; getFile |
| slack (webhook) | только outbound | — | текущее поведение, остаётся отдельным типом |
| slack (bot) | push Events API | member id | треды (ReplyTo=ts), подпись запроса, blocks (future) |
| mattermost | push webhook | user id | self-hosted: URL сервера в настройках драйвера |
| teams | push Bot Framework | aad id | верификация Microsoft, самый тяжёлый onboard |
| webhook (generic) | только outbound | — | escape hatch: envelope + HMAC-подпись, ответы в UI |

## 10. Безопасность

* Push-драйверы обязаны верифицировать запрос (подпись/секрет) —
  `VerifyRequest` не опционален.
* Закрытый мир идентичности (inbound-messages.md §5): инициативное сообщение
  принимается только при совпадении `Sender` с зарегистрированной
  идентичностью владельца канала. Поле `sender_id` появляется в канале
  пользователя, заполняется по подсказке драйвера.
* Исходящие секреты (токены ботов) — только в настройках/секрет-сторе, в
  события и логи не попадают.

## 11. Фазы

### Фаза 0 — выделение слоя (без изменения поведения)

* Интерфейс + реестр драйверов; matrix/telegram/slack-webhook/generic-webhook
  переезжают в драйверы; `sendToChannel` и sendfile-switch → dispatch по
  реестру.
* Настройки: JSONB payload + env-алиасы, generic админ-форма из схемы.
* `matrixInboundLoop` → общий poll-луп + matrix-драйвер; общий конвейер с
  дедупом; матчинг ответов канализируется.
* Курсоры в БД.

### Фаза 1 — intake поверх слоя

`docs/inbound-messages.md` фаза 1 строится на общем конвейере (matrix —
первый драйвер с TwoWay).

### Фаза 2 — новые inbound-каналы

Telegram inbound (pull, персистентный offset), Mattermost (push), Slack-bot
(push, треды) — по мере надобности, каждый = драйвер.

### Фаза 3 — интерактивность

Options как кнопки там, где capabilities.Interactive (telegram inline
keyboard, slack blocks); везде остаётся текстовый фолбэк.

## 12. Критерии готовности

1. Добавление тестового fake-канала (driver + регистрация, ~100 строк) даёт:
   доставку вопросов, send_file, inbound с дедупом, идентификацию, фолбэк на
   web — без единой правки вне файла драйвера.
2. Регресс существующих каналов: матричные вопросы/ответы/файлы, телеграмная
   доставка, слак-webhook и generic-webhook ведут себя как до рефакторинга
   (золото: существующие тесты notify/sendfile/matrixinbound без правок
   ассертов).
3. Настройки канала, заданные через env и через UI, эквивалентны.
4. Рестарт кернела не теряет курсоры и не порождает дублей сообщений.

## 13. Открытые вопросы

* Разметка исходящих: markdown у нас, у каналов HTML (matrix custom
  org.matrix.custom.html, telegram HTML/MarkdownV2) — нужен renderer per
  driver; MVP остаётся plain text.
* Несколько ботов одного типа (два матрикс-аккаунта для разных юнитов) —
  настройки станут списком инстансов драйвера; пока один на тип.
* Rate limits и retry-политики отправки по каналам (у телеграма жёсткие) —
  общий бюджет попыток в ядре, паузы в драйвере.
* Voice/stickers: игнорируем или транскрибируем — вне скоупа, фиксируем в
  capabilities как отсутствие.
