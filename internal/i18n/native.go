package i18n

func init() {
	for key, value := range map[string]string{
		"LangBot process %d has not exited: %w":                    "LangBot 进程 %d 尚未退出：%w",
		"waiting for LangBot processes to exit: %w":                "等待 LangBot 进程退出：%w",
		"cannot remove LangBot files; close LangBot and retry: %w": "无法删除 LangBot 文件；请关闭 LangBot 后重试：%w",
		"invalid native supervisor PID: %s":                        "原生守护进程 PID 无效：%s",
		"deployment changed":                                       "部署已变更", "invalid instance name: %s": "实例名称无效：%s",
		"region must be auto, cn or global":                                               "区域必须为 auto、cn 或 global",
		"windows-run requires Windows":                                                    "windows-run 仅支持 Windows",
		"HKCU Run command exceeds 260 characters; use shorter lbctl and deployment paths": "HKCU Run 命令超过 260 个字符；请缩短 lbctl 和部署路径",
		"unknown native service manager %q":                                               "未知原生服务管理器 %q", "unknown native service manager": "未知原生服务管理器",
		"process manager does not support autostart; configure a platform service manager": "process 管理方式不支持自启；请配置平台服务管理器",
		"save autostart: %w; rollback: %v":                                                 "保存自启：%w；回滚：%v",
		"autostart only modifies lbctl-generated Compose files: %w":                        "自启只能修改 lbctl 生成的 Compose 文件：%w",
		"autostart: %w; restore Compose: %v; restore policy: %v":                           "自启：%w；恢复 Compose：%v；恢复策略：%v",
		"native logs only support --service langbot":                                       "原生日志仅支持 --service langbot",
		"native logs do not support --since; use --tail":                                   "原生日志不支持 --since；请使用 --tail",
		"invalid data directory":                                                           "数据目录无效", "native supervisor did not start; inspect %s": "原生守护进程未启动；请查看 %s",
		"not a native deployment": "不是原生部署", "native instance already running": "原生实例已在运行",
		"unsupported platform %s": "不支持的平台 %s", "uv lock: %v": "uv 锁：%v", "uv checksum mismatch": "uv 校验和不匹配", "%s: no data received for %s": "%s：%s 内未收到数据",
		"uv executable absent from archive": "压缩包中没有 uv 可执行文件", "invalid stable version": "稳定版本号无效",
		"unsupported extra %s": "不支持的可选依赖 %s", "LangBot wheel not found on PyPI": "PyPI 上未找到 LangBot wheel",
		"unknown autostart action": "未知自启操作", "--name and --dir are mutually exclusive": "--name 与 --dir 不能同时使用",
		"invalid port": "端口无效", "unsupported --extra: ": "不支持的 --extra：",
		"deployment already exists; use upgrade or another --name": "部署已存在；请使用 upgrade 或其他 --name",
		"invalid image repository":                                 "镜像仓库无效", "no stable Core release": "没有稳定 Core 版本",
		"cannot resolve latest Core release: %v": "无法获取最新 Core 版本：%v", "download exceeds size limit": "下载超过大小限制",
		"native process exited before becoming healthy; inspect logs": "原生进程在健康检查通过前退出；请查看日志",
		"cannot back up symlink %s":                                   "无法备份符号链接 %s", "target version must be newer": "目标版本必须高于当前版本",
		"upgrade failed: %v; rollback stop failed: %w": "升级失败：%v；回滚停止失败：%w",
		"rollback data move: %w":                       "回滚数据移动：%w", "rollback data restore: %w": "回滚数据恢复：%w",
		"rollback start: %w": "回滚启动：%w", "rollback health: %w": "回滚健康检查：%w",
		"upgrade failed; previous version and data restored: %w":        "升级失败；原版本与数据已恢复：%w",
		"autostart is local; use --name or --dir, not connection flags": "自启为本机操作；请使用 --name 或 --dir，不接受连接参数",
		"invalid lbctl release version":                                 "lbctl 发行版本无效", "unsupported update platform": "不支持在此平台更新",
		"release checksum absent for %s": "发行包缺少 %s 的校验和", "lbctl checksum mismatch": "lbctl 校验和不匹配",
		"driver must be native or docker": "部署方式必须为 native 或 docker", "port must be 1-65535": "端口必须在 1-65535 之间",
		"select 1 or 2": "请选择 1 或 2", "no free port": "没有可用端口", "choose another --port": "请使用 --port 指定其他端口",
		"output failed": "输出失败", "Downloading": "正在下载", "Installing": "正在安装", "from": "来源",
		"Retrying LangBot wheel from PyPI; dependencies use": "重试从 PyPI 下载 LangBot wheel；依赖使用",
		"LangBot is not installed in this instance's Python environment (the installation was probably interrupted); rerun the same lbctl install command to resume": "实例的 Python 环境中没有安装 LangBot（安装过程可能被中断）；请重新运行同一条 lbctl install 命令继续安装",
		"Files were temporarily locked; retrying":                          "文件被临时占用（可能是杀毒软件扫描），正在重试",
		"Run loginctl enable-linger to keep LangBot running after logout.": "运行 loginctl enable-linger 可在退出登录后继续运行 LangBot。",
		"Windows temporarily removes the startup entry while an instance is stopped. If startup is enabled in the deployment settings, starting the instance restores it.": "Windows 在实例停止期间会暂时移除启动项。如果部署设置中已开启自启，启动实例时会恢复启动项。",
		"log_path": "安装日志", "runtime_log_path": "运行日志", "dir": "实例目录",
	} {
		chinese[key] = value
		if _, exists := english[value]; !exists {
			english[value] = key
		}
	}
}
