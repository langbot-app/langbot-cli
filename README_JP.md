# langbot-cli

[English](README.md) / [简体中文](README_CN.md) / [繁體中文](README_TW.md) / 日本語 / [Español](README_ES.md) / [Français](README_FR.md) / [한국어](README_KO.md) / [Русский](README_RU.md) / [Tiếng Việt](README_VI.md)

LangBot の独立したコマンドラインツール `lbctl`。HTTP 経由で Workspace のリソースを管理し、複数の context、認証・機能の検出、リソース管理、RAG 設定の検出、Pipeline の拡張と試行実行、実行記録と Sandbox の診断、非同期タスクの待機、およびローカル Docker Compose のインストール・管理・アップグレードを提供します。

次の機能を提供する LangBot Core と組み合わせて使用します：

- API Key 認証付きの `/api/v1/system/context` と `/api/v1/system/capabilities`。
- Bot、Pipeline、Knowledge Base、Provider、Model、Plugin、Skill、MCP Server、タスクの API。
- Provider/Model の機密情報をマスクする `include_secret=false`。

`lbctl` は Core が宣言する capability、Workspace の紐付け、権限に基づいて操作します。

## インストール

macOS、Linux、Windows Git Bash：

```sh
curl -fsSL https://download.langbot.app/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://download.langbot.app/install.ps1 | iex
```

インストーラーはプラットフォームに合ったバイナリを選択し、SHA-256 を検証します。Shell の既定パスは `~/.local/bin`、PowerShell は `%LOCALAPPDATA%\Programs\lbctl` で、ユーザーの `PATH` に追加します。

Cloudflare Workers がインストーラー、バイナリ、チェックサムのミラーを配信します。[GitHub Releases](https://github.com/langbot-app/langbot-cli/releases) と同期し、元のファイルもそこから入手できます。`LBCTL_VERSION` で公開済みのバージョンを指定します。

## ソースからビルド

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

Go の基準バージョンは 1.26 です。ビルド済みバイナリを直接実行できます。ローカルのデプロイ管理には Docker Compose を使用します。

## リモートインスタンスの設定

API Key は環境変数または一度限りの stdin 入力で渡します。設定の既定パスは `~/.config/langbot/config.yaml` で、Key 本体ではなく環境変数名を保存します。

まず Key に対応する Workspace を確認します：

```sh
export LANGBOT_PRODUCTION_API_KEY='<api-key>'

LANGBOT_API_KEY="$LANGBOT_PRODUCTION_API_KEY" \
  lbctl --endpoint https://bot.example.com/langbot whoami
```

返された `workspace_uuid` を context に紐付けます：

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

複数の接続先を管理します：

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

接続設定は次の優先順位で解決し、接続先と認証情報を検証します。

| 設定 | 優先順位 |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → 現在の context |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → context 設定 |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → context の環境変数参照 |
| タイムアウト | `--timeout` → `LANGBOT_TIMEOUT` → context 設定 → 30s |

`--api-key-stdin` は単一の呼び出し用です。リクエスト本文用の `--file -` とは別々に使用します。一時 endpoint は制御された読み取りに対応し、書き込みには保存済みで Workspace に紐付いた context を使います。

## ローカルのデプロイとアップグレード

`adopt` は既存の Compose デプロイを、サービス設定を維持したままローカル管理対象として登録します：

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

新しいローカルデプロイをインストールするか、データを保持してアンインストールします：

```sh
lbctl install --version <vX.Y.Z> --profile basic --port 5300
lbctl uninstall --yes
```

`lbctl upgrade` は LangBot のサービスイメージを更新します。CLI でインストールしたデプロイと接管したデプロイで共通のコマンドを使います：

```sh
lbctl upgrade --version <newer-vX.Y.Z> --dry-run -o json
lbctl upgrade --version <newer-vX.Y.Z> --yes -o json
```

接管したデプロイでは `langbot`、`langbot_plugin_runtime`、存在する `langbot_box` のイメージを置き換え、ポート、マウント、環境設定、コメント、他のサービスを保持します。対象イメージを取得・検証し、停止後にデータ、Compose、存在するプロジェクト `.env`、デプロイ記録をバックアップしてから起動し、バージョンを確認します。失敗後もバックアップと復旧状態を保持し、`status` と `doctor` で確認できます。

自動アップグレードは `rockchin/langbot:<tag>` を直接指定し、全データが Compose ディレクトリの `data` バインドマウントにあり、ローカル SQLite、Chroma、local storage を使う構成に対応します。事前に Compose、バックアップ範囲、バージョンを検証し、新しいバージョンへ更新します。`--dry-run` は計画を表示し、実行時にイメージを検証します。Compose を手動変更した後は同じ対象で `adopt` を再実行して新しいダイジェストを確認します。

ローカルのライフサイクル操作はデプロイインスタンスを対象とします。Bot、Pipeline、Skill などは以下の共通コマンドで context の Workspace を管理します。

## リソース管理

複雑なリクエスト本文は LangBot HTTP API と同じフィールドの JSON または YAML ファイルで指定します。`--file -` は stdin を読み取ります。

### Bot と Pipeline

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

`pipeline apply` は本文に `uuid` があれば更新し、それ以外は作成します。

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

ドキュメントをアップロードし、取り込みを開始して完了を待ちます：

```sh
lbctl knowledge-base ingest <uuid> --file ./document.pdf --wait
```

### Provider と Model

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

Model の種類は `llm`、`embedding`、`rerank` です。Provider/Model の出力にはサーバー側のマスクとクライアント側のフィールド選別を適用し、モデルのスキャンは構造化結果を返します。

### Plugin と Skill

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

Skill インストールの `--dry-run` はサーバー側プレビューを使います。Plugin のインストール、更新、削除は `--wait` で非同期タスクを追跡できます。

GitHub プラグインの入力は `owner`、`repo`、`release_tag` と、`release_id`/`asset_id` または `asset_url` です。URL は CLI が GitHub release を照会して WebUI と同じ ID ベースのリクエストに変換します。GitHub の照会は Workspace 認証から分離され、dry-run は事前チェックを行います。例：

```yaml
owner: langbot-app
repo: langbot-plugins
release_tag: 0.0.1.alpha.1
asset_url: https://github.com/langbot-app/langbot-plugins/releases/download/0.0.1.alpha.1/langbot-team-GoogleSearch-0.1.2.lbpkg
```

プラグイン設定の更新は、制限時間内に再読み込み後の値を取得して確認します。削除は実行中のプラグインと起動に失敗したインストールの両方に対応します。

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

## 書き込みとタスク

書き込みでは保存済み context、Workspace の紐付け、実際の認証主体、権限、capability、リソースの前提条件を確認します。共通ルール：

- `--dry-run` は事前チェックを行い、Skill インストールはサーバー側プレビューを使います。
- 削除には明示的な `--yes` が必要です。
- 検証可能な書き込みは完了後にリソースを再取得します。
- 結果が不明な場合は `result_unknown` を返し、確認してから次の操作を判断できます。
- 複数段階の操作は作成済みリソースや `task_id` を保持して進捗を追跡できます。

Bot/Pipeline の読み取りでは通常の設定を保持し、認証情報を `***` で隠します。作成、更新、Pipeline のコピー後に取得可能なフィールドを照合し、不一致は `verification_failed` を返します。`verification.observable_fields_match` は照合結果、`unverified_fields` は隠されたフィールドを示します。すべての照合対象が検証可能で一致した場合のみ `verified` が true になります。Bot 更新では値全体が `***` の場合に既存の認証情報を保持し、安全に復元できない場合は更新前に拒否します。

非同期タスクを個別に照会できます：

```sh
lbctl task list
lbctl task list --type <type> --kind <kind>
lbctl task get <task-id>
lbctl task get <task-id> --wait --wait-timeout 10m
```

ローカルの待機とサーバー側タスクは独立しています。`task get` で引き続き確認できます。

## 制御された API アクセス

`api get/post` は組み込み registry に登録された操作を呼び出します：

```sh
lbctl api get '/api/v1/system/tasks?kind=plugin'
lbctl api post '/api/v1/knowledge/bases/<uuid>/retrieve' --file query.yaml
```

Provider/Model の管理、インストール、テストなどの機密性の高い操作には専用コマンドを使います。`raw` は `/api/v1/system/info` の互換読み取りを提供します。

## 出力とチェック

既定の出力は読みやすい簡潔な表示です。自動化には `-o json` または `-o yaml` で完全な構造化結果を取得できます。成功・エラー結果は stdout、診断情報は stderr に出力します。

`status` は現在の環境を要約し、識別情報の不一致やアップグレード復旧の警告は表示します。同じコマンドに `-o json` または `-o yaml` を追加すると、完全なインスタンス/Workspace ID と配置先パスを確認できます。能力一覧の取得は全操作への対応を意味しません。各操作の対応状況は `capabilities` で確認してください。

| 終了コード | 意味 |
|---|---|
| 0 | 成功 |
| 2 | 入力・設定エラー |
| 3 / 4 / 5 | 認証エラー / 権限不足 / 見つからない |
| 6 | 前提条件不足・競合 |
| 7 | ネットワーク、TLS、タイムアウト、キャンセル |
| 8 | API・プロトコルの非互換 |
| 9 | デプロイの復旧が必要 |
| 10 | 未分類のサーバー・内部エラー |

```sh
make check
make cross-build

# Read-only diagnostics against a running Core.
LBCTL_TEST_ENDPOINT=http://localhost:5300 \
  go test ./internal/integration -run TestLiveCore -v
```

`make cross-build` は macOS、Linux、Windows の amd64/arm64 バイナリと `dist/checksums.txt` を生成します。

Agent 向けのコマンド検出と shell 補完：

```sh
lbctl schema
lbctl schema --command plugin.install.github
lbctl completion bash
```

## RAG 設定の検出

これらのコマンドは Core の対応 capability と `resource.view` 権限を使い、`-o json` と `-o yaml` に対応します。

```sh
lbctl knowledge-engine list
lbctl knowledge-engine creation-schema author/engine -o yaml
lbctl knowledge-engine retrieval-schema author/engine -o json
lbctl knowledge-parser list --mime-type application/pdf
```

エンジン一覧から `plugin_id` を選びます。creation schema は知識ベース作成設定、retrieval schema は検索設定を定義します。`knowledge-base create`、`ingest`、`retrieve` で取り込みと検索を行います。

## Pipeline の拡張バインディング

```sh
lbctl pipeline extensions get PIPELINE_UUID -o json
```

`pipeline extensions update PIPELINE_UUID --file extensions.yaml` はバインディングを**全置換**します。次の 8 フィールドはすべて必須です。`extensions get` で現在の設定と利用可能なリソースを確認し、保持する項目をすべて含めます。更新後は再取得で検証し、`--dry-run` は入力と前提条件をチェックします。

次の例は 4 種類の明示的なバインディングを空にし、すべての有効化スイッチをオフにします。

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

空配列と `enable_all_*: false` はその種類の拡張をバインドしない設定、`true` は利用可能なすべての拡張を有効にする設定です。Plugin は `{author: author, name: plugin}`、MCP Server は `available_mcp_servers` の `uuid`、Skill は `available_skills` の `name` を使います。MCP Resource の例は `{server_uuid: SERVER_UUID, uri: "docs://guide"}` です。`mcp_resource_agent_read_enabled` は Agent のリソース列挙・読み取りと、バインド済みリソースのコンテキスト追加を制御します。

更新には保存済みで Workspace に紐付いた context、`resource.manage` と `resource.view`、Core の対応 capability が必要です。

## Pipeline の単一ターン試行実行

```sh
lbctl pipeline run PIPELINE_UUID --message 'ナレッジベースを使ってこの質問に答えてください' --timeout 2m -o json
```

保存済みで Workspace に紐付いた context と `runtime.operate`、`resource.view` を使い、設定済みのモデルとツールを実際に呼び出します。実行ごとに新しいセッションを作成します。応答、セッション ID、クエリ ID、利用可能なメッセージ ID を返し、失敗時は非ゼロ終了コードになります。

Core は最大 60 秒待機し、CLI の `--timeout` は HTTP リクエストの待機を制御します。タイムアウトは**実行状態が不明**であることを示します。WebUI の監視ページ（`/home/monitoring`）でメッセージ・セッション ID、または Pipeline と時刻から記録を確認できます。

Core は `pipeline.run` capability を宣言します。`--dry-run` で前提条件を確認できます。

## アプリケーション実行記録の診断

Core の対応 capability を使い、現在の API Key の Workspace にある実行記録、失敗したメッセージの詳細、呼び出しエラーを照会します。

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

記録の照会には `resource.view` が必要です。一覧は `--limit 1..500` と `--offset` を使い、Core の上限も適用されます。`--start-time` と `--end-time` は ISO 8601 です。フィルターは各コマンドの `--help` を参照してください。

## Sandbox の読み取り専用診断

```sh
lbctl sandbox status
lbctl sandbox sessions
lbctl sandbox errors
```

現在の Workspace を照会します。状態は `resource.view`、セッションとエラーは `audit.view` を使います。管理対象 Sandbox はサーバー側の受付チェックに従い、状態には `enabled`、`available`、サーバーの説明を含みます。

Core の対応 capability を使い、`-o json` と `-o yaml` に対応します。`enabled: false` は無効、`enabled: true` かつ `available: false` は利用不可を示します。
