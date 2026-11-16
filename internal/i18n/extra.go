package i18n

func init() {
	for key, value := range map[string]string{
		"Verify stopped services": "确认服务已停止", "Verify backup": "验证备份",
		"Check backup prerequisites": "检查备份前置条件", "Verify deployment assets": "验证部署资产",
		":": "：",
		"Output language: en or zh-Hans (auto-detected by default)": "输出语言：en 或 zh-Hans（默认自动检测）",
		"Show full subprocess output":                               "显示完整子进程输出",
		"Resolve version":                                           "确定版本", "Validate target": "检查目标", "Check ports": "检查端口",
		"Save deployment record": "保存部署记录", "Download uv": "下载 uv", "Install Python 3.12": "安装 Python 3.12",
		"Create virtual environment": "创建虚拟环境", "Install LangBot packages": "安装 LangBot 依赖",
		"Register startup": "注册自启", "Start service": "启动服务", "Stop service": "停止服务",
		"Check service health": "检查服务健康", "Remove startup": "删除自启", "Remove data": "清理数据",
		"Remove virtual environments": "删除虚拟环境", "Remove containers": "删除容器", "Check prerequisites": "检查前置条件",
		"Pull images": "拉取镜像", "Start containers": "启动容器", "Stop containers": "停止容器", "Restart containers": "重启容器",
		"Resolve deployment": "定位部署", "Enable startup": "开启自启", "Disable startup": "关闭自启", "Inspect startup": "查看自启",
		"Back up data": "备份数据", "Download update": "下载更新", "Replace executable": "替换可执行文件", "Write deployment assets": "写入部署资产",
		"Windows reports the startup entry as inactive while the instance is stopped; start the instance to restore it.": "Windows 在实例停止期间将启动项显示为未激活；启动实例后会恢复。",
	} {
		chinese[key] = value
		if _, exists := english[value]; !exists {
			english[value] = key
		}
	}
}
