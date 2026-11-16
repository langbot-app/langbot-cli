package i18n

func init() {
	for key, value := range map[string]string{
		"Native deployment ports are available":                             "原生部署端口可用",
		"Start a managed local deployment":                                  "启动本机受管部署",
		"Stop a managed local deployment":                                   "停止本机受管部署",
		"Restart a managed local deployment":                                "重启本机受管部署",
		"Local instance name":                                               "本机实例名称",
		"Local deployment directory":                                        "本机部署目录",
		"Also remove lbctl-managed data directories":                        "同时清理 lbctl 受管数据目录",
		"Confirm uninstall":                                                 "确认卸载",
		"Free conflicting ports, or change the HTTP port with --port":       "先释放冲突端口，或用 --port 更换 HTTP 端口",
		"Free ports 5401/5410, or use --driver docker":                      "先释放 5401/5410 冲突端口，或改用 --driver docker",
		"%s port %d conflicts with %s; change the HTTP port with --port":    "%s 端口 %d 与 %s 端口冲突；请用 --port 更换 HTTP 端口",
		"Stop the conflicting service, or change the HTTP port with --port": "请停止冲突服务，或用 --port 更换 HTTP 端口",
		"Core 4.11.x native deployments require exclusive ports 5401 and 5410; stop conflicting services or use Docker for multiple instances": "Core 4.11.x 原生部署需要独占 5401 和 5410，请停止冲突服务，或使用 Docker 部署多实例",
		"%s port %d is unavailable: %v; %s":  "%s 端口 %d 不可用：%v；%s",
		"Installation plan: LangBot ":        "安装计划：LangBot ",
		"Installation failed\nFailed step: ": "安装失败\n失败步骤：",
		"Log":                                "日志",
		"Runtime log":                        "运行日志",
		"Retry":                              "重试",
		"Rollback":                           "回滚",
		"Preflight made no file changes.":    "预检阶段没有改动任何文件。",
		"Preflight: %d passed, %d failed (ports)\n": "预检：%d 项通过，%d 项失败（端口）\n",
	} {
		chinese[key] = value
		if _, exists := english[value]; !exists {
			english[value] = key
		}
		originals[key] = value
	}
	for key, value := range map[string]string{
		"--name and --dir are mutually exclusive":                                                                      "--name 与 --dir 不能同时使用",
		"multiple local deployments have this name; use --dir":                                                         "多个本机部署使用此名称；请使用 --dir",
		"deployment is uninstalled; reinstall before changing autostart":                                               "部署已卸载；请重新安装后再修改自启",
		"--name/--dir cannot be combined with connection flags":                                                        "--name/--dir 不能与连接参数同时使用",
		"unsupported local deployment driver":                                                                          "不支持的本机部署方式",
		"deployment already exists (%s, %s, port %d); reuse its installation options or use lbctl start --endpoint %s": "部署已存在（%s，%s，端口 %d）；请复用其安装参数或执行 lbctl start --endpoint %s",
		"save autostart: %w":                                       "保存自启：%w",
		"save autostart: %w; rollback: %v":                         "保存自启：%w；回滚：%v",
		"%w; restore Compose: %v":                                  "%w；恢复 Compose：%v",
		"%w; restore policy: %v":                                   "%w；恢复策略：%v",
		"autostart: %w":                                            "自启：%w",
		"uv lock: %w":                                              "uv 锁：%w",
		"port %d is already accepting connections on %s":           "端口 %d 已在 %s 接受连接",
		"deployment is uninstalled; reinstall this instance first": "部署已卸载；请先重新安装此实例",
		"Directory: %s":                                            "目录：%s",
		"Instance: %s":                                             "实例：%s",
		"Endpoint: %s":                                             "端点：%s",
		"Compose project: %s":                                      "Compose project：%s",
		"Instance UUID: %s":                                        "instance UUID：%s",
		"Registration conflict: %s. Continue using the existing instance: %s; or uninstall it (data preserved) and reinstall: %s": "登记冲突：%s。继续使用原实例：%s；或清理旧实例后重装（数据保留）：%s",
	} {
		chinese[key] = value
		if _, exists := english[value]; !exists {
			english[value] = key
		}
	}
}
