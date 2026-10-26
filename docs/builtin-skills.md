# Предустановленные скилы: офисный пакет (builtin skills)

## 1. Цель

Каждая инсталляция Temporality с первого старта имеет набор скилов,
нужных практически в любой компании: работа с PDF, DOCX, XLSX/CSV,
презентациями, ревью и генерацией документов. Пакет следует паттерну
builtin sandbox profiles (docs/sandbox-profiles.md §5): каталог в коде +
идемпотентный seed в реестр при старте ядра.

Скилы живут в реестре workspace (`workspace_skill` + версия с
`origin: initial`, docs/living-skills.md §4), а не на диске: они видны в
Skills UI, находятся через `skill_search`, инспектируются `skill_inspect`,
привязываются к агентам и эволюционируют через `skill_propose` как любые
другие.

## 2. Состав пакета

| Скил | Capabilities | Ключевое правило процедуры |
|---|---|---|
| `pdf-processing` | extract_text, extract_tables, split_merge_pages, fill_forms, ocr | Сначала проба toolchain (pdfplumber → pypdf → fitz); скан без текстового слоя = OCR, а не «нет содержимого» |
| `word-processing` | read_docx, edit_docx, edit_tables, edit_headers, apply_styles | Правки на уровне runs (`paragraph.text = …` уничтожает форматирование); сохранение в новый файл |
| `spreadsheet-analysis` | read_xlsx, read_csv, audit_formulas, aggregate_data, write_results | `data_only=True` для значений vs формул; каждая цифра с кросс-проверкой; источник не трогаем |
| `presentation-authoring` | create_pptx, edit_slides, add_tables_charts, write_speaker_notes, render_check | Заполнять placeholder'ы шаблона, не рисовать текстовые боксы сверху; render-check через soffice+view_image |
| `document-review` | diff_versions, find_contradictions, check_structure, classify_findings | Только чтение; каждое замечание = локация + цитата + severity; вердикт обязан следовать из findings |
| `document-generation` | fill_template, render_pdf, verify_output, deliver_document | Замена placeholder'ов на уровне runs; обязательная верификация (не осталось `{{…}}`, открытие, render); внешние документы — через `request_approval` |
| `archive-management` | inspect_archives, extract_archives, create_archives, password_protected, split_volumes, verify_integrity | Шифрование → немедленный `ask_human` за паролем (порядок действий §2.1.1); инспекция ДО извлечения (zip-slip, zip-бомбы, вложенные архивы); извлечение в новую директорию; пароли — через env, не в логи; обязательный integrity-тест |

Каждый скил объявляет `inputs/outputs`, `preconditions/postconditions`,
`evidence.required` (страницы/подсчёты/файлы — то, что можно проверить
после исполнения) и `runtime` (sandbox: required, network: restricted).

## 2.1. Готовые скрипты (bundled tooling)

С версии 1.1.0 каждый скил пакета несёт рабочий скрипт в приложении
«## Bundled files» — том же формате, что производит импорт (cmd/
skillimport.go): `### scripts/…` + код-блок. Поэтому скрипты видны во
вкладке «Файлы скила» с подсветкой и сканером рисков, доставляются агенту
через `skill_inspect` и проходят review как импортированные файлы.

| Скил | Скрипт | Подкоманды |
|---|---|---|
| pdf-processing | `scripts/pdf_tool.py` | info · extract · tables · split · merge · fill · ocr |
| word-processing | `scripts/docx_tool.py` | extract · replace (run-level) · tables |
| spreadsheet-analysis | `scripts/xlsx_tool.py` | inventory · analyze (с кросс-проверкой сумм) · to-csv |
| presentation-authoring | `scripts/pptx_tool.py` | inventory · build (по JSON-наброску, с проверкой placeholder'ов) |
| document-review | `scripts/doc_diff.py` | извлечение текста (docx/pdf/txt) + unified diff со статистикой |
| document-generation | `scripts/fill_template.py` | заполнение `{{placeholder}}` на уровне runs + отчёт о незаполненных |
| archive-management | `scripts/archive_tool.sh` | inspect (traversal/бомба) · extract · create · verify |

Модель исполнения: агент записывает файл в песочницу (`write_file`) и
запускает; при отсутствии библиотеки скрипт печатает подсказку
`pip install …` и выходит с кодом 3; ошибки — честный JSON с кодом 4.
Скрипты не пишут в исходные файлы и проверяют собственный вывод (пересчёт
страниц, остатки placeholder'ов, целостность архива). Пайплайн редактора
скилов проверяет их тем же сканером рисков, что и импортированные файлы.

### 2.1.1. Требования к образу песочницы

Офисному пакету нужен python3/pip и архивная тройка. Базовые alpine-образы
песочницы (`kernel/sandbox.ProfileDefinitions`, слой `apkTools`) с
26.10.2026 несут `unzip tar p7zip python3 py3-pip` — вместе с прежними
git/curl/jq/bash/ripgrep. Образы собираются администратором через
`POST /v1/workspace/sandbox-profiles/{id}/build`; профиль, собранный раньше,
продолжает жить на старом дайджесте — пересоберите, чтобы офисный пакет
работал полностью (инцидент 26.10: в node-образе не было ни python3, ни 7z,
и агент 8 ходов искал инструментацию вместо того, чтобы сразу спросить
пароль от шифрованного архива — порядок действий скила теперь требует
спрашивать первым).

Порядок действий при шифрованном архиве (archive-management, шаг 0):
обнаружили шифрование → немедленный `ask_human` за паролем; проверка
инструментария — параллельно или после. Пароль передаётся через переменную
окружения ARCHIVE_PASSWORD (не argv, не логи); неверный пароль → повторный
вопрос, не перебор.

## 3. Механика сидирования

- `workspace.SeedBuiltinSkills(ctx)` вызывается на старте ядра рядом с
  `SeedSandboxProfiles`; `KERNEL_BUILTIN_SKILLS=off` отключает.
- Свежая инсталляция получает пакет сразу. Существующая строка **обновляется
  до версии каталога**, только пока её текущая ревизия — нетронутый builtin
  (в version-строке `origin: initial`): правка человека или применённая
  эволюция агента (`origin: human-edit / agent-proposal`) закрепляют скил —
  каталог никогда не перезаписывает авторский контент. Так стенд с сидом
  1.0.0 поднимется до 1.1.0 (скрипты) без ручных действий.
- Следствие: удалённый админом builtin-скил вернётся при рестарте. Вместо
  удаления — отвязать от агентов (скил без привязок не попадает в промпты).
- Манифесты хранятся тем же JSON-путём, что и у всех скилов; тест
  `TestBuiltinSkillsCatalogSanity` парсит их через `skills.ParseManifest` —
  тот же путь, что и `resolveAgentSkills` на старте рана.

## 4. Инструменты в манифестах

Только реально существующие тулы агента: `run_command`, `read_file`,
`write_file`, `edit_file`, `view_image`, `send_file`, `ask_human`,
`request_approval`, `artifact_create/submit`, `remember`. Тест каталога
держит белый список — манифест с фантомным тулом падает на CI.

Особый случай: `document-review` объявлен без `write_file`/`edit_file` —
ревью не редактирует проверяемые документы (тест
`TestDocumentReviewDeclaresNoWriteTools`).

## 5. Runtime-ограничения контента

Процедуры написаны под честное поведение в песочнице:

- проверка наличия python-библиотек перед использованием (pdfplumber,
  pypdf, fitz, python-docx, openpyxl, pandas — опционально, python-pptx,
  pytesseract);
- `pip install` только если сеть песочницы позволяет; офлайн → деградация к
  установленному + честная пометка в отчёте, не молчаливое ухудшение;
- тяжёлые визуальные проверки (рендер PPTX/DOCX через `soffice --headless`)
  отмечаются как «выполнено/пропущено» — скил не заявляет проверку, которой
  не было.

## 6. Как добавить новый builtin

Один вызов-конструктор в `workspace/builtinskills.go`
(`builtinXxx() BuiltinSkill`) + строка в `BuiltinSkills()`. Тесты каталога
подхватят его автоматически: уникальность id, валидность манифеста,
секции SKILL.md, белый список тулов, обязательные evidence.

Правила контента:

1. SKILL.md: `## Purpose`, `## When to use` (+ When NOT), `## Procedure`
   (шаги с верификацией), `## Constraints`, `## Common mistakes`,
   `## Verification`.
2. Процедура учитывает офлайн-деградацию и границы песочницы.
3. Манифест ссылается только на существующие тулы; evidence проверяем.
4. Ни один скил не пишет в исходные файлы пользователя.
