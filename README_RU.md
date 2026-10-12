# langbot-cli

[English](README.md) / [简体中文](README_CN.md) / [繁體中文](README_TW.md) / [日本語](README_JP.md) / [Español](README_ES.md) / [Français](README_FR.md) / [한국어](README_KO.md) / Русский / [Tiếng Việt](README_VI.md)

Самостоятельный инструмент командной строки LangBot — `lbctl`. Управляет ресурсами Workspace по HTTP: несколько context, определение идентичности и возможностей, управление ресурсами, конфигурация RAG, расширения и пробные запуски Pipeline, записи выполнения и диагностика Sandbox, ожидание асинхронных задач, а также установка, принятие под управление, запуск, остановка и обновление локальных развёртываний Docker Compose.

Используйте LangBot Core со следующими возможностями:

- Аутентификация API Key для `/api/v1/system/context` и `/api/v1/system/capabilities`.
- API Bot, Pipeline, Knowledge Base, Provider, Model, Plugin, Skill, MCP Server и задач.
- Скрытие секретов на сервере через `include_secret=false` в запросах Provider/Model.

`lbctl` выполняет операции с учётом объявленных Core возможностей, привязки Workspace и прав.

## Установка

macOS, Linux или Windows Git Bash:

```sh
curl -fsSL https://download.langbot.app/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://download.langbot.app/install.ps1 | iex
```

Установщик выбирает бинарный файл для платформы и проверяет SHA-256. Shell устанавливает в `~/.local/bin`, PowerShell — в `%LOCALAPPDATA%\Programs\lbctl` и добавляет путь в пользовательский `PATH`.

Cloudflare Workers раздаёт зеркало установщиков, бинарных файлов и контрольных сумм, синхронизированное с [GitHub Releases](https://github.com/langbot-app/langbot-cli/releases). Оригинальные файлы также доступны там. Задайте `LBCTL_VERSION` для конкретной опубликованной версии.

## Сборка из исходников

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

Базовая версия Go — 1.26. Собранный бинарный файл запускается напрямую; локальные развёртывания управляются через Docker Compose.

## Настройка удалённых экземпляров

Передайте API Key через переменную окружения или однократный ввод stdin. Конфигурация хранится в `~/.config/langbot/config.yaml` и содержит имя переменной вместо самого ключа.

Сначала определите Workspace, связанный с ключом:

```sh
export LANGBOT_PRODUCTION_API_KEY='<api-key>'

LANGBOT_API_KEY="$LANGBOT_PRODUCTION_API_KEY" \
  lbctl --endpoint https://bot.example.com/langbot whoami
```

Привяжите полученный `workspace_uuid` к context:

```sh
lbctl context add production \
  --endpoint https://bot.example.com/langbot \
  --api-key-env LANGBOT_PRODUCTION_API_KEY \
  --expect-workspace '<workspace_uuid>' \
  --timeout 15s

lbctl context use production
lbctl context check production
lbctl whoami
lbctl capabilities
```

Управление несколькими целями:

```sh
lbctl context add dev --endpoint http://localhost:5300 \
  --api-key-env LANGBOT_DEV_API_KEY \
  --expect-workspace '<workspace_uuid>'
lbctl context list
lbctl context show production
lbctl context check --all
lbctl --context dev whoami
lbctl context update production --timeout 30s
lbctl context remove dev --yes
```

Параметры соединения определяются в следующем порядке; выбранная цель и учётные данные проверяются.

| Параметр | Приоритет |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → текущий context |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → конфигурация context |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → ссылка на переменную context |
| Тайм-аут | `--timeout` → `LANGBOT_TIMEOUT` → конфигурация context → 30s |

`--api-key-stdin` используется для одного вызова, отдельно от `--file -` для тела запроса. Временные endpoint поддерживают контролируемое чтение; запись ресурсов использует сохранённый context с привязкой к Workspace.

## Локальное развёртывание и обновление

`adopt` регистрирует существующее развёртывание Compose для локального управления, сохраняя настройки сервисов:

```sh
lbctl --endpoint http://127.0.0.1:5300 adopt \
  --file /absolute/path/docker-compose.yaml --project langbot --profile all
lbctl status
lbctl doctor
lbctl stop
lbctl start
lbctl restart
lbctl logs --follow --tail 100
```

Установите новое локальное развёртывание или удалите его, сохранив данные:

```sh
lbctl install --version <vX.Y.Z> --profile basic --port 5300
lbctl uninstall --yes
```

`lbctl upgrade` обновляет образы сервисов LangBot. Установленные через CLI и принятые под управление развёртывания используют одинаковые команды:

```sh
lbctl upgrade --version <newer-vX.Y.Z> --dry-run -o json
lbctl upgrade --version <newer-vX.Y.Z> --yes -o json
```

Обновление принятого развёртывания заменяет образы `langbot`, `langbot_plugin_runtime` и существующего `langbot_box`, сохраняя порты, монтирования, окружение, комментарии и другие сервисы. Оно загружает и проверяет образ, останавливает развёртывание, создаёт резервную копию данных, Compose, существующего `.env` проекта и записи развёртывания, затем запускает и проверяет версию. После сбоя резервные копии и состояние восстановления сохраняются для проверки через `status` и `doctor`.

Автоматическое обновление поддерживает сервисы с прямым указанием `rockchin/langbot:<tag>`, всеми данными в bind-монтировании `data` каталога Compose и локальными SQLite, Chroma и local storage. Перед переходом на более новую версию проверяются конфигурация, охват резервного копирования и версии. `--dry-run` показывает план, а выполнение проверяет образ. После ручного изменения Compose снова вызовите `adopt` для той же цели, чтобы подтвердить новый дайджест.

Локальные команды жизненного цикла работают с экземпляром развёртывания. Bot, Pipeline, Skill и другие ресурсы управляются общими командами ниже в Workspace выбранного context.

## Управление ресурсами

Для сложных тел запроса используйте JSON или YAML с полями LangBot HTTP API. `--file -` читает stdin.

### Bot и Pipeline

```sh
lbctl bot list
lbctl bot get <uuid>
lbctl bot create --file bot.yaml
lbctl bot update <uuid> --file bot.yaml
lbctl bot delete <uuid> --yes

lbctl pipeline list
lbctl pipeline get <uuid>
lbctl pipeline apply --file pipeline.yaml
lbctl pipeline copy <uuid>
lbctl pipeline delete <uuid> --yes
```

`pipeline apply` обновляет ресурс, если тело содержит `uuid`, иначе создаёт его.

### Knowledge Base

```sh
lbctl knowledge-base list
lbctl knowledge-base get <uuid>
lbctl knowledge-base create --file knowledge-base.yaml
lbctl knowledge-base update <uuid> --file knowledge-base.yaml
lbctl knowledge-base retrieve <uuid> --file query.yaml
lbctl knowledge-base file list <uuid>
lbctl knowledge-base file delete <uuid> <file-id> --yes
lbctl knowledge-base delete <uuid> --yes
```

Загрузите документ, запустите импорт и дождитесь завершения:

```sh
lbctl knowledge-base ingest <uuid> --file ./document.pdf --wait
```

### Provider и Model

```sh
lbctl provider list
lbctl provider get <uuid>
lbctl provider create --file provider.yaml
lbctl provider update <uuid> --file provider.yaml
lbctl provider scan-models <uuid> --type llm
lbctl provider delete <uuid> --yes

lbctl model list
lbctl model list --type llm --provider <provider-uuid>
lbctl model get <uuid> --type llm
lbctl model create --type llm --file model.yaml
lbctl model update <uuid> --type llm --file model.yaml
lbctl model test <uuid> --type llm
lbctl model delete <uuid> --type llm --yes
```

Типы Model: `llm`, `embedding` и `rerank`. Вывод Provider/Model применяет серверное скрытие секретов и клиентскую фильтрацию полей; поиск моделей возвращает структурированные результаты.

### Plugin и Skill

```sh
lbctl plugin list
lbctl plugin get <author> <name>
lbctl plugin config get <author> <name>
lbctl plugin config update <author> <name> --file config.yaml
lbctl plugin install github --file plugin.yaml --wait
lbctl plugin install marketplace --file plugin.yaml --wait
lbctl plugin install local --file plugin.zip --wait
lbctl plugin upgrade <author> <name> --wait
lbctl plugin logs <author> <name>
lbctl plugin delete <author> <name> --yes --wait

lbctl skill list
lbctl skill get <name>
lbctl skill preview <name>
lbctl skill file list <name>
lbctl skill file read <name> <path>
lbctl skill file write <name> <path> --file content.md
lbctl skill install github --file skill.yaml
lbctl skill install upload --file skill.zip
lbctl skill delete <name> --yes
```

Установка Skill с `--dry-run` использует серверный предпросмотр. Установка, обновление и удаление Plugin поддерживают `--wait` для отслеживания асинхронных задач.

Запрос GitHub содержит `owner`, `repo`, `release_tag` и `release_id`/`asset_id` либо `asset_url`. Для URL CLI запрашивает release и отправляет те же ID, что WebUI. Запросы GitHub отделены от аутентификации Workspace; dry-run проверяет предварительные условия. Пример:

```yaml
owner: langbot-app
repo: langbot-plugins
release_tag: 0.0.1.alpha.1
asset_url: https://github.com/langbot-app/langbot-plugins/releases/download/0.0.1.alpha.1/langbot-team-GoogleSearch-0.1.2.lbpkg
```

Обновление настроек плагина ожидает повторного чтения после перезагрузки в пределах тайм-аута. Удаление очищает работающие плагины и установки, завершившиеся ошибкой запуска.

### MCP Server

```sh
lbctl mcp-server list
lbctl mcp-server get <name>
lbctl mcp-server create --file server.yaml
lbctl mcp-server update <name> --file server.yaml
lbctl mcp-server test <name> --wait
lbctl mcp-server resources <name>
lbctl mcp-server resource-templates <name>
lbctl mcp-server resource-read <name> --file request.yaml
lbctl mcp-server logs <name>
lbctl mcp-server delete <name> --yes
```

## Запись и задачи

Запись проверяет сохранённый context, привязку Workspace, фактическую идентичность, права, возможности и условия ресурсов. Общие правила:

- `--dry-run` проверяет предварительные условия; установка Skill использует серверный предпросмотр.
- Удаление требует явного `--yes`.
- Проверяемые операции записи повторно читают ресурс после завершения.
- При неопределённом результате возвращается `result_unknown` для проверки перед следующим действием.
- Многошаговые операции сохраняют созданные ресурсы или `task_id` для отслеживания прогресса.

Чтение Bot/Pipeline сохраняет обычные настройки и скрывает учётные данные с помощью `***`. После создания, обновления и копирования Pipeline доступные поля сравниваются с прочитанными значениями; несовпадения возвращают `verification_failed`. `verification.observable_fields_match` показывает результат, а `unverified_fields` перечисляет скрытые поля. `verified` равен true только тогда, когда все проверяемые поля доступны для проверки и совпадают. Обновление Bot сохраняет существующие учётные данные для значений, точно равных `***`; если безопасное восстановление невозможно, обновление отклоняется до отправки.

Асинхронные задачи можно запрашивать отдельно:

```sh
lbctl task list
lbctl task list --type <type> --kind <kind>
lbctl task get <task-id>
lbctl task get <task-id> --wait --wait-timeout 10m
```

Локальное ожидание и жизненный цикл серверной задачи независимы; продолжайте проверку через `task get`.

## Контролируемый доступ к API

`api get/post` вызывает операции из встроенного реестра:

```sh
lbctl api get '/api/v1/system/tasks?kind=plugin'
lbctl api post '/api/v1/knowledge/bases/<uuid>/retrieve' --file query.yaml
```

Для Provider/Model, установки, тестов и других чувствительных операций используйте специальные команды. `raw` предоставляет совместимое чтение `/api/v1/system/info`.

## Вывод и проверки

Вывод по умолчанию краток и удобен для чтения. Автоматизация использует `-o json` или `-o yaml` для полного структурированного результата. Результаты и ошибки идут в stdout, диагностика — в stderr.

`status` кратко показывает активное окружение, сохраняя предупреждения о несовпадении идентификаторов и необходимости восстановления. Добавьте `-o json` или `-o yaml` к той же команде для полных идентификаторов экземпляра/Workspace и путей развёртывания. Получение списка возможностей не означает поддержку всех операций; проверяйте их состояния через `capabilities`.

| Код выхода | Значение |
|---|---|
| 0 | Успех |
| 2 | Ошибка ввода или конфигурации |
| 3 / 4 / 5 | Ошибка аутентификации / недостаточно прав / не найдено |
| 6 | Не выполнено условие или конфликт |
| 7 | Сеть, TLS, тайм-аут или отмена |
| 8 | Несовместимость API или протокола |
| 9 | Требуется восстановление развёртывания |
| 10 | Неклассифицированная ошибка сервера или внутренняя ошибка |

```sh
make check
make cross-build

# Read-only diagnostics against a running Core.
LBCTL_TEST_ENDPOINT=http://localhost:5300 \
  go test ./internal/integration -run TestLiveCore -v
```

`make cross-build` создаёт бинарные файлы amd64/arm64 для macOS, Linux и Windows, а также `dist/checksums.txt`.

Поиск команд для агентов и автодополнение shell:

```sh
lbctl schema
lbctl schema --command plugin.install.github
lbctl completion bash
```

## Поиск конфигурации RAG

Команды используют соответствующие возможности Core и требуют `resource.view`. Поддерживаются `-o json` и `-o yaml`.

```sh
lbctl knowledge-engine list
lbctl knowledge-engine creation-schema author/engine -o yaml
lbctl knowledge-engine retrieval-schema author/engine -o json
lbctl knowledge-parser list --mime-type application/pdf
```

Выберите `plugin_id` в списке движков. Creation schema определяет настройки создания базы знаний, retrieval schema — настройки поиска. Используйте `knowledge-base create`, `ingest` и `retrieve` для импорта и поиска.

## Привязки расширений Pipeline

```sh
lbctl pipeline extensions get PIPELINE_UUID -o json
```

`pipeline extensions update PIPELINE_UUID --file extensions.yaml` **полностью заменяет привязки**. Все восемь полей обязательны. Проверьте текущие привязки и ресурсы через `extensions get`, сохранив в запросе все нужные элементы. Обновление проверяется повторным чтением; `--dry-run` проверяет ввод и условия.

Пример очищает четыре списка явных привязок и выключает все переключатели.

```yaml
bound_plugins: []
bound_mcp_servers: []
bound_skills: []
bound_mcp_resources: []
enable_all_plugins: false
enable_all_mcp_servers: false
enable_all_skills: false
mcp_resource_agent_read_enabled: false
```

Пустые массивы с `enable_all_*: false` оставляют тип без привязки; `true` включает все доступные расширения этого типа. Plugin использует `{author: author, name: plugin}`, MCP Server — `uuid` из `available_mcp_servers`, Skill — `name` из `available_skills`. Пример MCP Resource: `{server_uuid: SERVER_UUID, uri: "docs://guide"}`. `mcp_resource_agent_read_enabled` управляет перечислением и чтением ресурсов Agent и включением привязанных ресурсов в контекст.

Обновление требует сохранённого context с привязкой Workspace, прав `resource.manage` и `resource.view`, а также соответствующей возможности Core.

## Одноходовый пробный запуск Pipeline

```sh
lbctl pipeline run PIPELINE_UUID --message 'Ответь на этот вопрос, используя базу знаний' --timeout 2m -o json
```

Запуск использует сохранённый context с привязкой Workspace, права `runtime.operate` и `resource.view` и вызывает настроенные модели и инструменты. Каждый запуск создаёт новую сессию. Возвращаются ответ, ID сессии, ID запроса и доступные ID сообщений; ошибка даёт ненулевой код выхода.

Core ожидает до 60 секунд; `--timeout` CLI управляет ожиданием HTTP. Тайм-аут означает **неизвестный статус выполнения**. Проверьте записи по ID сообщения/сессии на странице мониторинга WebUI (`/home/monitoring`) либо найдите недавние записи по Pipeline и времени.

Core объявляет `pipeline.run`; `--dry-run` проверяет предварительные условия.

## Диагностика записей выполнения

Запрашивайте записи выполнения, подробности ошибочных сообщений и ошибки вызовов в Workspace текущего API Key через соответствующие возможности Core.

```sh
lbctl monitoring errors --pipeline PIPELINE_UUID --limit 20
lbctl monitoring messages --pipeline PIPELINE_UUID --start-time 2026-09-16T10:00:00+08:00 --end-time 2026-09-16T10:05:00+08:00
lbctl monitoring messages --session SESSION_ID --limit 20
lbctl monitoring message MESSAGE_ID
lbctl monitoring session SESSION_ID
lbctl monitoring llm-calls --pipeline PIPELINE_UUID
lbctl monitoring tool-calls --session SESSION_ID
lbctl monitoring embedding-calls --knowledge-base KB_UUID
lbctl monitoring sessions --active true
```

Запросы требуют `resource.view`. Списки поддерживают `--limit 1..500` и `--offset` с учётом ограничений Core. `--start-time` и `--end-time` используют ISO 8601. Фильтры описаны в `--help`.

## Диагностика Sandbox только для чтения

```sh
lbctl sandbox status
lbctl sandbox sessions
lbctl sandbox errors
```

Sandbox запрашивает текущий Workspace: состояние требует `resource.view`, сессии и ошибки — `audit.view`. Управляемый sandbox использует серверные проверки допуска; состояние содержит `enabled`, `available` и пояснения сервера.

Core объявляет соответствующие возможности; поддерживаются `-o json` и `-o yaml`. `enabled: false` означает отключение; `enabled: true` с `available: false` — недоступность.
