# langbot-cli

[English](README.md) / [简体中文](README_CN.md) / 繁體中文 / [日本語](README_JP.md) / [Español](README_ES.md) / [Français](README_FR.md) / [한국어](README_KO.md) / [Русский](README_RU.md) / [Tiếng Việt](README_VI.md)

LangBot 的獨立命令列工具，執行檔名稱為 `lbctl`。透過 HTTP 管理 Workspace 資源，支援多個 context、身分與能力探索、資源管理、RAG 設定探索、Pipeline 擴充與試執行、執行紀錄與 Sandbox 診斷、非同步任務等待，以及本機 Docker Compose 部署的安裝、接管、啟停與升級。

搭配提供以下能力的 LangBot Core：

- API Key 驗證的 `/api/v1/system/context` 與 `/api/v1/system/capabilities`。
- Bot、Pipeline、Knowledge Base、Provider、Model、Plugin、Skill、MCP Server 與任務查詢介面。
- Provider/Model 查詢的伺服器端遮罩參數 `include_secret=false`。

`lbctl` 依據 Core 宣告的 capability、Workspace 綁定與權限執行操作。

## 安裝

macOS、Linux 或 Windows Git Bash：

```sh
curl -fsSL https://download.langbot.app/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://download.langbot.app/install.ps1 | iex
```

安裝腳本自動選擇適合平台的執行檔並驗證 SHA-256。Shell 預設安裝至 `~/.local/bin`；PowerShell 預設安裝至 `%LOCALAPPDATA%\Programs\lbctl`，並加入使用者 `PATH`。

下載鏡像由 Cloudflare Workers 分發，包含安裝腳本、執行檔與校驗檔。發行套件同步自 [GitHub Releases](https://github.com/langbot-app/langbot-cli/releases)，也可直接下載原始檔案。設定 `LBCTL_VERSION` 可安裝指定已發布版本。

## 從原始碼建置

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

Go 基準版本為 1.26。編譯後的執行檔可直接執行；本機部署管理使用 Docker Compose。

## 設定遠端實例

API Key 由環境變數或單次 stdin 輸入提供。設定預設儲存於 `~/.config/langbot/config.yaml`，儲存環境變數名稱而非 Key 本身。

先查詢 Key 所屬的 Workspace：

```sh
export LANGBOT_PRODUCTION_API_KEY='<api-key>'

LANGBOT_API_KEY="$LANGBOT_PRODUCTION_API_KEY" \
  lbctl --endpoint https://bot.example.com/langbot whoami
```

將回傳的 `workspace_uuid` 綁定至 context：

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

管理多個目標：

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

連線參數依以下優先順序解析，並驗證選定的目標與憑證。

| 參數 | 優先順序 |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → 目前 context |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → context 設定 |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → context 的環境變數參照 |
| 逾時 | `--timeout` → `LANGBOT_TIMEOUT` → context 設定 → 30s |

`--api-key-stdin` 用於單次呼叫；請與請求內容輸入 `--file -` 分開使用。臨時 endpoint 支援受控讀取；資源寫入使用已儲存且綁定 Workspace 的 context。

## 本機部署與升級

`adopt` 將現有 Compose 部署登記為本機受管部署，保留服務設定：

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

安裝新的本機部署，或解除安裝並保留資料：

```sh
lbctl install --version <vX.Y.Z> --profile basic --port 5300
lbctl uninstall --yes
```

`lbctl upgrade` 升級 LangBot 服務映像。CLI 安裝與接管的部署使用相同命令：

```sh
lbctl upgrade --version <newer-vX.Y.Z> --dry-run -o json
lbctl upgrade --version <newer-vX.Y.Z> --yes -o json
```

接管部署升級會替換 `langbot`、`langbot_plugin_runtime` 與現有 `langbot_box` 的映像，保留其他 Compose 內容，包括連接埠、掛載、環境設定、註解與其他服務。流程先拉取並驗證目標映像，再停止部署，備份資料、Compose、存在的專案 `.env` 與部署紀錄，最後啟動並驗證版本。失敗後保留備份與恢復狀態，可使用 `status`、`doctor` 檢查。

自動升級支援直接宣告 `rockchin/langbot:<tag>` 的服務，資料完整位於 Compose 目錄的 `data` 綁定目錄，並使用本機 SQLite、Chroma 與 local storage。升級前驗證 Compose、備份範圍與版本；目標版本高於目前版本。`--dry-run` 顯示計畫，執行時驗證目標映像。手動修改接管的 Compose 後，再次對相同目標執行 `adopt`，確認新的資產摘要。

本機生命週期命令以部署實例為目標；Bot、Pipeline、Skill 等資源透過下方同一套命令管理 context 所屬的 Workspace。

## 管理資源

複雜請求內容使用 JSON 或 YAML 檔案，欄位與 LangBot HTTP API 一致。`--file -` 從 stdin 讀取。

### Bot 與 Pipeline

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

`pipeline apply` 的請求包含 `uuid` 時更新，否則建立。

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

上傳文件、提交匯入並等待任務完成：

```sh
lbctl knowledge-base ingest <uuid> --file ./document.pdf --wait
```

### Provider 與 Model

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

Model 類型支援 `llm`、`embedding` 與 `rerank`。Provider/Model 輸出使用伺服器端遮罩與客戶端欄位篩選；模型掃描提供結構化結果。

### Plugin 與 Skill

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

Skill 安裝的 `--dry-run` 使用伺服器端預覽。Plugin 安裝、升級與刪除支援 `--wait` 追蹤非同步任務。

GitHub 插件請求包含 `owner`、`repo`、`release_tag`，以及 `release_id`/`asset_id` 或 `asset_url`。CLI 會將連結輸入解析為與 WebUI 相同的 ID 請求；GitHub 查詢與 Workspace 驗證分離，dry-run 執行前置檢查。範例：

```yaml
owner: langbot-app
repo: langbot-plugins
release_tag: 0.0.1.alpha.1
asset_url: https://github.com/langbot-app/langbot-plugins/releases/download/0.0.1.alpha.1/langbot-team-GoogleSearch-0.1.2.lbpkg
```

插件設定更新會在有限時間內等待重新載入後的回讀；刪除命令可清理執行中與啟動失敗的插件安裝。

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

## 寫入與任務

寫入依序核對已儲存的 context、Workspace 綁定、實際身分、權限、capability 與必要資源前提。通用規則：

- `--dry-run` 執行前置檢查；Skill 安裝使用伺服器端預覽。
- 刪除需要明確提供 `--yes`。
- 可驗證的寫入完成後會回讀資源。
- 結果不確定時回傳 `result_unknown`，便於核查後決定下一步。
- 多步操作保留已建立的資源或 `task_id`，便於追蹤進度。

Bot/Pipeline 讀取保留一般設定，憑證以 `***` 遮蔽。建立、更新與 Pipeline 複製會重新讀取並核對可觀察欄位，不一致時回傳 `verification_failed`。`verification.observable_fields_match` 表示核對結果，`unverified_fields` 列出隱藏欄位；僅在所有核對欄位都可驗證且一致時，`verified` 才為 true。Bot 更新會為完整的 `***` 佔位符保留原憑證，無法安全還原的佔位符會在更新前遭拒絕。

非同步任務也可獨立查詢：

```sh
lbctl task list
lbctl task list --type <type> --kind <kind>
lbctl task get <task-id>
lbctl task get <task-id> --wait --wait-timeout 10m
```

本機等待與伺服器端任務生命週期獨立，可用 `task get` 繼續查詢。

## 受控 API 入口

`api get/post` 呼叫內建 registry 登記的操作：

```sh
lbctl api get '/api/v1/system/tasks?kind=plugin'
lbctl api post '/api/v1/knowledge/bases/<uuid>/retrieve' --file query.yaml
```

Provider/Model 管理、安裝、測試與其他敏感操作使用專用命令。`raw` 提供 `/api/v1/system/info` 的相容讀取。

## 輸出與檢查

預設為便於閱讀的精簡輸出；自動化可用 `-o json` 或 `-o yaml` 取得完整結構化結果。成功與錯誤結果寫入 stdout，診斷資訊寫入 stderr。

| 結束代碼 | 意義 |
|---|---|
| 0 | 成功 |
| 2 | 輸入或設定錯誤 |
| 3 / 4 / 5 | 驗證錯誤 / 權限不足 / 找不到 |
| 6 | 前提未滿足或衝突 |
| 7 | 網路、TLS、逾時或取消 |
| 8 | API 或協定不相容 |
| 9 | 部署需要恢復處理 |
| 10 | 未分類伺服器或內部錯誤 |

```sh
make check
make cross-build

# Read-only diagnostics against a running Core.
LBCTL_TEST_ENDPOINT=http://localhost:5300 \
  go test ./internal/integration -run TestLiveCore -v
```

`make cross-build` 產生 macOS、Linux、Windows 的 amd64/arm64 執行檔及 `dist/checksums.txt`。

供 Agent 使用的命令探索與 shell 補全：

```sh
lbctl schema
lbctl schema --command plugin.install.github
lbctl completion bash
```

## RAG 設定探索

命令使用 Core 對應的 capability，並要求 `resource.view` 權限；支援 `-o json` 或 `-o yaml`。

```sh
lbctl knowledge-engine list
lbctl knowledge-engine creation-schema author/engine -o yaml
lbctl knowledge-engine retrieval-schema author/engine -o json
lbctl knowledge-parser list --mime-type application/pdf
```

從引擎列表選擇 `plugin_id`；creation schema 定義知識庫建立參數，retrieval schema 定義檢索參數。接著使用 `knowledge-base create`、`ingest`、`retrieve` 進行匯入與檢索。

## Pipeline 擴充綁定

```sh
lbctl pipeline extensions get PIPELINE_UUID -o json
```

`pipeline extensions update PIPELINE_UUID --file extensions.yaml` **完整替換**綁定，以下八個欄位皆必填。先用 `extensions get` 查看目前綁定與可用資源，保留所有需要的項目。更新後自動回讀驗證，`--dry-run` 檢查輸入與前提。

此範例清空四類明確綁定，並關閉全部啟用開關。

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

空陣列搭配 `enable_all_*: false` 表示該類型不綁定；`true` 啟用該類型所有可用擴充。插件項使用 `{author: author, name: plugin}`；MCP Server 使用 `available_mcp_servers` 的 `uuid`；Skill 使用 `available_skills` 的 `name`。MCP Resource 範例為 `{server_uuid: SERVER_UUID, uri: "docs://guide"}`。`mcp_resource_agent_read_enabled` 控制 Agent 列出、讀取 MCP 資源及將綁定資源納入上下文。

更新要求已儲存且綁定 Workspace 的 context、`resource.manage` 與 `resource.view` 權限及 Core 對應 capability。

## Pipeline 單輪試執行

```sh
lbctl pipeline run PIPELINE_UUID --message '请根据知识库回答这个问题' --timeout 2m -o json
```

試執行使用已儲存且綁定 Workspace 的 context，要求 `runtime.operate` 與 `resource.view`，並呼叫設定的模型與工具。每次建立新工作階段，回傳回覆、工作階段 ID、查詢 ID 與可用訊息 ID；失敗回傳非零結束代碼。

Core 最多等待 60 秒，CLI 的 `--timeout` 控制 HTTP 請求等待。逾時表示**執行狀態未知**；可在 WebUI 監控頁（`/home/monitoring`）以訊息或工作階段 ID 查詢紀錄，也可依 Pipeline 與時間查詢近期紀錄。

Core 宣告 `pipeline.run` capability；`--dry-run` 可檢查前置條件。

## 應用程式執行紀錄診斷

使用 Core 對應 capability，查詢目前 API Key 所屬 Workspace 的執行紀錄、失敗訊息與呼叫錯誤。

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

紀錄查詢要求 `resource.view`。列表支援 `--limit 1..500` 與 `--offset`，由 Core 設定進一步限制。`--start-time`、`--end-time` 使用 ISO 8601；篩選條件請見各命令 `--help`。

## Sandbox 唯讀診斷

```sh
lbctl sandbox status
lbctl sandbox sessions
lbctl sandbox errors
```

Sandbox 查詢目前 Workspace：狀態要求 `resource.view`，工作階段與錯誤要求 `audit.view`。受管 sandbox 使用伺服器端准入檢查，狀態包含 `enabled`、`available` 與伺服器說明。

Core 宣告對應 capability；支援 `-o json` 與 `-o yaml`。`enabled: false` 表示停用，`enabled: true` 且 `available: false` 表示不可用。
