package command

import (
	"errors"
	"sort"
	"strings"

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
