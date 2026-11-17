package i18n

func init() {
	for key, value := range map[string]string{
		"Local context changed during authentication":                          "鉴权期间本地 context 已发生变化",
		"Unable to configure local API authentication":                         "无法配置本地 API 鉴权",
		"Local API credential unavailable; run lbctl restart":                  "本地 API 凭据不可用；请运行 lbctl restart",
		"Automatic API authentication requires Core v4.10.3 or newer; skipped": "自动 API 鉴权需要 Core v4.10.3 或更新版本；已跳过",
		"Local API key configured; run lbctl restart to activate it":           "本地 API Key 已配置，重启后生效：lbctl restart",
		"Local context endpoint differs from deployment":                       "本地 context 的地址与部署实例不一致",
		"No API key configured":                                                "未配置 API Key",
		"Reuse the existing installation options or use lbctl upgrade":         "请沿用现有安装参数，或使用 lbctl upgrade 升级",
		"Unable to repair local deployment":                                    "无法修复本地部署",
		"Print the selected API key explicitly":                                "主动查看所选实例的 API Key",
		"Printing an API key requires --yes":                                   "查看 API Key 需要指定 --yes",
		"Confirm printing the API key":                                         "确认打印 API Key",
		"CLI connected to this instance: use lbctl bot list directly":          "命令行已连接本实例：可直接使用 lbctl bot list 等命令",
	} {
		chinese[key] = value
		if _, exists := english[value]; !exists {
			english[value] = key
		}
	}
}
