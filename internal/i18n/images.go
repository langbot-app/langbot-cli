package i18n

func init() {
	for key, value := range map[string]string{
		"Image repository %s (%s, digest verified)": "镜像源 %s（%s，摘要已校验）",
		"Image digest verification or pull failed":  "镜像摘要校验或拉取失败",
		"%s (%d ms)":                           "%s（%d 毫秒）",
		"Image registry authentication failed": "镜像源认证失败",
		"Image registry unavailable":           "镜像源不可用",
		"Image registry available":             "镜像源可用",
		"Unable to resolve a trusted image digest; check network or proxy and retry": "无法从官方镜像源获取可信摘要；请检查网络或代理后重试",
		"Invalid image manifest":                                        "镜像清单无效",
		"Image digest verification failed":                              "镜像摘要校验失败",
		"Explicit image repository":                                     "显式指定",
		"Saved image repository":                                        "沿用已保存的镜像源",
		"Docker Hub available":                                          "Docker Hub 可用",
		"Automatic image repository":                                    "自动选择",
		"Official fallback image repository":                            "官方镜像源回退",
		"Image pull failed":                                             "镜像拉取失败",
		"Image pull or digest verification failed for all repositories": "所有镜像源拉取或摘要校验均失败",
		"Image repository %s (%s)":                                      "镜像源 %s（%s）",
		"Image repository %s (%s, %d ms, digest verified)":              "镜像源 %s（%s，延迟 %d 毫秒，摘要已校验）",
		"Docker image repositories":                                     "Docker 镜像源",
		"Selected image repository: %s":                                 "选中的镜像源：%s",
		"Image source changed: %s → %s":                                 "镜像源已切换：%s → %s",
	} {
		chinese[key] = value
		if _, exists := english[value]; !exists {
			english[value] = key
		}
	}
}
