# langbot-cli

[English](README.md) / [简体中文](README_CN.md) / [繁體中文](README_TW.md) / [日本語](README_JP.md) / [Español](README_ES.md) / [Français](README_FR.md) / 한국어 / [Русский](README_RU.md) / [Tiếng Việt](README_VI.md)

LangBot의 독립 명령줄 도구 `lbctl`입니다. HTTP로 Workspace 리소스를 관리하며 여러 context, ID 및 기능 검색, 리소스 관리, RAG 설정 검색, Pipeline 확장과 시험 실행, 실행 기록 및 Sandbox 진단, 비동기 작업 대기, 로컬 Docker Compose 설치·관리·업그레이드를 제공합니다.

다음 기능을 제공하는 LangBot Core와 함께 사용합니다:

- API Key 인증을 사용하는 `/api/v1/system/context`와 `/api/v1/system/capabilities`.
- Bot, Pipeline, Knowledge Base, Provider, Model, Plugin, Skill, MCP Server 및 작업 API.
- Provider/Model 조회에서 서버 측 비밀 정보 마스킹을 위한 `include_secret=false`.

`lbctl`은 Core가 선언한 capability, Workspace 연결 및 권한에 따라 작업을 실행합니다.

## 설치

macOS, Linux 또는 Windows Git Bash:

```sh
curl -fsSL https://download.langbot.app/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://download.langbot.app/install.ps1 | iex
```

설치 스크립트는 플랫폼에 맞는 바이너리를 선택하고 SHA-256을 확인합니다. Shell 기본 경로는 `~/.local/bin`, PowerShell은 `%LOCALAPPDATA%\Programs\lbctl`이며 사용자 `PATH`에 추가합니다.

Cloudflare Workers가 설치 스크립트, 바이너리, 체크섬 미러를 배포합니다. [GitHub Releases](https://github.com/langbot-app/langbot-cli/releases)에서 동기화하며 원본도 다운로드할 수 있습니다. `LBCTL_VERSION`으로 특정 배포 버전을 지정합니다.

## 소스에서 빌드

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

Go 기준 버전은 1.26입니다. 컴파일된 바이너리를 직접 실행하며 로컬 배포 관리에는 Docker Compose를 사용합니다.

## 원격 인스턴스 설정

API Key는 환경 변수 또는 일회성 stdin 입력으로 제공합니다. 기본 설정 파일은 `~/.config/langbot/config.yaml`이며 Key 자체 대신 환경 변수 이름을 저장합니다.

초기 설정 후 대상 LangBot 관리 화면의 API Key 관리에서 Workspace API Key를 생성합니다. 인증이 필요한 명령은 자격 증명 누락을 감지하고 선택된 환경 변수, 입력을 숨기는 PowerShell/Bash 예제, 기존 연결 옵션을 유지한 `whoami` 확인 절차를 안내합니다. JSON/YAML에는 `error.reason`과 `error.guidance`가 포함되며 오류 유형은 `auth`, 종료 코드는 3으로 유지됩니다. Key 거부와 변수 미설정을 구분하며, CLI는 자동으로 입력을 기다리거나 Key를 저장하지 않습니다.

먼저 Key에 연결된 Workspace를 확인합니다:

```sh
export LANGBOT_PRODUCTION_API_KEY='<api-key>'

LANGBOT_API_KEY="$LANGBOT_PRODUCTION_API_KEY" \
  lbctl --endpoint https://bot.example.com/langbot whoami
```

반환된 `workspace_uuid`를 context에 연결합니다:

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

여러 대상 관리:

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

연결 설정은 다음 우선순위로 결정되며 선택한 대상과 인증 정보를 검증합니다.

| 설정 | 우선순위 |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → 현재 context |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → context 설정 |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → context의 환경 변수 참조 |
| 시간 제한 | `--timeout` → `LANGBOT_TIMEOUT` → context 설정 → 30s |

`--api-key-stdin`은 일회성 호출에 사용하고 요청 본문 입력인 `--file -`와는 별도로 사용합니다. 임시 endpoint는 제어된 읽기를 지원하며 리소스 쓰기는 저장된 Workspace 연결 context를 사용합니다.

## 로컬 배포와 업그레이드

`adopt`는 서비스 설정을 유지하면서 기존 Compose 배포를 로컬 관리 대상으로 등록합니다:

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

새 로컬 배포를 설치하거나 데이터를 유지하면서 제거합니다:

```sh
lbctl install --version <vX.Y.Z> --profile basic --port 5300
lbctl uninstall --yes
```

`lbctl upgrade`는 LangBot 서비스 이미지를 업그레이드합니다. CLI로 설치한 배포와 등록한 배포는 같은 명령을 사용합니다:

```sh
lbctl upgrade --version <newer-vX.Y.Z> --dry-run -o json
lbctl upgrade --version <newer-vX.Y.Z> --yes -o json
```

등록한 배포의 업그레이드는 `langbot`, `langbot_plugin_runtime`, 기존 `langbot_box` 이미지를 교체하고 포트, 마운트, 환경 설정, 주석과 다른 서비스를 유지합니다. 이미지를 다운로드·검증하고 배포를 중지한 뒤 데이터, Compose, 존재하는 프로젝트 `.env`, 배포 기록을 백업하고 시작하여 버전을 확인합니다. 실패 후에도 백업과 복구 상태를 유지하며 `status`, `doctor`로 확인합니다.

자동 업그레이드는 `rockchin/langbot:<tag>`를 직접 선언하고 모든 데이터가 Compose 디렉터리의 `data` 바인드 마운트에 있으며 로컬 SQLite, Chroma, local storage를 사용하는 구성을 지원합니다. 설정, 백업 범위와 버전을 확인한 뒤 더 높은 버전으로 업그레이드합니다. `--dry-run`은 계획을 표시하고 실행 시 이미지를 검증합니다. Compose를 수동 변경한 후 같은 대상에 `adopt`를 다시 실행하여 새 다이제스트를 확인합니다.

로컬 수명 주기 명령은 배포 인스턴스를 대상으로 합니다. Bot, Pipeline, Skill 등의 리소스는 아래 공통 명령으로 context의 Workspace를 관리합니다.

## 리소스 관리

복잡한 요청 본문은 LangBot HTTP API와 같은 필드의 JSON 또는 YAML 파일로 전달합니다. `--file -`는 stdin에서 읽습니다.

### Bot과 Pipeline

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

`pipeline apply`는 본문에 `uuid`가 있으면 업데이트하고 그 외에는 생성합니다.

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

문서를 업로드하고 수집 작업을 제출하여 완료를 기다립니다:

```sh
lbctl knowledge-base ingest <uuid> --file ./document.pdf --wait
```

### Provider와 Model

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

Model 유형은 `llm`, `embedding`, `rerank`입니다. Provider/Model 출력은 서버 마스킹과 클라이언트 필드 필터링을 적용하며 모델 검색은 구조화된 결과를 제공합니다.

### Plugin과 Skill

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

Skill 설치의 `--dry-run`은 서버 미리보기를 사용합니다. Plugin 설치, 업그레이드, 삭제는 `--wait`로 비동기 작업을 추적합니다.

GitHub 플러그인 요청은 `owner`, `repo`, `release_tag` 및 `release_id`/`asset_id` 또는 `asset_url`을 포함합니다. URL 입력은 CLI가 release를 조회하여 WebUI와 같은 ID 요청으로 변환합니다. GitHub 조회는 Workspace 인증과 분리되어 있으며 dry-run은 사전 검사를 수행합니다. 예:

```yaml
owner: langbot-app
repo: langbot-plugins
release_tag: 0.0.1.alpha.1
asset_url: https://github.com/langbot-app/langbot-plugins/releases/download/0.0.1.alpha.1/langbot-team-GoogleSearch-0.1.2.lbpkg
```

플러그인 설정 업데이트는 제한된 시간 동안 재시작 후 값을 다시 읽어 검증합니다. 삭제 명령은 실행 중인 플러그인과 시작 실패 설치를 모두 정리합니다.

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

## 쓰기와 작업

쓰기 작업은 저장된 context, Workspace 연결, 실제 ID, 권한, capability 및 리소스 사전 조건을 확인합니다. 공통 규칙:

- `--dry-run`은 사전 검사를 수행하며 Skill 설치는 서버 미리보기를 사용합니다.
- 삭제에는 명시적인 `--yes`가 필요합니다.
- 검증 가능한 쓰기는 완료 후 리소스를 다시 읽습니다.
- 결과가 불확실하면 `result_unknown`을 반환하여 확인 후 다음 작업을 결정할 수 있습니다.
- 다단계 작업은 생성한 리소스 또는 `task_id`를 유지하여 진행 상황을 추적합니다.

Bot/Pipeline 조회는 일반 설정을 유지하고 인증 정보는 `***`로 가립니다. 생성, 수정, Pipeline 복사 후 다시 조회하여 확인 가능한 필드를 비교하며, 불일치는 `verification_failed`를 반환합니다. `verification.observable_fields_match`는 비교 결과를, `unverified_fields`는 가려진 필드를 나타냅니다. 모든 비교 대상 필드를 검증할 수 있고 값이 일치할 때만 `verified`가 true입니다. Bot 수정은 값 전체가 `***`인 자리표시자의 기존 인증 정보를 유지하며, 안전하게 복원할 수 없으면 수정 전에 거부합니다.

비동기 작업을 별도로 조회합니다:

```sh
lbctl task list
lbctl task list --type <type> --kind <kind>
lbctl task get <task-id>
lbctl task get <task-id> --wait --wait-timeout 10m
```

로컬 대기와 서버 작업 수명 주기는 독립적이며 `task get`으로 계속 조회할 수 있습니다.

## 제어된 API 접근

`api get/post`는 내장 registry에 등록된 작업을 호출합니다:

```sh
lbctl api get '/api/v1/system/tasks?kind=plugin'
lbctl api post '/api/v1/knowledge/bases/<uuid>/retrieve' --file query.yaml
```

Provider/Model 관리, 설치, 테스트 및 민감한 작업은 전용 명령을 사용합니다. `raw`는 `/api/v1/system/info`의 호환 읽기를 제공합니다.

## 출력과 검사

기본 출력은 간결하고 읽기 쉽습니다. 자동화에는 `-o json` 또는 `-o yaml`로 전체 구조화 결과를 얻습니다. 성공 및 오류 결과는 stdout, 진단은 stderr로 출력합니다.

`status`는 현재 환경을 요약하고 식별 정보 불일치 및 업그레이드 복구 경고를 유지합니다. 같은 명령에 `-o json` 또는 `-o yaml`을 추가하면 전체 인스턴스/Workspace ID와 배포 경로를 확인할 수 있습니다. 기능 목록을 가져왔다는 것이 모든 작업을 지원한다는 뜻은 아닙니다. 각 작업의 지원 상태는 `capabilities`로 확인하세요.

| 종료 코드 | 의미 |
|---|---|
| 0 | 성공 |
| 2 | 입력 또는 설정 오류 |
| 3 / 4 / 5 | 인증 오류 / 권한 부족 / 찾을 수 없음 |
| 6 | 사전 조건 미충족 또는 충돌 |
| 7 | 네트워크, TLS, 시간 초과 또는 취소 |
| 8 | API 또는 프로토콜 비호환 |
| 9 | 배포 복구 필요 |
| 10 | 분류되지 않은 서버 또는 내부 오류 |

```sh
make check
make cross-build

# Read-only diagnostics against a running Core.
LBCTL_TEST_ENDPOINT=http://localhost:5300 \
  go test ./internal/integration -run TestLiveCore -v
```

`make cross-build`는 macOS, Linux, Windows의 amd64/arm64 바이너리와 `dist/checksums.txt`를 생성합니다.

Agent용 명령 검색과 shell 자동 완성:

```sh
lbctl schema
lbctl schema --command plugin.install.github
lbctl completion bash
```

## RAG 설정 검색

이 명령은 Core의 해당 capability와 `resource.view` 권한을 사용하며 `-o json`, `-o yaml`을 지원합니다.

```sh
lbctl knowledge-engine list
lbctl knowledge-engine creation-schema author/engine -o yaml
lbctl knowledge-engine retrieval-schema author/engine -o json
lbctl knowledge-parser list --mime-type application/pdf
```

엔진 목록에서 `plugin_id`를 선택합니다. creation schema는 지식 베이스 생성 설정을, retrieval schema는 검색 설정을 정의합니다. `knowledge-base create`, `ingest`, `retrieve`로 수집 및 검색합니다.

## Pipeline 확장 연결

```sh
lbctl pipeline extensions get PIPELINE_UUID -o json
```

`pipeline extensions update PIPELINE_UUID --file extensions.yaml`은 연결을 **전체 교체**합니다. 아래 여덟 필드는 모두 필수입니다. `extensions get`으로 현재 연결과 사용 가능한 리소스를 확인하고 유지할 모든 항목을 포함합니다. 업데이트 후 다시 읽어 검증하고 `--dry-run`은 입력과 사전 조건을 확인합니다.

이 예제는 네 종류의 명시적 연결 목록을 비우고 모든 활성화 스위치를 끕니다.

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

빈 배열과 `enable_all_*: false`는 해당 유형을 연결하지 않으며 `true`는 사용 가능한 전체 확장을 활성화합니다. Plugin은 `{author: author, name: plugin}`, MCP Server는 `available_mcp_servers`의 `uuid`, Skill은 `available_skills`의 `name`을 사용합니다. MCP Resource 예: `{server_uuid: SERVER_UUID, uri: "docs://guide"}`. `mcp_resource_agent_read_enabled`는 Agent의 리소스 목록·읽기와 연결 리소스의 컨텍스트 포함을 제어합니다.

업데이트에는 저장된 Workspace 연결 context, `resource.manage`, `resource.view` 및 해당 Core capability가 필요합니다.

## Pipeline 단일 턴 시험 실행

```sh
lbctl pipeline run PIPELINE_UUID --message '지식 베이스를 사용해 이 질문에 답해주세요' --timeout 2m -o json
```

저장된 Workspace 연결 context와 `runtime.operate`, `resource.view` 권한을 사용하며 설정된 모델과 도구를 호출합니다. 실행마다 새 세션을 만듭니다. 응답, 세션 ID, 쿼리 ID, 사용 가능한 메시지 ID를 반환하며 실패 시 0이 아닌 코드로 종료합니다.

Core는 최대 60초 기다리고 CLI `--timeout`은 HTTP 대기 시간을 제어합니다. 시간 초과는 **실행 상태 알 수 없음**을 의미합니다. WebUI 모니터링 페이지(`/home/monitoring`)에서 메시지·세션 ID 또는 Pipeline과 시간으로 기록을 확인합니다.

Core는 `pipeline.run` capability를 선언하며 `--dry-run`으로 사전 조건을 확인합니다.

## 애플리케이션 실행 기록 진단

Core의 해당 capability로 현재 API Key의 Workspace에서 실행 기록, 실패한 메시지 상세와 호출 오류를 조회합니다.

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

조회에는 `resource.view`가 필요합니다. 목록은 `--limit 1..500`, `--offset`을 지원하며 Core 제한이 적용됩니다. `--start-time`, `--end-time`은 ISO 8601을 사용합니다. 필터는 각 명령의 `--help`를 확인하세요.

## Sandbox 읽기 전용 진단

```sh
lbctl sandbox status
lbctl sandbox sessions
lbctl sandbox errors
```

현재 Workspace를 조회합니다. 상태는 `resource.view`, 세션과 오류는 `audit.view`를 사용합니다. 관리형 sandbox는 서버 승인 검사를 사용하며 상태에 `enabled`, `available` 및 서버 설명을 포함합니다.

Core의 해당 capability를 사용하며 `-o json`, `-o yaml`을 지원합니다. `enabled: false`는 비활성화, `enabled: true`와 `available: false`는 사용 불가를 의미합니다.
