package command

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func helpHint(cmd *cobra.Command) string {
	path := "lbctl"
	if cmd != nil {
		path = cmd.CommandPath()
	}
	return "请运行 " + path + " --help 查看用法"
}

// Use static metadata, never raw parser errors, which can include secret values.
func commandInputError(cmd *cobra.Command) error {
	if cmd == nil {
		return result.New("input", "参数无效；"+helpHint(cmd))
	}
	missing := []string{}
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		if required := flag.Annotations[cobra.BashCompOneRequiredFlag]; len(required) > 0 && required[0] == "true" && !flag.Changed {
			missing = append(missing, "--"+flag.Name)
		}
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return result.New("input", "缺少必需选项 "+strings.Join(missing, "、")+"；"+helpHint(cmd))
	}
	return result.New("input", "参数无效；"+helpHint(cmd))
}

func commandFlagError(cmd *cobra.Command, cause error) error {
	message := "未知选项或选项格式错误"
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		name := "--" + flag.Name
		if cause.Error() == "flag needs an argument: "+name || (flag.Shorthand != "" && cause.Error() == "flag needs an argument: -"+flag.Shorthand) {
			message = name + " 需要提供值"
		} else if strings.Contains(cause.Error(), `for "`+name+`" flag:`) {
			message = name + " 的值无效（类型：" + flag.Value.Type() + "）"
		}
	})
	return result.New("input", message+"；"+helpHint(cmd))
}

func configureHumanHelp(root *cobra.Command) {
	root.SetUsageTemplate(strings.NewReplacer(
		".LocalFlags.FlagUsages", "humanFlags . false", ".InheritedFlags.FlagUsages", "humanFlags . true",
		"Usage:", "用法：", "Aliases:", "别名：", "Examples:", "示例：",
		"Available Commands:", "可用命令：", "Additional Commands:", "其他命令：",
		"Global Flags:", "全局选项：", "Flags:", "选项：",
		"Additional help topics:", "其他帮助主题：",
		`Use "{{.CommandPath}} [command] --help" for more information about a command.`,
		`运行 "{{.CommandPath}} <命令> --help" 查看该命令的帮助。`,
		"[command]", "<命令>",
	).Replace(root.UsageTemplate()))
	root.SetHelpCommand(&cobra.Command{
		Use: "help [command]", Short: "查看命令帮助", Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			target, remaining, err := root.Find(args)
			if err != nil || len(remaining) != 0 {
				return result.New("input", "未知帮助主题；"+helpHint(root))
			}
			return target.Help()
		},
	})
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		cmd.InitDefaultHelpFlag()
		if flag := cmd.Flags().Lookup("help"); flag != nil {
			flag.Usage = "查看此命令的帮助"
		}
		switch cmd.Name() {
		case "help":
			cmd.Short = "查看命令帮助"
			cmd.Long = "查看任意命令的帮助，例如 lbctl help bot update。"
		case "completion":
			cmd.Short = "生成 shell 自动补全脚本"
			cmd.Long = "生成指定 shell 的自动补全脚本。"
		case "whoami":
			cmd.Short = "查看当前实例、Workspace 和权限"
		case "capabilities":
			cmd.Short = "查看 Core 声明的操作能力"
		}
		if cmd.Parent() != nil && cmd.Parent().Name() == "completion" {
			cmd.Short = "生成 " + cmd.Name() + " 自动补全脚本"
			cmd.Long = cmd.Short + "。将输出保存并加载到对应 shell 中。"
		}
		if original := cmd.Args; original != nil {
			cmd.Args = func(command *cobra.Command, args []string) error {
				if err := original(command, args); err != nil {
					var typed *result.Error
					if errors.As(err, &typed) {
						return err
					}
					message := "位置参数数量不正确"
					required := []string{}
					for _, field := range strings.Fields(command.Use)[1:] {
						if strings.HasPrefix(field, "<") && strings.HasSuffix(field, ">") {
							required = append(required, field)
						}
					}
					if len(args) < len(required) {
						message = "缺少位置参数 " + strings.Join(required[len(args):], " ")
					}
					return result.New("input", message+"；用法："+command.UseLine()+"；"+helpHint(command))
				}
				return nil
			}
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
}

func selectedConfigPath(deps Dependencies, flags *globalFlags) (string, error) {
	if flags.config != "" {
		return flags.config, nil
	}
	if path, exists := deps.LookupEnv("LANGBOT_CONFIG"); exists {
		if strings.TrimSpace(path) == "" {
			return "", result.New("input", "LANGBOT_CONFIG 不能为空")
		}
		return path, nil
	}
	return deps.DefaultConfigPath()
}

var helpGroups = []struct {
	title string
	names []string
}{
	{"连接与诊断", []string{"context", "status", "doctor", "whoami", "capabilities"}},
	{"资源与内容", []string{"bot", "pipeline", "provider", "model", "knowledge-base", "knowledge-engine", "knowledge-parser", "plugin", "skill", "mcp-server"}},
	{"运行与排查", []string{"monitoring", "sandbox", "task", "logs"}},
	{"本机部署", []string{"install", "adopt", "start", "stop", "restart", "upgrade", "uninstall"}},
	{"工具与自动化", []string{"schema", "api", "raw", "completion", "version", "help"}},
}

func configureExperience(root *cobra.Command, deps Dependencies, flags *globalFlags) {
	contextNames := func(cmd *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		path, err := selectedConfigPath(deps, flags)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		file, err := (config.Store{Path: path}).Load()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := []string{}
		for name := range file.Contexts {
			if strings.HasPrefix(name, prefix) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	_ = root.RegisterFlagCompletionFunc("context", contextNames)
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		name := commandPath(cmd)
		if schemaMetaByName[name].SupportsDryRun {
			if cmd.Long == "" {
				cmd.Long = cmd.Short
			}
			cmd.Long += "\n--dry-run 只执行该操作声明的检查，不写入；预检通过不代表业务校验或实际执行成功。"
		}
		configureListFilter(cmd, flags)
		if example, exists := commandExamples[name]; exists {
			cmd.Example = example
		}
		if strings.HasPrefix(name, "context.") && name != "context.add" && name != "context.list" {
			cmd.ValidArgsFunction = contextNames
		}
		for _, flag := range schemaFlagsFor(cmd, schemaMetaByName[name]) {
			if len(flag.Enum) > 0 {
				values := append([]string(nil), flag.Enum...)
				_ = cmd.RegisterFlagCompletionFunc(flag.Name, func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
					matches := []string{}
					for _, v := range values {
						if strings.HasPrefix(v, prefix) {
							matches = append(matches, v)
						}
					}
					return matches, cobra.ShellCompDirectiveNoFileComp
				})
			}
		}
		if name == "task.get" {
			original := cmd.Args
			cmd.Args = func(c *cobra.Command, args []string) error {
				if err := original(c, args); err != nil {
					return err
				}
				if _, err := strconv.ParseUint(args[0], 10, 64); err != nil {
					return result.New("input", "task ID 必须为非负整数；请运行 lbctl task list 查看任务 ID")
				}
				return nil
			}
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
	for index, group := range helpGroups {
		id := strconv.Itoa(index)
		root.AddGroup(&cobra.Group{ID: id, Title: group.title + "："})
		for _, cmd := range root.Commands() {
			for _, name := range group.names {
				if cmd.Name() == name {
					cmd.GroupID = id
				}
			}
		}
	}
	root.Long = "LangBot 服务管理 CLI。默认输出便于人工阅读；agent/脚本使用 -o json 或 -o yaml。\n连接已有实例先看 context list 和 status；本机部署的启停操作需要先 adopt。"
}

func init() { cobra.AddTemplateFunc("humanFlags", humanFlags) }
func humanFlags(cmd *cobra.Command, inherited bool) string {
	set := cmd.LocalFlags()
	if inherited {
		set = cmd.InheritedFlags()
	}
	meta := schemaMetaByName[commandPath(cmd)]
	view := pflag.NewFlagSet("help", pflag.ContinueOnError)
	set.VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || meta.FlagApplicability[flag.Name].Status == "rejected" {
			return
		}
		copy := *flag
		if meta.RequiredFlags[flag.Name] {
			copy.Usage += "（必填）"
		}
		if condition := meta.FlagApplicability[flag.Name]; condition.Status == "conditional" {
			copy.Usage += "（" + condition.Reason + "）"
		}
		view.AddFlag(&copy)
	})
	return view.FlagUsages()
}

func flagEnum(command, flag string) []string {
	switch flag {
	case "output":
		return []string{"json", "yaml", "yml"}
	case "profile":
		if command == "install" || command == "adopt" {
			return []string{"basic", "all"}
		}
	case "service":
		if command == "logs" {
			return []string{"langbot", "langbot_plugin_runtime", "langbot_box"}
		}
	case "type":
		if strings.HasPrefix(command, "model.") || command == "provider.scan-models" {
			return []string{"llm", "embedding", "rerank"}
		}
	case "active":
		return []string{"true", "false"}
	}
	return nil
}

var commandExamples = map[string]string{
	"context.list":   "  lbctl context list\n  lbctl context list --discover\n  lbctl --config ./config.yaml context list",
	"context.show":   "  lbctl context show\n  lbctl context show production",
	"context.add":    "  lbctl context add local --endpoint http://127.0.0.1:5300 --api-key-env LANGBOT_API_KEY\n  lbctl --context local whoami\n  lbctl context update local --expect-workspace <workspace-id>\n  lbctl context use local",
	"context.use":    "  lbctl context use local\n  lbctl status\n  lbctl bot list",
	"schema":         "  lbctl schema --summary\n  lbctl schema --command bot\n  lbctl schema --command 'bot update'",
	"bot.list":       "  lbctl bot list\n  lbctl bot list --filter LangBot\n  lbctl bot list -o json",
	"bot.get":        "  lbctl bot get <bot-id>\n  # 根据 use_pipeline_uuid 查看关联 Pipeline\n  lbctl pipeline get <pipeline-id>",
	"bot.update":     "  # request.yaml 的最小示例：{\"name\":\"my-bot\"}\n  lbctl bot update <bot-id> --file request.yaml --dry-run\n  lbctl bot update <bot-id> --file request.yaml\n  # 修改 adapter_config 前先 get，保留仍需使用的配置；*** 为脱敏值。",
	"pipeline.apply": "  # 更新示例：{\"uuid\":\"<pipeline-id>\",\"name\":\"my-pipeline\"}\n  # 无 uuid 时创建；config 字段以目标 Core 版本为准。\n  lbctl pipeline apply --file pipeline.yaml --dry-run\n  lbctl pipeline apply --file pipeline.yaml",
	"model.list":     "  lbctl model list --filter deepseek\n  lbctl model list --type llm --provider <provider-id>\n  lbctl model list -o json",
	"task.get":       "  lbctl task get 42\n  lbctl task get 42 --wait --wait-timeout 5m",
}
