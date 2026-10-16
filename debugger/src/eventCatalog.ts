import type { Locale } from './locales'

/**
 * Localized catalog of journal event types the kernel actually emits
 * (cmd/agent-kernel, kernel/agent, observation). It feeds the event-trigger
 * constructor: users pick an event by its human name instead of typing raw
 * dotted types they have no way to know. The catalog is a curated reference,
 * not a closed enum — triggers on types added later still work via the
 * "custom type" escape hatch.
 *
 * Every type listed here was verified to be emitted by non-test Go code.
 * model.text_delta / model.reasoning are deliberately absent: they are
 * high-frequency streaming events, unusable as a trigger source.
 */

export interface EventGroupMeta { id: string; ru: string; en: string }
export interface EventMeta {
  type: string
  group: string
  ru: { title: string; desc: string }
  en: { title: string; desc: string }
}

export const EVENT_GROUPS: EventGroupMeta[] = [
  { id: 'runs', ru: 'Запуски и ходы', en: 'Runs and turns' },
  { id: 'model', ru: 'Модель', en: 'Model' },
  { id: 'tools', ru: 'Инструменты', en: 'Tools' },
  { id: 'delegation', ru: 'Делегирование и планы', en: 'Delegation and plans' },
  { id: 'teams', ru: 'Команды', en: 'Teams' },
  { id: 'approvals', ru: 'Согласования с человеком', en: 'Human approvals' },
  { id: 'knowledge', ru: 'Знания и подсказки', en: 'Knowledge and hints' },
  { id: 'artifacts', ru: 'Артефакты и ревью', en: 'Artifacts and reviews' },
  { id: 'secrets', ru: 'Секреты', en: 'Secrets' },
  { id: 'mcp', ru: 'MCP', en: 'MCP' },
  { id: 'triggers', ru: 'Триггеры и вебхуки', en: 'Triggers and webhooks' },
  { id: 'intake', ru: 'Входящие сообщения', en: 'Intake' },
  { id: 'quality', ru: 'Качество и эволюция агентов', en: 'Quality and evolution' },
  { id: 'sagas', ru: 'Саги', en: 'Sagas' },
  { id: 'ops', ru: 'Эксплуатация', en: 'Operations' },
]

const E = (group: string, type: string, ruTitle: string, ruDesc: string, enTitle: string, enDesc: string): EventMeta => ({
  type, group, ru: { title: ruTitle, desc: ruDesc }, en: { title: enTitle, desc: enDesc },
})

export const EVENT_CATALOG: EventMeta[] = [
  // ── runs ──
  E('runs', 'run.started', 'Запуск начат', 'Агентный ран стартовал (из чата, триггера, делегирования или команды).', 'Run started', 'An agent run started (from chat, trigger, delegation or team).'),
  E('runs', 'run.completed', 'Запуск завершён', 'Ран успешно завершился; в данных — ходы, токены, стоимость.', 'Run completed', 'The run finished successfully; data carries turns, tokens and cost.'),
  E('runs', 'run.failed', 'Запуск упал', 'Ран завершился ошибкой (модель, инструмент, политика или лимит).', 'Run failed', 'The run ended with an error (model, tool, policy or budget limit).'),
  E('runs', 'run.cancelled', 'Запуск отменён', 'Ран отменён пользователем или системой.', 'Run cancelled', 'The run was cancelled by the user or the system.'),
  E('runs', 'turn.started', 'Ход начат', 'Агент начал очередной ход внутри рана.', 'Turn started', 'The agent started another turn within the run.'),
  E('runs', 'turn.completed', 'Ход завершён', 'Ход агента завершился.', 'Turn completed', 'The agent turn finished.'),
  E('runs', 'agent.summary', 'Итоговый ответ агента', 'Финальный текстовый ответ рана для пользователя.', 'Agent summary', "The run's final text answer for the user."),
  E('runs', 'run.user_note', 'Дополнение пользователя принято', 'Сообщение из чата, отправленное во время рана, встроено в контекст на ближайшей границе хода.', 'User note accepted', 'A chat message sent mid-run was appended to the context at the nearest turn boundary.'),
  E('runs', 'run.note_dropped', 'Дополнение не доставлено', 'Бюджет рана исчерпан до того, как заметка достигла модели; она зафиксирована в журнале.', 'Note not delivered', "The run's budget ran out before the note reached the model; it is journaled instead."),
  // ── model ──
  E('model', 'model.started', 'Вызов модели начат', 'Начался запрос к провайдеру модели.', 'Model call started', 'A provider request started.'),
  E('model', 'model.completed', 'Вызов модели завершён', 'Провайдер вернул ответ; в данных — токены и длительность.', 'Model call completed', 'The provider answered; data carries tokens and duration.'),
  E('model', 'model.failed', 'Вызов модели упал', 'Ошибка провайдера или таймаут стрима.', 'Model call failed', 'Provider error or stream timeout.'),
  E('model', 'model.attempt', 'Повторная попытка вызова', 'Ядро ретраит упавший вызов модели.', 'Model call retry', 'The kernel retries a failed model call.'),
  E('model', 'model.nudged', 'Зависший вызов перезапущен', 'Пользователь вручную «подтолкнул» зависший вызов модели.', 'Stuck call nudged', 'The user manually nudged a stuck model call.'),
  // ── tools ──
  E('tools', 'tool.started', 'Вызов инструмента начат', 'Агент вызвал инструмент (команда, файл, делегирование…).', 'Tool call started', 'The agent invoked a tool (command, file, delegation…).'),
  E('tools', 'tool.completed', 'Инструмент завершён', 'Вызов инструмента успешно завершился.', 'Tool call completed', 'A tool call finished successfully.'),
  E('tools', 'tool.failed', 'Инструмент упал', 'Вызов инструмента завершился ошибкой; побочный эффект может быть неопределён.', 'Tool call failed', 'A tool call errored; its side effect may be uncertain.'),
  E('tools', 'tool.blocked', 'Инструмент заблокирован', 'Политика или RBAC запретили вызов до исполнения.', 'Tool call blocked', 'Policy or RBAC refused the call before execution.'),
  // ── delegation ──
  E('delegation', 'delegation.started', 'Делегирование начато', 'Родительский ран запустил дочерний.', 'Delegation started', 'A parent run started a child run.'),
  E('delegation', 'delegation.completed', 'Делегирование завершено', 'Дочерний ран вернул результат родителю.', 'Delegation completed', 'The child run returned its result to the parent.'),
  E('delegation', 'delegation.failed', 'Делегирование упало', 'Дочерний ран завершился ошибкой.', 'Delegation failed', 'The child run failed.'),
  E('delegation', 'plan.started', 'План начат', 'Лид-агент приступил к исполнению плана задач.', 'Plan started', 'The lead agent began executing the task plan.'),
  E('delegation', 'plan.completed', 'План завершён', 'Исполнение плана закончено.', 'Plan completed', 'Plan execution finished.'),
  E('delegation', 'plan.task.started', 'Задача плана начата', 'Исполнитель приступил к задаче плана.', 'Plan task started', 'A worker started a plan task.'),
  E('delegation', 'plan.task.completed', 'Задача плана завершена', 'Задача плана выполнена.', 'Plan task completed', 'A plan task finished successfully.'),
  E('delegation', 'plan.task.failed', 'Задача плана упала', 'Задача плана завершилась ошибкой.', 'Plan task failed', 'A plan task errored.'),
  E('delegation', 'plan.task.rejected', 'Задача отправлена на доработку', 'Ревьюер отклонил результат задачи — назначена доработка.', 'Plan task rejected', 'The reviewer rejected the task result — rework assigned.'),
  E('delegation', 'plan.task.skipped', 'Задача пропущена', 'Задача исключена из исполнения.', 'Plan task skipped', 'The task was skipped.'),
  E('delegation', 'plan.task.invalidated', 'Задача инвалидирована', 'Задача потеряла актуальность (например, план изменился).', 'Plan task invalidated', 'The task became irrelevant (e.g. the plan changed).'),
  E('delegation', 'plan.task.reopened', 'Задача открыта заново', 'Завершённая задача открыта повторно.', 'Plan task reopened', 'A finished task was reopened.'),
  E('delegation', 'plan.task.incomplete', 'Задача не завершена', 'Исполнитель не успел завершить задачу (например, упёрся в лимит шагов).', 'Plan task incomplete', 'The worker did not finish the task (e.g. hit the step limit).'),
  // ── teams ──
  E('teams', 'team.created', 'Команда создана', 'Определена новая команда агентов.', 'Team created', 'A new agent team was defined.'),
  E('teams', 'team.updated', 'Команда изменена', 'Определение команды обновлено.', 'Team updated', 'The team definition changed.'),
  E('teams', 'team.deleted', 'Команда удалена', 'Определение команды удалено.', 'Team deleted', 'The team definition was deleted.'),
  E('teams', 'team.started', 'Командный запуск начат', 'Команда приступила к работе над целью.', 'Team run started', 'The team started working on its goal.'),
  E('teams', 'team.run_started', 'Запуск команды стартовал', 'Начало командного рана (синоним team.started для дерева ранов).', 'Team run launched', 'A team run launched (tree view companion of team.started).'),
  E('teams', 'team.completed', 'Команда завершила работу', 'Командный ран успешен; в данных — токены, стоимость, доработки.', 'Team completed', 'The team run succeeded; data carries tokens, cost and rework.'),
  E('teams', 'team.failed', 'Команда упала', 'Командный ран завершился ошибкой.', 'Team failed', 'The team run failed.'),
  // ── approvals ──
  E('approvals', 'human.requested', 'Запрошено подтверждение', 'Агент задал вопрос человеку (ask human) и ждёт ответа.', 'Approval requested', 'The agent asked a human a question and waits for the answer.'),
  E('approvals', 'human.answered', 'Ответ получен', 'Человек ответил на вопрос агента.', 'Question answered', 'A human answered the agent question.'),
  E('approvals', 'human.rejected', 'Ответ: отказ', 'Человек отклонил предложенное действие.', 'Answer: rejected', 'A human declined the proposed action.'),
  E('approvals', 'human.reasked', 'Повторный вопрос', 'Агент переспросил, уточнив формулировку.', 'Question re-asked', 'The agent re-asked with a refined question.'),
  E('approvals', 'human.cancelled', 'Вопрос отменён', 'Вопрос человеку отменён (например, ран остановлен).', 'Question cancelled', 'The human question was cancelled (e.g. run stopped).'),
  E('approvals', 'human.timed_out', 'Ожидание истекло', 'Ответ не пришёл за отведённое время.', 'Approval timed out', 'No answer arrived within the allotted time.'),
  E('approvals', 'hitl.verdict.recorded', 'Вердикт согласования записан', 'Зафиксировано решение валидируемой операции.', 'HITL verdict recorded', 'A decision on a validated operation was recorded.'),
  // ── knowledge ──
  E('knowledge', 'knowledge.proposed', 'Знание предложено', 'Из рана извлечено знание-кандидат.', 'Knowledge proposed', 'A knowledge candidate was extracted from a run.'),
  E('knowledge', 'knowledge.confirmed', 'Знание подтверждено', 'Человек подтвердил знание.', 'Knowledge confirmed', 'A human confirmed the knowledge.'),
  E('knowledge', 'knowledge.challenged', 'Знание оспорено', 'Знание поставлено под сомнение.', 'Knowledge challenged', 'The knowledge was called into question.'),
  E('knowledge', 'knowledge.corrected', 'Знание исправлено', 'Формулировка знания скорректирована.', 'Knowledge corrected', 'The knowledge proposition was corrected.'),
  E('knowledge', 'knowledge.disproved', 'Знание опровергнуто', 'Знание опровергнуто практикой.', 'Knowledge disproved', 'The knowledge was disproved in practice.'),
  E('knowledge', 'knowledge.invalidated', 'Знание инвалидировано', 'Знание выведено из оборота.', 'Knowledge invalidated', 'The knowledge was retired from use.'),
  E('knowledge', 'knowledge.superseded', 'Знание заменено', 'Знание заменено более актуальным.', 'Knowledge superseded', 'The knowledge was replaced by a fresher one.'),
  E('knowledge', 'knowledge.linked', 'Знание связано', 'Знание привязано к скиллу или артефакту.', 'Knowledge linked', 'The knowledge was linked to a skill or artifact.'),
  E('knowledge', 'knowledge.promoted', 'Знание повышено до навыка', 'Подтверждённое знание стало черновиком навыка.', 'Knowledge promoted to skill', 'Confirmed knowledge became a skill draft.'),
  E('knowledge', 'knowledge.recalled', 'Знание отозвано', 'Знание попало в контекст рана как подсказка.', 'Knowledge recalled', 'The knowledge entered a run context as a hint.'),
  E('knowledge', 'knowledge.used', 'Знание использовано', 'Агент применил знание в работе.', 'Knowledge used', 'The agent applied the knowledge.'),
  E('knowledge', 'knowledge.extraction.started', 'Извлечение знаний начато', 'По завершённому рану запущена экстракция знаний.', 'Knowledge extraction started', 'Extraction over a finished run began.'),
  E('knowledge', 'knowledge.extraction.completed', 'Извлечение знаний завершено', 'Экстракция вернула кандидатов.', 'Knowledge extraction completed', 'Extraction returned candidates.'),
  E('knowledge', 'knowledge.extraction.failed', 'Извлечение знаний упало', 'Экстракция не удалась (не влияет на итог рана).', 'Knowledge extraction failed', 'Extraction failed (never affects the run result).'),
  E('knowledge', 'hint.query', 'Запрос подсказок', 'Ран запросил подсказки у журнала наблюдений.', 'Hint query', 'The run asked the observation journal for hints.'),
  E('knowledge', 'hint.offered', 'Подсказка предложена', 'Знание отобрано в подсказки рана.', 'Hint offered', 'A knowledge item was selected as a run hint.'),
  E('knowledge', 'hint.used', 'Подсказка использована', 'Агент применил предложенную подсказку.', 'Hint used', 'The agent applied the offered hint.'),
  E('knowledge', 'hint.ignored', 'Подсказка проигнорирована', 'Агент не стал применять подсказку.', 'Hint ignored', 'The agent skipped the offered hint.'),
  E('knowledge', 'hint.outcome', 'Итог подсказки', 'Итоговое влияние подсказки на ход рана.', 'Hint outcome', 'The final effect of a hint on the run.'),
  // ── artifacts ──
  E('artifacts', 'artifact.created', 'Артефакт создан', 'В реестре появился новый рабочий продукт.', 'Artifact created', 'A new work product appeared in the registry.'),
  E('artifacts', 'artifact.updated', 'Артефакт обновлён', 'Содержимое артефакта изменилось.', 'Artifact updated', 'The artifact content changed.'),
  E('artifacts', 'artifact.deleted', 'Артефакт удалён', 'Артефакт удалён из реестра.', 'Artifact deleted', 'The artifact was removed from the registry.'),
  E('artifacts', 'artifact.authored', 'Артефакт создан агентом', 'Агент выступил автором артефакта.', 'Artifact authored by agent', 'An agent authored the artifact.'),
  E('artifacts', 'artifact.read', 'Артефакт прочитан', 'Агент загрузил содержимое артефакта.', 'Artifact read', 'An agent loaded the artifact content.'),
  E('artifacts', 'artifact.proposed', 'Артефакт предложен', 'Появилась новая версия артефакта (неутверждённая).', 'Artifact proposed', 'A new (unapproved) artifact version appeared.'),
  E('artifacts', 'artifact.submitted', 'Артефакт отправлен на ревью', 'Версия ушла на согласование ревьюерам.', 'Artifact submitted for review', 'The version went to reviewers.'),
  E('artifacts', 'artifact.review.requested', 'Запрошено ревью артефакта', 'Открыт раунд согласования артефакта.', 'Artifact review requested', 'An artifact review round opened.'),
  E('artifacts', 'artifact.review.approved', 'Артефакт утверждён', 'Ревьюеры одобрили версию.', 'Artifact approved', 'Reviewers approved the version.'),
  E('artifacts', 'artifact.review.rejected', 'Артефакт отклонён', 'Ревьюеры отклонили версию.', 'Artifact rejected', 'Reviewers rejected the version.'),
  // ── secrets ──
  E('secrets', 'secret.created', 'Секрет создан', 'В хранилище зарегистрирован секрет.', 'Secret created', 'A secret was registered in the vault.'),
  E('secrets', 'secret.updated', 'Секрет обновлён', 'Метаданные секрета изменены.', 'Secret updated', 'Secret metadata changed.'),
  E('secrets', 'secret.deleted', 'Секрет удалён', 'Секрет удалён из хранилища.', 'Secret deleted', 'The secret was removed from the vault.'),
  E('secrets', 'secret.value_set', 'Значение секрета записано', 'Секрету записано новое значение.', 'Secret value set', 'A new value was written for the secret.'),
  E('secrets', 'secret.issued', 'Секрет выдан', 'Секрет выдан рану (аренда креденшела).', 'Secret issued', 'The secret was issued to a run (credential lease).'),
  E('secrets', 'secret.denied', 'Выдача секрета отклонена', 'Рану отказано в доступе к секрету.', 'Secret denied', 'A run was refused access to the secret.'),
  // ── mcp ──
  E('mcp', 'mcp.call.started', 'MCP-вызов начат', 'Начат вызов инструмента внешнего MCP-сервера.', 'MCP call started', 'An external MCP tool call started.'),
  E('mcp', 'mcp.call.completed', 'MCP-вызов завершён', 'MCP-инструмент вернул результат.', 'MCP call completed', 'The MCP tool returned a result.'),
  E('mcp', 'mcp.call.failed', 'MCP-вызов упал', 'Ошибка вызова MCP-сервера.', 'MCP call failed', 'The MCP server call errored.'),
  E('mcp', 'mcp.call.blocked', 'MCP-вызов заблокирован', 'Политика запретила вызов MCP-инструмента.', 'MCP call blocked', 'Policy refused the MCP tool call.'),
  // ── triggers ──
  E('triggers', 'trigger.created', 'Триггер создан', 'Настроен новый триггер.', 'Trigger created', 'A new trigger was configured.'),
  E('triggers', 'trigger.updated', 'Триггер изменён', 'Настройки триггера обновлены.', 'Trigger updated', 'Trigger settings changed.'),
  E('triggers', 'trigger.deleted', 'Триггер удалён', 'Триггер удалён.', 'Trigger deleted', 'The trigger was deleted.'),
  E('triggers', 'trigger.received', 'Доставка вебхука получена', 'Вебхук получил внешний вызов.', 'Webhook delivery received', 'The webhook received an external call.'),
  E('triggers', 'trigger.accepted', 'Вебхук принят', 'Доставка запустила ран.', 'Webhook accepted', 'The delivery started a run.'),
  E('triggers', 'trigger.rejected', 'Вебхук отклонён', 'Доставка отклонена (авторизация, дедупликация, валидация).', 'Webhook rejected', 'The delivery was refused (auth, dedup or validation).'),
  // ── intake ──
  E('intake', 'intake.received', 'Входящее получено', 'Из канала (чат, почта, вебхук) пришло сообщение.', 'Inbound received', 'A message arrived from a channel (chat, mail, webhook).'),
  E('intake', 'intake.classified', 'Входящее классифицировано', 'Определены проект и суть входящего сообщения.', 'Inbound classified', 'Project and intent of the inbound message were identified.'),
  E('intake', 'intake.routed', 'Входящее маршрутизировано', 'Сообщение направлено агенту или в очередь.', 'Inbound routed', 'The message was routed to an agent or queue.'),
  E('intake', 'intake.rejected', 'Входящее отклонено', 'Сообщение отклонено (спам, отсутствие проекта…).', 'Inbound rejected', 'The message was rejected (spam, no project…).'),
  E('intake', 'intake.cancelled', 'Обработка входящего отменена', 'Обработка сообщения отменена.', 'Inbound cancelled', 'Processing of the message was cancelled.'),
  // ── quality ──
  E('quality', 'agent.definition.proposed', 'Предложена эволюция агента', 'Появился черновик изменений определения агента.', 'Agent evolution proposed', 'A draft of agent definition changes appeared.'),
  E('quality', 'agent.definition.applied', 'Эволюция агента применена', 'Черновик применён человеком.', 'Agent evolution applied', 'A human applied the draft.'),
  E('quality', 'agent.definition.rejected', 'Эволюция агента отклонена', 'Черновик отклонён человеком.', 'Agent evolution rejected', 'A human rejected the draft.'),
  E('quality', 'agent.evaluation.completed', 'Оценка агента завершена', 'Прошёл прогон оценочного набора агента.', 'Agent evaluation completed', 'An agent evaluation run finished.'),
  E('quality', 'agent.evaluation.gate.passed', 'Гейт применения пройден', 'Оценочный гейт разрешил применение предложения.', 'Apply gate passed', 'The eval gate allowed the proposal to apply.'),
  E('quality', 'agent.evaluation.gate.blocked', 'Гейт применения заблокировал', 'Оценочный гейт отклонил применение предложения.', 'Apply gate blocked', 'The eval gate refused to apply the proposal.'),
  E('quality', 'agent.evaluation.gate.overridden', 'Гейт преодолён override', 'Писатель применил предложение вопреки красному гейту.', 'Apply gate overridden', 'A writer applied the proposal despite the red gate.'),
  E('quality', 'agent.evaluation.judge.calibrated', 'Калибровка LLM-судьи', 'Измерено согласие судьи с эталонными вердиктами.', 'LLM judge calibrated', 'Judge agreement with golden verdicts was measured.'),
  E('quality', 'agent.quality.dashboard_viewed', 'Дашборд качества открыт', 'Кто-то открыл отчёт качества агента.', 'Quality dashboard viewed', 'Someone opened the agent quality report.'),
  E('quality', 'skill.proposed', 'Навык предложен', 'Появился черновик навыка.', 'Skill proposed', 'A skill draft appeared.'),
  E('quality', 'skill.applied', 'Навык применён', 'Черновик навыка утверждён человеком.', 'Skill applied', 'A human applied the skill draft.'),
  E('quality', 'skill.rejected', 'Навык отклонён', 'Черновик навыка отклонён человеком.', 'Skill rejected', 'A human rejected the skill draft.'),
  E('quality', 'skill.evaluation.completed', 'Оценка навыка завершена', 'Прошёл прогон оценочного набора навыка.', 'Skill evaluation completed', 'A skill evaluation run finished.'),
  E('quality', 'eval.case_candidate.created', 'Кандидат оценочного кейса', 'Из рана предложен новый оценочный кейс.', 'Eval case candidate', 'A new evaluation case was proposed from a run.'),
  // ── sagas ──
  E('sagas', 'saga.registered', 'Сага зарегистрирована', 'Зарегистрирована новая длинная история работ.', 'Saga registered', 'A new long-running work saga was registered.'),
  E('sagas', 'saga.attached', 'Ран привязан к саге', 'Запуск присоединён к существующей саге.', 'Run attached to saga', 'A run was attached to an existing saga.'),
  // ── ops ──
  E('ops', 'notification.sent', 'Уведомление отправлено', 'Каналу доставлено уведомление.', 'Notification sent', 'A notification was delivered to a channel.'),
  E('ops', 'operation.reconciled', 'Операция сверена', 'Неопределённая операция закрыта решением человека.', 'Operation reconciled', 'An uncertain operation was closed by a human decision.'),
  E('ops', 'memory.read.failed', 'Ошибка чтения памяти', 'Не удалось прочитать запись памяти рана.', 'Memory read failed', 'A run memory record could not be read.'),
  E('ops', 'storage.snapshot', 'Снапшот хранилища', 'Плановый обход песочницы: объёмы по проектам и свободное место.', 'Storage snapshot', 'Scheduled sandbox walk: per-project volumes and free space.'),
]

/** Event lookup by raw type. */
export function eventInfo(type: string): EventMeta | undefined {
  return EVENT_CATALOG.find((e) => e.type === type)
}

/** Catalog slice for a locale, grouped in display order. */
export function localizedEventGroups(locale: Locale): Array<{ group: EventGroupMeta; events: Array<{ type: string; title: string; desc: string }> }> {
  return EVENT_GROUPS.map((group) => ({
    group,
    events: EVENT_CATALOG
      .filter((e) => e.group === group.id)
      .map((e) => ({ type: e.type, title: e[locale].title, desc: e[locale].desc })),
  })).filter((g) => g.events.length > 0)
}
