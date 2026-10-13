# langbot-cli

[English](README.md) / [简体中文](README_CN.md) / [繁體中文](README_TW.md) / [日本語](README_JP.md) / [Español](README_ES.md) / [Français](README_FR.md) / [한국어](README_KO.md) / [Русский](README_RU.md) / Tiếng Việt

Công cụ dòng lệnh độc lập của LangBot, có tên `lbctl`. Quản lý tài nguyên Workspace qua HTTP, hỗ trợ nhiều context, khám phá danh tính và khả năng, quản lý tài nguyên, cấu hình RAG, tiện ích mở rộng và chạy thử Pipeline, bản ghi thực thi và chẩn đoán Sandbox, chờ tác vụ bất đồng bộ, cùng cài đặt, tiếp quản, quản lý vòng đời và nâng cấp triển khai Docker Compose cục bộ.

Sử dụng với LangBot Core cung cấp các khả năng sau:

- Xác thực API Key cho `/api/v1/system/context` và `/api/v1/system/capabilities`.
- API Bot, Pipeline, Knowledge Base, Provider, Model, Plugin, Skill, MCP Server và tác vụ.
- Che thông tin bí mật phía máy chủ qua `include_secret=false` cho truy vấn Provider/Model.

`lbctl` thực hiện thao tác theo capability, liên kết Workspace và quyền mà Core khai báo.

## Cài đặt

macOS, Linux hoặc Windows Git Bash:

```sh
curl -fsSL https://download.langbot.app/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://download.langbot.app/install.ps1 | iex
```

Trình cài đặt chọn tệp nhị phân phù hợp nền tảng và kiểm tra SHA-256. Shell mặc định dùng `~/.local/bin`; PowerShell dùng `%LOCALAPPDATA%\Programs\lbctl` và thêm vào `PATH` của người dùng.

Cloudflare Workers phân phối bản mirror của trình cài đặt, tệp nhị phân và checksum, đồng bộ từ [GitHub Releases](https://github.com/langbot-app/langbot-cli/releases). Có thể tải tệp gốc tại đó. Đặt `LBCTL_VERSION` để chọn phiên bản đã phát hành.

## Biên dịch từ mã nguồn

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

Phiên bản Go cơ sở là 1.26. Tệp nhị phân đã biên dịch chạy trực tiếp; quản lý triển khai cục bộ sử dụng Docker Compose.

## Cấu hình instance từ xa

Cung cấp API Key qua biến môi trường hoặc stdin cho một lần gọi. Cấu hình mặc định nằm tại `~/.config/langbot/config.yaml`, lưu tên biến môi trường thay cho nội dung khóa.

Sau khi khởi tạo, hãy tạo Workspace API Key trong mục quản lý API Key của LangBot đích. Lệnh cần xác thực phát hiện thông tin đăng nhập bị thiếu và hiển thị biến môi trường được chọn, ví dụ PowerShell/Bash với đầu vào ẩn và bước kiểm tra `whoami` giữ nguyên tùy chọn kết nối. JSON/YAML có `error.reason` và `error.guidance`; loại lỗi vẫn là `auth`, mã thoát là 3. Khóa bị từ chối được phân biệt với biến chưa đặt; CLI không tự chờ nhập và không lưu khóa.

Trước tiên xác định Workspace của khóa:

```sh
export LANGBOT_PRODUCTION_API_KEY='<api-key>'

LANGBOT_API_KEY="$LANGBOT_PRODUCTION_API_KEY" \
  lbctl --endpoint https://bot.example.com/langbot whoami
```

Liên kết `workspace_uuid` trả về với context:

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

Quản lý nhiều đích:

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

Tham số kết nối được xử lý theo thứ tự sau, đồng thời xác minh đích và thông tin xác thực.

| Tham số | Thứ tự ưu tiên |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → context hiện tại |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → cấu hình context |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → biến môi trường của context |
| Thời gian chờ | `--timeout` → `LANGBOT_TIMEOUT` → cấu hình context → 30s |

Dùng `--api-key-stdin` cho một lần gọi và dùng riêng `--file -` để nhập nội dung yêu cầu. Endpoint tạm thời hỗ trợ đọc có kiểm soát; ghi tài nguyên sử dụng context đã lưu và liên kết với Workspace.

## Triển khai cục bộ và nâng cấp

`adopt` đăng ký triển khai Compose có sẵn để quản lý cục bộ, giữ nguyên cấu hình dịch vụ:

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

Cài đặt triển khai mới hoặc gỡ cài đặt nhưng giữ lại dữ liệu:

```sh
lbctl install --version <vX.Y.Z> --profile basic --port 5300
lbctl uninstall --yes
```

`lbctl upgrade` nâng cấp image dịch vụ LangBot. Triển khai do CLI cài đặt và triển khai được tiếp quản dùng cùng các lệnh:

```sh
lbctl upgrade --version <newer-vX.Y.Z> --dry-run -o json
lbctl upgrade --version <newer-vX.Y.Z> --yes -o json
```

Nâng cấp thay image của `langbot`, `langbot_plugin_runtime` và `langbot_box` hiện có, giữ cổng, mount, môi trường, chú thích và dịch vụ khác. Quy trình tải và xác minh image, dừng triển khai, sao lưu dữ liệu, Compose, `.env` dự án nếu có và bản ghi triển khai, rồi khởi động và kiểm tra phiên bản. Khi thất bại, bản sao lưu và trạng thái khôi phục được giữ để kiểm tra bằng `status`, `doctor`.

Nâng cấp tự động hỗ trợ khai báo trực tiếp `rockchin/langbot:<tag>`, toàn bộ dữ liệu trong bind mount `data` của thư mục Compose, dùng SQLite, Chroma và local storage cục bộ. Cấu hình, phạm vi sao lưu và phiên bản được kiểm tra trước khi nâng cấp lên phiên bản cao hơn. `--dry-run` hiển thị kế hoạch; khi thực thi sẽ xác minh image. Sau khi sửa Compose thủ công, chạy lại `adopt` cho cùng đích để xác nhận digest mới.

Lệnh vòng đời cục bộ tác động đến instance triển khai. Bot, Pipeline, Skill và tài nguyên khác dùng bộ lệnh chung dưới đây để quản lý Workspace của context.

## Quản lý tài nguyên

Dùng tệp JSON hoặc YAML cho nội dung yêu cầu phức tạp, với trường tương ứng API HTTP LangBot. `--file -` đọc từ stdin.

### Bot và Pipeline

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

`pipeline apply` cập nhật nếu nội dung có `uuid`, ngược lại tạo mới.

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

Tải tài liệu lên, gửi tác vụ nạp dữ liệu và chờ hoàn tất:

```sh
lbctl knowledge-base ingest <uuid> --file ./document.pdf --wait
```

### Provider và Model

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

Các loại Model là `llm`, `embedding`, `rerank`. Kết quả Provider/Model áp dụng che bí mật phía máy chủ và lọc trường phía máy khách; quét mô hình trả về kết quả có cấu trúc.

### Plugin và Skill

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

Cài Skill với `--dry-run` dùng bản xem trước phía máy chủ. Cài đặt, nâng cấp và xóa Plugin hỗ trợ `--wait` để theo dõi tác vụ bất đồng bộ.

Yêu cầu plugin GitHub gồm `owner`, `repo`, `release_tag` và `release_id`/`asset_id` hoặc `asset_url`. Với URL, CLI tra cứu release rồi gửi cùng ID như WebUI. Truy vấn GitHub tách biệt với xác thực Workspace; dry-run kiểm tra điều kiện trước. Ví dụ:

```yaml
owner: langbot-app
repo: langbot-plugins
release_tag: 0.0.1.alpha.1
asset_url: https://github.com/langbot-app/langbot-plugins/releases/download/0.0.1.alpha.1/langbot-team-GoogleSearch-0.1.2.lbpkg
```

Cập nhật cấu hình plugin chờ đọc lại sau khi tải lại trong thời gian giới hạn. Lệnh xóa dọn cả plugin đang chạy và cài đặt khởi động thất bại.

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

## Ghi dữ liệu và tác vụ

Thao tác ghi kiểm tra context đã lưu, liên kết Workspace, danh tính thực, quyền, capability và điều kiện tài nguyên. Quy tắc chung:

- `--dry-run` kiểm tra điều kiện trước; cài Skill dùng bản xem trước phía máy chủ.
- Xóa cần xác nhận rõ ràng bằng `--yes`.
- Thao tác ghi có thể xác minh sẽ đọc lại tài nguyên sau khi hoàn tất.
- Kết quả chưa chắc chắn trả về `result_unknown` để kiểm tra trước khi quyết định bước tiếp theo.
- Thao tác nhiều bước giữ tài nguyên đã tạo hoặc `task_id` để theo dõi tiến độ.

Khi đọc Bot/Pipeline, cấu hình thông thường được giữ lại và thông tin xác thực được che bằng `***`. Sau khi tạo, cập nhật hoặc sao chép Pipeline, CLI đọc lại để so sánh các trường quan sát được; nếu không khớp, trả về `verification_failed`. `verification.observable_fields_match` cho biết kết quả, còn `unverified_fields` liệt kê các trường bị che. `verified` chỉ là true khi mọi trường được kiểm tra đều có thể xác minh và khớp. Khi cập nhật Bot, giá trị chính xác `***` giữ nguyên thông tin xác thực hiện có; nếu không thể khôi phục an toàn, CLI từ chối trước khi cập nhật.

Có thể truy vấn tác vụ bất đồng bộ riêng:

```sh
lbctl task list
lbctl task list --type <type> --kind <kind>
lbctl task get <task-id>
lbctl task get <task-id> --wait --wait-timeout 10m
```

Việc chờ cục bộ độc lập với vòng đời tác vụ máy chủ; dùng `task get` để tiếp tục theo dõi.

## Truy cập API có kiểm soát

`api get/post` gọi các thao tác được đăng ký trong registry tích hợp:

```sh
lbctl api get '/api/v1/system/tasks?kind=plugin'
lbctl api post '/api/v1/knowledge/bases/<uuid>/retrieve' --file query.yaml
```

Dùng lệnh chuyên biệt cho Provider/Model, cài đặt, kiểm thử và thao tác nhạy cảm. `raw` hỗ trợ đọc tương thích `/api/v1/system/info`.

## Đầu ra và kiểm tra

Đầu ra mặc định ngắn gọn, dễ đọc. Tự động hóa dùng `-o json` hoặc `-o yaml` để lấy kết quả có cấu trúc đầy đủ. Kết quả thành công và lỗi ghi vào stdout, chẩn đoán vào stderr.

`status` tóm tắt môi trường hiện tại và vẫn hiển thị cảnh báo sai danh tính hoặc cần khôi phục nâng cấp. Thêm `-o json` hoặc `-o yaml` vào cùng lệnh để xem đầy đủ ID instance/Workspace và đường dẫn triển khai. Lấy được danh sách khả năng không có nghĩa mọi thao tác đều được hỗ trợ; kiểm tra trạng thái từng thao tác bằng `capabilities`.

| Mã thoát | Ý nghĩa |
|---|---|
| 0 | Thành công |
| 2 | Lỗi đầu vào hoặc cấu hình |
| 3 / 4 / 5 | Lỗi xác thực / thiếu quyền / không tìm thấy |
| 6 | Thiếu điều kiện hoặc xung đột |
| 7 | Mạng, TLS, hết thời gian hoặc hủy |
| 8 | API hoặc giao thức không tương thích |
| 9 | Triển khai cần khôi phục |
| 10 | Lỗi máy chủ hoặc nội bộ chưa phân loại |

```sh
make check
make cross-build

# Read-only diagnostics against a running Core.
LBCTL_TEST_ENDPOINT=http://localhost:5300 \
  go test ./internal/integration -run TestLiveCore -v
```

`make cross-build` tạo tệp nhị phân amd64/arm64 cho macOS, Linux, Windows và `dist/checksums.txt`.

Khám phá lệnh dành cho Agent và tự động hoàn thành shell:

```sh
lbctl schema
lbctl schema --command plugin.install.github
lbctl completion bash
```

## Khám phá cấu hình RAG

Các lệnh dùng capability tương ứng của Core và cần quyền `resource.view`. Hỗ trợ `-o json`, `-o yaml`.

```sh
lbctl knowledge-engine list
lbctl knowledge-engine creation-schema author/engine -o yaml
lbctl knowledge-engine retrieval-schema author/engine -o json
lbctl knowledge-parser list --mime-type application/pdf
```

Chọn `plugin_id` từ danh sách engine. Creation schema định nghĩa cấu hình tạo cơ sở tri thức, retrieval schema định nghĩa tham số truy xuất. Dùng `knowledge-base create`, `ingest`, `retrieve` để nạp và truy xuất.

## Liên kết tiện ích mở rộng Pipeline

```sh
lbctl pipeline extensions get PIPELINE_UUID -o json
```

`pipeline extensions update PIPELINE_UUID --file extensions.yaml` **thay toàn bộ liên kết**. Cả tám trường đều bắt buộc. Dùng `extensions get` để xem liên kết và tài nguyên có sẵn, giữ mọi mục cần dùng trong yêu cầu. Cập nhật được xác minh bằng đọc lại; `--dry-run` kiểm tra đầu vào và điều kiện.

Ví dụ xóa bốn danh sách liên kết tường minh và tắt toàn bộ công tắc.

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

Mảng rỗng với `enable_all_*: false` để loại tiện ích đó không được liên kết; `true` bật tất cả tiện ích có sẵn của loại đó. Plugin dùng `{author: author, name: plugin}`; MCP Server dùng `uuid` từ `available_mcp_servers`; Skill dùng `name` từ `available_skills`. MCP Resource ví dụ: `{server_uuid: SERVER_UUID, uri: "docs://guide"}`. `mcp_resource_agent_read_enabled` điều khiển Agent liệt kê, đọc và thêm tài nguyên liên kết vào ngữ cảnh.

Cập nhật cần context đã lưu và liên kết Workspace, quyền `resource.manage`, `resource.view` và capability tương ứng của Core.

## Chạy thử Pipeline một lượt

```sh
lbctl pipeline run PIPELINE_UUID --message 'Trả lời câu hỏi này bằng cơ sở tri thức' --timeout 2m -o json
```

Chạy thử dùng context đã lưu và liên kết Workspace, cần `runtime.operate`, `resource.view`, và gọi mô hình cùng công cụ đã cấu hình. Mỗi lần tạo phiên mới. Trả về câu trả lời, ID phiên, ID truy vấn và ID thông điệp có sẵn; thất bại trả mã thoát khác 0.

Core chờ tối đa 60 giây; `--timeout` của CLI kiểm soát thời gian chờ HTTP. Hết thời gian nghĩa là **trạng thái thực thi chưa xác định**. Xem bản ghi bằng ID thông điệp/phiên tại trang giám sát WebUI (`/home/monitoring`), hoặc tìm theo Pipeline và thời gian.

Core khai báo `pipeline.run`; dùng `--dry-run` kiểm tra điều kiện trước.

## Chẩn đoán bản ghi thực thi

Dùng capability tương ứng của Core để truy vấn bản ghi, chi tiết thông điệp thất bại và lỗi gọi trong Workspace của API Key hiện tại.

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

Truy vấn cần `resource.view`. Danh sách hỗ trợ `--limit 1..500`, `--offset`, theo giới hạn Core. `--start-time`, `--end-time` dùng ISO 8601. Xem bộ lọc trong `--help`.

## Chẩn đoán Sandbox chỉ đọc

```sh
lbctl sandbox status
lbctl sandbox sessions
lbctl sandbox errors
```

Sandbox truy vấn Workspace hiện tại: trạng thái cần `resource.view`, phiên và lỗi cần `audit.view`. Sandbox được quản lý dùng kiểm tra cho phép phía máy chủ; trạng thái gồm `enabled`, `available` và giải thích của máy chủ.

Core khai báo capability tương ứng; hỗ trợ `-o json`, `-o yaml`. `enabled: false` nghĩa là tắt; `enabled: true` với `available: false` nghĩa là không khả dụng.
