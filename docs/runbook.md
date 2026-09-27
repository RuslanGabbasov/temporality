# Runbook — Temporality

Операционное руководство для администратора Temporality-стека.

---

## 1. Архитектура развёртывания

```
┌─────────────────────────────────────────────────────┐
│  docker compose                                     │
│                                                     │
│  ┌────────────┐  ┌────────────┐  ┌──────────────┐  │
│  │  postgres   │  │  journal   │  │ agent-kernel │  │
│  │  :5432      │  │  :8080     │  │  :8090       │  │
│  └────────────┘  └────────────┘  └──────────────┘  │
│                                                     │
│  ┌────────────┐  ┌────────────┐                     │
│  │  debugger   │  │  temporal  │                     │
│  │  :3000      │  │  :7233/:8233                    │
│  └────────────┘  └────────────┘                     │
└─────────────────────────────────────────────────────┘
```

Сервисы: postgres (данные), journal (события), agent-kernel (workflow),
debugger (UI), temporal (durable execution).

---

## 2. Backup / Restore PostgreSQL

### Backup

```sh
# Пока контейнер работает
docker compose exec postgres pg_dump -U temporality temporality \
  | gzip > backup-$(date +%Y%m%d-%H%M%S).sql.gz

# Проверить целостность
gunzip -t backup-*.sql.gz && echo "backup OK"
```

### Restore

```sh
# Остановить всё кроме postgres
docker compose stop journal agent-kernel debugger

# Восстановить
gunzip -c backup-YYYYMMDD-HHMMSS.sql.gz \
  | docker compose exec -T postgres psql -U temporality temporality

# Перезапустить
docker compose up -d
```

### Автоматизация

Добавить в crontab хоста:

```
0 3 * * * cd /path/to/temporality && docker compose exec -T postgres pg_dump -U temporality temporality | gzip > /backups/temporality-$(date +\%Y\%m\%d).sql.gz && find /backups -name '*.sql.gz' -mtime +30 -delete
```

### Проверка backup

Раз в месяц восстановить backup в тестовую БД и проверить:

```sh
gunzip -c backup.sql.gz | docker compose exec -T postgres psql -U temporality test_restore
# Проверить наличие данных
docker compose exec -T postgres psql -U temporality test_restore -c \
  "SELECT count(*) FROM observation_events; SELECT count(*) FROM kernel_event_outbox;"
docker compose exec -T postgres psql -U temporality -c "DROP DATABASE test_restore;"
```

---

## 3. Migration / Upgrade

### Порядок обновления

1. **Backup** (см. §2).
2. `git pull` — получить новую версию.
3. `docker compose build` — собрать образы.
4. `docker compose up -d` — перезапустить. Миграции применяются
   автоматически при старте journal и kernel.
5. Проверить health (см. §5).

### Ручные миграции

Миграции journal: `migrations/000018_observation_events.up.sql` и выше.
Миграции kernel outbox: `migrations/000019_kernel_event_outbox.up.sql`.
Миграции квот: `migrations/000020_kernel_run_quota.up.sql`.

Применяются автоматически при старте. Для ручного применения:

```sh
docker compose exec postgres psql -U temporality temporality \
  -f /path/to/migration.up.sql
```

### Откат миграции

```sh
docker compose stop journal agent-kernel
docker compose exec postgres psql -U temporality temporality \
  -f migrations/NNNNNN_*.down.sql
docker compose up -d
```

---

## 4. Token Rotation

### Текущие токены

- `JOURNAL_AUTH_TOKENS` — токены journal API (reader/writer/operator)
- `KERNEL_AUTH_TOKENS` — токены kernel API (reader/writer/operator)
- `TEMPORALITY_API_TOKEN` — токен kernel → journal (writer)

### Добавление нового токена (без простоя)

```sh
# 1. Сгенерировать новый токен
NEW_TOKEN=$(openssl rand -hex 32)

# 2. Добавить в .env (не удаляя старый!)
echo "NEW_USER_TOKEN=${NEW_TOKEN}:newuser:reader:project-a" >> .env

# 3. Обновить JOURNAL_AUTH_TOKENS / KERNEL_AUTH_TOKENS в .env
#    Формат: token:subject:role:projects;token2:...
#    Добавить новый токен через ; к существующим

# 4. Перезапустить (старые токены продолжают работать)
docker compose restart journal agent-kernel

# 5. Проверить новый токен
curl -H "Authorization: Bearer $NEW_TOKEN" http://localhost:8080/healthz

# 6. Раздать новый токен пользователю
```

### Удаление токена (с пропаданием доступа)

```sh
# 1. Удалить токен из JOURNAL_AUTH_TOKENS / KERNEL_AUTH_TOKENS в .env
# 2. Перезапустить
docker compose restart journal agent-kernel
```

### Ротация TEMPORALITY_API_TOKEN

```sh
# 1. Сгенерировать новый
NEW_API_TOKEN=$(openssl rand -hex 32)

# 2. Добавить как writer в JOURNAL_AUTH_TOKENS
# 3. Обновить TEMPORALITY_API_TOKEN в .env
# 4. Перезапустить оба сервиса
docker compose restart journal agent-kernel
```

---

## 5. Health Monitoring

### Эндпоинты

| Сервис | URL | Проверяет |
|--------|-----|-----------|
| Journal | `GET http://localhost:8080/healthz` | DB connection |
| Kernel | `GET http://localhost:8090/healthz` | Liveness |
| Temporal UI | `http://localhost:8233` | Web UI |

### Проверка состояния

```sh
# Все сервисы запущены?
docker compose ps

# Journal health
curl -s http://localhost:8080/healthz

# Kernel health
curl -s http://localhost:8090/healthz

# Temporal UI доступен?
curl -s -o /dev/null -w '%{http_code}' http://localhost:8233
```

### Автоматический мониторинг

Docker compose healthcheck уже настроен для postgres, journal, kernel.
Можно добавить внешний мониторинг через cron:

```sh
*/5 * * * * curl -sf http://localhost:8080/healthz || systemctl restart temporality-journal
*/5 * * * * curl -sf http://localhost:8090/healthz || systemctl restart temporality-agent-kernel
```

---

## 6. Outbox Monitoring

Outbox — очередь событий kernel → journal. Если outbox растёт, значит
journal недоступен или не принимает события.

### Проверка состояния

```sh
# Требует operator-токен
curl -s 'http://localhost:8090/v1/agent/outbox' \
  -H "Authorization: Bearer $OPERATOR_TOKEN"
```

Ответ:
```json
{
  "pending": 0,
  "oldest_pending_age_seconds": 0,
  "delivered": 1542,
  "last_error": ""
}
```

### Тревога

- `pending > 0` дольше 5 минут — journal недоступен.
- `last_error` не пустой — проблема с доставкой.

### Действия

```sh
# Проверить journal
curl -s http://localhost:8080/healthz

# Проверить логи
docker compose logs journal --tail=20

# Если journal упал — перезапустить
docker compose restart journal
```

---

## 7. Quota Monitoring

### Проверка использования

```sh
curl -s 'http://localhost:8090/v1/agent/quotas?project=PROJECT' \
  -H "Authorization: Bearer $READER_TOKEN"
```

Ответ:
```json
{
  "project": "forge",
  "used": 12,
  "limit": 50,
  "reset_at": "2026-09-28T00:00:00Z"
}
```

### Настройка квот

В `.env`:

```
KERNEL_RUN_QUOTAS=*:50,forge:200,lighthouse:100
```

- `*:50` — дефолт 50 запусков/день на проект
- `forge:200` — конкретный проект
- Пустая строка = без ограничений

После изменения — `docker compose restart agent-kernel`.

### Превышение квоты

Агент получает HTTP 429 с `Retry-After`. Оператор видит:

```json
{
  "error": "daily run quota exceeded for project forge",
  "quota": {
    "allowed": false,
    "limit": 50,
    "used": 50,
    "reset_at": "2026-09-28T00:00:00Z"
  }
}
```

---

## 8. Типовые аварии

### Kernel не стартует (MCP connection error)

**Симптом:** `connect MCP server: ... EOF` в логах.

**Причина:** KERNEL_MCP_COMMAND указывает на бинарник, который не
запускается в контейнере.

**Решение:** Отключить MCP (`KERNEL_MCP_COMMAND=` в `.env`) или
проверить бинарник:

```sh
docker compose exec -T agent-kernel /usr/local/bin/test-mcp
# Должен hang (ждёт stdin), не паниковать
```

### Outbox растёт

**Симптом:** `pending` увеличивается, `oldest_pending_age_seconds` растёт.

**Причина:** Journal недоступен.

**Решение:**

```sh
docker compose logs journal --tail=20
docker compose restart journal
```

### Temporal workflow завис

**Симптом:** Run показывает `Running` но не завершается.

**Проверка:**

```sh
# Temporal UI
open http://localhost:8233

# Или через kernel
curl -s "http://localhost:8090/v1/agent/runs/RUN_ID?project=PROJECT" \
  -H "Authorization: Bearer $READER_TOKEN"
```

**Действия:**
- Проверить логи kernel: `docker compose logs agent-kernel --tail=50`
- Если workflow завис в approval — отправить approval через UI
- Если workflow завис в tool — проверить sandbox (docker ps, логи)

### Нехватка памяти (PostgreSQL)

**Симптомы:** OOM killer, postgres restarts.

**Решение:**

```sh
# Проверить использование
docker stats postgres

# Очистить старые outbox-записи (доставленные >30 дней назад)
docker compose exec postgres psql -U temporality temporality -c \
  "DELETE FROM kernel_event_outbox WHERE delivered_at < now() - interval '30 days';"
```

### Конфликт workflow ID

**Симптом:** `409 Conflict` при запуске run.

**Причина:** Повторный `run_id` для того же проекта.

**Решение:** Использовать уникальный `run_id` (timestamp, UUID).

---

## 9. Secrets

### Через файлы (docker secrets / 12-factor _FILE)

Kernel и journal поддерживают конвенцию `<VAR>_FILE`:

| Переменная | `_FILE` аналог |
|-----------|---------------|
| `DATABASE_URL` | `DATABASE_URL_FILE` |
| `TEMPORALITY_MODEL_API_KEY` | `TEMPORALITY_MODEL_API_KEY_FILE` |
| `TEMPORALITY_API_TOKEN` | `TEMPORALITY_API_TOKEN_FILE` |
| `KERNEL_AUTH_TOKENS` | `KERNEL_AUTH_TOKENS_FILE` |
| `JOURNAL_AUTH_TOKENS` | `JOURNAL_AUTH_TOKENS_FILE` |

Если `VAR` не задан, а `VAR_FILE` указывает на файл — значение берётся
из файла (trimmed). Прямое значение всегда побеждает.

### Пример docker-compose secrets

```yaml
secrets:
  model_api_key:
    file: ./secrets/model_api_key.txt

services:
  agent-kernel:
    secrets:
      - source: model_api_key
        target: temporality_model_api_key
    environment:
      TEMPORALITY_MODEL_API_KEY_FILE: /run/secrets/temporality_model_api_key
```

---

## 10. Полезные команды

```sh
# Логи в реальном времени
docker compose logs -f agent-kernel

# Пересобрать и перезапустить один сервис
docker compose up -d --build agent-kernel

# Запустить shell в контейнере
docker compose exec agent-kernel sh

# Проверить версию Go в контейнере
docker compose exec agent-kernel go version  # только build stage

# Полный перезапуск
docker compose down && docker compose up -d

# Очистить неиспользуемые образы
docker system prune
```