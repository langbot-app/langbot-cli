package i18n

func init() {
	for key, value := range map[string]string{
		"LangBot · Production-grade platform for building agentic IM bots (open source)":                                             "LangBot · 生产级 AI 即时通信机器人开发平台（开源）",
		"Connect LLMs, agents, knowledge bases and plugins to QQ, WeChat/WeCom, Feishu, DingTalk, Telegram, Discord, Slack and more": "把大模型、Agent、知识库与插件接入 QQ、微信/企业微信、飞书、钉钉、Telegram、Discord、Slack 等平台",
		"Website https://langbot.app · Docs https://docs.langbot.app · Plugins https://space.langbot.app":                            "官网 https://langbot.app · 文档 https://docs.langbot.app · 插件市场 https://space.langbot.app",
		"Deploy:    lbctl install · upgrade · uninstall":                                                                             "部署：lbctl install · upgrade · uninstall",
		"Manage:    lbctl status · start · stop · restart · logs":                                                                    "管理：lbctl status · start · stop · restart · logs",
		"Configure: lbctl context · model · bot · pipeline":                                                                          "配置：lbctl context · model · bot · pipeline",
		"All commands: lbctl --help":                    "全部命令：lbctl --help",
		"Cloudflare exit-IP detection; global fallback": "Cloudflare 出口 IP 检测；失败回退 global",
		"Saved deployment settings":                     "已保存的部署设置", "Default deployment settings": "默认部署设置",
		"Region: %s (%s)": "网络区域：%s（%s）", "Instance: %s · Directory: %s · Port: %d": "实例：%s · 目录：%s · 端口：%d",
		"1. Open WebUI and create an administrator account":                          "1. 打开 WebUI 创建管理员账号",
		"2. Add a model (LangBot Space includes free credits)":                       "2. 添加模型（可使用 LangBot Space 赠送的额度）",
		"3. Connect a bot platform":                                                  "3. 接入一个机器人平台",
		"Prefer not to self-host? https://cloud.langbot.app":                         "不想自己部署？可使用云版本：https://cloud.langbot.app",
		"Commands: lbctl status · logs · stop · start · upgrade · autostart disable": "常用命令：lbctl status · logs · stop · start · upgrade · autostart disable",
		"Uptime": "运行时长", "Not reported": "未提供", "Instance": "实例", "Directory": "目录",
		"Startup": "自启", "Enabled": "已开启", "Disabled": "已关闭",
	} {
		chinese[key] = value
		if _, exists := english[value]; !exists {
			english[value] = key
		}
	}
}
