package command

import (
	"net/http"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

func sandboxGet(cmd *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags, path string) error {
	return emitCall(deps, flags, func() (app.Result, error) {
		return service.APIRequest(cmd.Context(), http.MethodGet, path, nil, connectionOptions(flags))
	})
}
func newSandboxCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	root := &cobra.Command{Use: "sandbox", Short: "查询与管理当前 Workspace sandbox", Args: cobra.NoArgs}
	for _, kind := range []string{"status", "sessions", "errors"} {
		root.AddCommand(&cobra.Command{Use: kind, Short: "查询 sandbox " + kind, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return sandboxGet(cmd, service, deps, flags, "/api/v1/box/"+kind)
		}})
	}
	root.AddCommand(newSandboxConfigCommand(service, deps, flags))
	return root
}

func newSandboxConfigCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	root := &cobra.Command{Use: "config", Short: "管理工作区沙箱后端、E2B 连接和资源限制", Args: cobra.NoArgs}
	var refresh bool
	get := &cobra.Command{Use: "get", Short: "查询已保存、生效的配置及后端能力", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.SandboxConfig(cmd.Context(), "get", nil, refresh, false, false, connectionOptions(flags))
			})
		}}
	get.Flags().BoolVar(&refresh, "refresh", false, "刷新服务端后端可用性，不创建沙箱")
	root.AddCommand(get)
	var resetDryRun, resetYes bool
	reset := &cobra.Command{Use: "reset", Short: "清除工作区沙箱覆盖，恢复部署默认配置", Args: cobra.NoArgs,
		Long:    "清除工作区保存的后端、E2B 连接及凭据和资源限制，需要 --yes。后续任务继承部署默认配置，已有任务与 MCP 连接保留配置快照。",
		Example: "  lbctl sandbox config reset --dry-run\n  lbctl sandbox config reset --yes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.SandboxConfig(cmd.Context(), "reset", nil, false, resetDryRun, resetYes, connectionOptions(flags))
			})
		}}
	reset.Flags().BoolVar(&resetYes, "yes", false, "确认清除工作区的后端、E2B 凭据和资源限制覆盖")
	reset.Flags().BoolVar(&resetDryRun, "dry-run", false, "仅检查前置条件，保留当前配置")
	root.AddCommand(reset)
	for _, action := range []string{"update", "check", "test"} {
		var file, keyEnv string
		var dryRun, yes bool
		cmd := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			var body map[string]any
			var err error
			if action == "update" {
				body, err = loadRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
			} else {
				body, err = loadOptionalRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
			}
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			if cmd.Flags().Changed("e2b-api-key-env") {
				key, found := deps.LookupEnv(keyEnv)
				if !found || strings.TrimSpace(key) == "" {
					return emitCommand(deps, flags, app.Result{}, result.New("input", "E2B API Key 环境变量未设置或为空"))
				}
				e2b := map[string]any{}
				if raw := body["e2b"]; raw != nil {
					var ok bool
					e2b, ok = raw.(map[string]any)
					if !ok {
						return emitCommand(deps, flags, app.Result{}, result.New("input", "e2b 必须是对象"))
					}
				}
				if _, exists := e2b["api_key"]; exists {
					return emitCommand(deps, flags, app.Result{}, result.New("input", "不能同时通过请求体 api_key 和 --e2b-api-key-env 提供 E2B 密钥"))
				}
				e2b["api_key"], body["e2b"] = strings.TrimSpace(key), e2b
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.SandboxConfig(cmd.Context(), action, body, false, dryRun, yes, connectionOptions(flags))
			})
		}}
		switch action {
		case "update":
			cmd.Short = "保存工作区沙箱配置，回读验证"
			cmd.Long = "保存后端、E2B 连接和资源限制，需要 --yes；只影响新任务，已有任务与 MCP 连接保留配置快照。省略的 E2B 密钥由 Core 保留；更换地址时是否需要新密钥由 Core 校验。"
			cmd.Example = "  # sandbox.yaml：{\"backend\":\"docker\",\"limits\":{\"timeout_sec\":60}}\n  lbctl sandbox config update --file sandbox.yaml --dry-run\n  lbctl sandbox config update --file sandbox.yaml --yes"
		case "check":
			cmd.Short = "检查 E2B 草稿连接，不保存或创建沙箱"
			cmd.Long = "使用草稿或当前 E2B 配置检查连接，不保存、不创建沙箱。草稿请求格式为 {e2b:{api_url,sandbox_url,template}}；密钥可用 --e2b-api-key-env 注入。"
			cmd.Example = "  lbctl sandbox config check\n  lbctl sandbox config check --file e2b.yaml --e2b-api-key-env E2B_API_KEY"
		case "test":
			cmd.Short = "显式创建、执行并清理临时 E2B 沙箱"
			cmd.Long = "测试消耗 E2B 配额并可能产生费用，需要 --yes。由 Core 创建临时沙箱、执行验证命令并删除；不保存配置。测试或清理失败返回非零退出码并保留诊断。可用 --timeout 2m 留出创建与清理时间；超时后先核查服务端状态，不要直接重试。"
			cmd.Example = "  lbctl --timeout 2m sandbox config test --file e2b.yaml --e2b-api-key-env E2B_API_KEY --yes"
		}
		cmd.Flags().StringVar(&file, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取；check/test 可省略")
		cmd.Flags().StringVar(&keyEnv, "e2b-api-key-env", "", "从指定环境变量读取 E2B API Key，不写入 CLI context")
		if action != "check" {
			cmd.Flags().BoolVar(&yes, "yes", false, "确认保存配置或执行可能计费的临时沙箱测试")
			cmd.Flags().BoolVar(&dryRun, "dry-run", false, "仅检查前置条件，不保存、不执行临时沙箱测试")
		}
		root.AddCommand(cmd)
	}
	return root
}
