package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/output"
	"github.com/langbot-app/langbot-cli/internal/requestbody"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	In                io.Reader
	Out               io.Writer
	Err               io.Writer
	LookupEnv         func(string) (string, bool)
	DefaultConfigPath func() (string, error)
	Transport         http.RoundTripper
	Version           string
	Commit            string
	BuildDate         string
}

type globalFlags struct {
	config      string
	configSet   bool
	context     string
	endpoint    string
	timeout     string
	apiKeyStdin bool
	output      string
	contextSet  bool
	endpointSet bool
	timeoutSet  bool
	exitCode    int
}

func Execute(ctx context.Context, args []string, deps Dependencies) int {
	deps = normalizeDependencies(deps)
	flags := &globalFlags{output: output.FormatJSON}
	service := app.New(app.Dependencies{
		In: deps.In, LookupEnv: deps.LookupEnv, DefaultConfigPath: func() (string, error) {
			if flags.config != "" {
				return flags.config, nil
			}
			return deps.DefaultConfigPath()
		},
		Transport: deps.Transport, Version: deps.Version, Commit: deps.Commit, BuildDate: deps.BuildDate,
	})
	root := newRoot(deps, flags, service)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		var typed *result.Error
		if !errors.As(err, &typed) {
			err = result.New("input", "参数无效，请使用 --help 查看用法")
		}
		return emit(deps, flags.output, app.Result{}, err)
	}
	return flags.exitCode
}

func normalizeDependencies(deps Dependencies) Dependencies {
	if deps.In == nil {
		deps.In = strings.NewReader("")
	}
	if deps.Out == nil {
		deps.Out = os.Stdout
	}
	if deps.Err == nil {
		deps.Err = os.Stderr
	}
	if deps.LookupEnv == nil {
		deps.LookupEnv = os.LookupEnv
	}
	if deps.DefaultConfigPath == nil {
		deps.DefaultConfigPath = config.DefaultPath
	}
	return deps
}

func newRoot(deps Dependencies, flags *globalFlags, service *app.Service) *cobra.Command {
	root := &cobra.Command{
		Use:           "lbctl",
		Short:         "LangBot 服务管理 CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return result.New("input", "未知命令或参数，请使用 --help 查看用法")
			}
			return cmd.Help()
		},
	}
	root.SetOut(deps.Out)
	root.SetErr(deps.Err)
	root.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return result.New("input", "参数无效，请使用 --help 查看用法")
	})
	persistent := root.PersistentFlags()
	persistent.StringVar(&flags.config, "config", "", "配置文件路径")
	persistent.StringVar(&flags.context, "context", "", "使用指定 context")
	persistent.StringVar(&flags.endpoint, "endpoint", "", "临时服务地址")
	persistent.StringVar(&flags.timeout, "timeout", "", "请求超时时间")
	persistent.BoolVar(&flags.apiKeyStdin, "api-key-stdin", false, "从 stdin 读取本次请求的 API Key")
	persistent.StringVarP(&flags.output, "output", "o", output.FormatJSON, "输出格式：json、table 或 yaml")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		flags.configSet = cmd.InheritedFlags().Changed("config")
		if flags.configSet && strings.TrimSpace(flags.config) == "" {
			return result.New("input", "config 不能为空")
		}
		flags.contextSet = cmd.InheritedFlags().Changed("context")
		flags.endpointSet = cmd.InheritedFlags().Changed("endpoint")
		flags.timeoutSet = cmd.InheritedFlags().Changed("timeout")
		if _, err := output.NormalizeFormat(flags.output); err != nil {
			return result.New("input", "输出格式无效，请使用 json、table 或 yaml")
		}
		return nil
	}
	root.AddCommand(newContextCommand(service, deps, flags))
	root.AddCommand(newStatusCommand(service, deps, flags))
	root.AddCommand(newVersionCommand(service, deps, flags))
	root.AddCommand(newRawCommand(service, deps, flags))
	root.AddCommand(newAPICommand(service, deps, flags))
	root.AddCommand(newIdentityCommand(service, deps, flags, "whoami"))
	root.AddCommand(newIdentityCommand(service, deps, flags, "capabilities"))
	root.AddCommand(newBotCommand(service, deps, flags))
	root.AddCommand(newPipelineCommand(service, deps, flags))
	root.AddCommand(newTaskCommand(service, deps, flags))
	root.AddCommand(newKnowledgeBaseCommand(service, deps, flags))
	root.AddCommand(newPluginCommand(service, deps, flags))
	root.AddCommand(newSkillCommand(service, deps, flags))
	root.AddCommand(newMCPServerCommand(service, deps, flags))
	return root
}

func newBotCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "bot", Short: "管理 Bot 资源", Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{
		Use: "list", Short: "列出 Bot", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.BotList(cmd.Context(), connectionOptions(flags))
			})
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "get <id>", Short: "读取指定 Bot", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.BotGet(cmd.Context(), args[0], connectionOptions(flags))
			})
		},
	})
	command.AddCommand(newBotCreateCommand(service, deps, flags))
	command.AddCommand(newBotUpdateCommand(service, deps, flags))
	command.AddCommand(newBotDeleteCommand(service, deps, flags))
	return command
}

func newBotCreateCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var file string
	var dryRun bool
	command := &cobra.Command{
		Use: "create", Short: "创建 Bot", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := loadRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.BotCreate(cmd.Context(), body, dryRun, connectionOptions(flags))
			})
		},
	}
	command.Flags().StringVar(&file, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "只检查目标和操作前提，不写入")
	return command
}

func newBotUpdateCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var file string
	var dryRun bool
	command := &cobra.Command{
		Use: "update <id>", Short: "更新 Bot", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := loadRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.BotUpdate(cmd.Context(), args[0], body, dryRun, connectionOptions(flags))
			})
		},
	}
	command.Flags().StringVar(&file, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "只检查目标和操作前提，不写入")
	return command
}

func newBotDeleteCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var yes bool
	var dryRun bool
	command := &cobra.Command{
		Use: "delete <id>", Short: "删除 Bot", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.BotDelete(cmd.Context(), args[0], yes, dryRun, connectionOptions(flags))
			})
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "确认删除")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "只检查目标和操作前提，不写入")
	return command
}

func newPipelineCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "pipeline", Short: "管理 Pipeline 资源", Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{
		Use: "list", Short: "列出 Pipeline", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PipelineList(cmd.Context(), connectionOptions(flags))
			})
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "get <id>", Short: "读取指定 Pipeline", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PipelineGet(cmd.Context(), args[0], connectionOptions(flags))
			})
		},
	})
	command.AddCommand(newPipelineApplyCommand(service, deps, flags))
	command.AddCommand(newPipelineCopyCommand(service, deps, flags))
	command.AddCommand(newPipelineDeleteCommand(service, deps, flags))
	return command
}

func newPipelineApplyCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var file string
	var dryRun bool
	command := &cobra.Command{
		Use: "apply", Short: "创建或更新 Pipeline", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := loadRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PipelineApply(cmd.Context(), body, dryRun, connectionOptions(flags))
			})
		},
	}
	command.Flags().StringVar(&file, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "只检查目标和操作前提，不写入")
	return command
}

func newPipelineCopyCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use: "copy <id>", Short: "复制 Pipeline", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PipelineCopy(cmd.Context(), args[0], dryRun, connectionOptions(flags))
			})
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "只检查目标和操作前提，不写入")
	return command
}

func newPipelineDeleteCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var yes bool
	var dryRun bool
	command := &cobra.Command{
		Use: "delete <id>", Short: "删除 Pipeline", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PipelineDelete(cmd.Context(), args[0], yes, dryRun, connectionOptions(flags))
			})
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "确认删除")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "只检查目标和操作前提，不写入")
	return command
}

func newTaskCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "task", Short: "查看异步任务", Args: cobra.NoArgs}
	var taskType, taskKind string
	list := &cobra.Command{
		Use: "list", Short: "列出异步任务", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.TaskList(cmd.Context(), taskType, taskKind, connectionOptions(flags))
			})
		},
	}
	list.Flags().StringVar(&taskType, "type", "", "按任务类型筛选")
	list.Flags().StringVar(&taskKind, "kind", "", "按任务 kind 筛选")
	command.AddCommand(list)
	var wait bool
	var pollInterval time.Duration
	var waitTimeout time.Duration
	get := &cobra.Command{
		Use: "get <id>", Short: "读取异步任务状态", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if wait && (pollInterval <= 0 || waitTimeout <= 0) {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "等待间隔和等待超时必须为正数"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				options := connectionOptions(flags)
				if !wait {
					return service.TaskGet(cmd.Context(), args[0], options)
				}
				return service.TaskWait(cmd.Context(), args[0], app.WaitOptions{
					Wait: true, PollInterval: pollInterval, WaitTimeout: waitTimeout,
				}, options)
			})
		},
	}
	get.Flags().BoolVar(&wait, "wait", false, "等待任务进入终态")
	get.Flags().DurationVar(&pollInterval, "poll-interval", time.Second, "任务轮询间隔")
	get.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "本地等待超时")
	command.AddCommand(get)
	return command
}

func newKnowledgeBaseCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "knowledge-base", Aliases: []string{"kb"}, Short: "管理知识库", Args: cobra.NoArgs}
	var filename string
	var parserPluginID string
	var dryRun bool
	var wait bool
	var pollInterval time.Duration
	var waitTimeout time.Duration
	ingest := &cobra.Command{
		Use: "ingest <knowledge-base-id>", Short: "上传文件并提交知识库入库", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(filename) == "" || filename == "-" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "--file 必须指定本地文件，不能使用 stdin"))
			}
			if dryRun && wait {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "--dry-run 不能与 --wait 同时使用"))
			}
			if wait && (pollInterval <= 0 || waitTimeout <= 0) {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "等待间隔和等待超时必须为正数"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.KnowledgeBaseIngest(cmd.Context(), args[0], filename, parserPluginID, dryRun, app.WaitOptions{
					Wait: wait, PollInterval: pollInterval, WaitTimeout: waitTimeout,
				}, connectionOptions(flags))
			})
		},
	}
	ingest.Flags().StringVar(&filename, "file", "", "要上传的本地文件")
	ingest.Flags().StringVar(&parserPluginID, "parser-plugin-id", "", "可选的解析器插件 ID")
	ingest.Flags().BoolVar(&dryRun, "dry-run", false, "检查前置条件但不上传或提交")
	ingest.Flags().BoolVar(&wait, "wait", false, "等待入库任务进入终态")
	ingest.Flags().DurationVar(&pollInterval, "poll-interval", time.Second, "任务轮询间隔")
	ingest.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "本地等待超时")
	command.AddCommand(ingest)
	return command
}

func newPluginCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: "管理 Plugin", Args: cobra.NoArgs}
	install := &cobra.Command{Use: "install", Short: "安装 Plugin", Args: cobra.NoArgs}
	newTaskFlags := func(cmd *cobra.Command, dryRun, wait *bool, pollInterval, waitTimeout *time.Duration) {
		cmd.Flags().BoolVar(dryRun, "dry-run", false, "只检查前置条件，不安装或升级")
		cmd.Flags().BoolVar(wait, "wait", false, "等待任务进入终态")
		cmd.Flags().DurationVar(pollInterval, "poll-interval", time.Second, "任务轮询间隔")
		cmd.Flags().DurationVar(waitTimeout, "wait-timeout", 5*time.Minute, "本地等待超时")
	}
	var githubFile string
	var githubDryRun, githubWait bool
	var githubPollInterval, githubWaitTimeout time.Duration
	github := &cobra.Command{Use: "github", Short: "从 GitHub 安装 Plugin", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := loadRequestBody(cmd.Context(), githubFile, flags.apiKeyStdin, deps.In)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			if err := validateWaitFlags(githubDryRun, githubWait, githubPollInterval, githubWaitTimeout); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PluginInstallGitHub(cmd.Context(), body, githubDryRun, githubWait, app.WaitOptions{Wait: githubWait, PollInterval: githubPollInterval, WaitTimeout: githubWaitTimeout}, connectionOptions(flags))
			})
		},
	}
	github.Flags().StringVar(&githubFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	newTaskFlags(github, &githubDryRun, &githubWait, &githubPollInterval, &githubWaitTimeout)
	install.AddCommand(github)

	var marketplaceFile string
	var marketplaceDryRun, marketplaceWait bool
	var marketplacePollInterval, marketplaceWaitTimeout time.Duration
	marketplace := &cobra.Command{Use: "marketplace", Short: "从 Marketplace 安装 Plugin", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := loadRequestBody(cmd.Context(), marketplaceFile, flags.apiKeyStdin, deps.In)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			if err := validateWaitFlags(marketplaceDryRun, marketplaceWait, marketplacePollInterval, marketplaceWaitTimeout); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PluginInstallMarketplace(cmd.Context(), body, marketplaceDryRun, marketplaceWait, app.WaitOptions{Wait: marketplaceWait, PollInterval: marketplacePollInterval, WaitTimeout: marketplaceWaitTimeout}, connectionOptions(flags))
			})
		},
	}
	marketplace.Flags().StringVar(&marketplaceFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	newTaskFlags(marketplace, &marketplaceDryRun, &marketplaceWait, &marketplacePollInterval, &marketplaceWaitTimeout)
	install.AddCommand(marketplace)

	var localFile string
	var localDryRun, localWait bool
	var localPollInterval, localWaitTimeout time.Duration
	local := &cobra.Command{Use: "local", Short: "从本地包安装 Plugin", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateWaitFlags(localDryRun, localWait, localPollInterval, localWaitTimeout); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PluginInstallLocal(cmd.Context(), localFile, localDryRun, localWait, app.WaitOptions{Wait: localWait, PollInterval: localPollInterval, WaitTimeout: localWaitTimeout}, connectionOptions(flags))
			})
		},
	}
	local.Flags().StringVar(&localFile, "file", "", "本地 Plugin 包文件")
	newTaskFlags(local, &localDryRun, &localWait, &localPollInterval, &localWaitTimeout)
	install.AddCommand(local)
	command.AddCommand(install)

	var upgradeDryRun, upgradeWait bool
	var upgradePollInterval, upgradeWaitTimeout time.Duration
	upgrade := &cobra.Command{Use: "upgrade <author> <name>", Short: "升级已安装 Plugin", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateWaitFlags(upgradeDryRun, upgradeWait, upgradePollInterval, upgradeWaitTimeout); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PluginUpgrade(cmd.Context(), args[0], args[1], upgradeDryRun, upgradeWait, app.WaitOptions{Wait: upgradeWait, PollInterval: upgradePollInterval, WaitTimeout: upgradeWaitTimeout}, connectionOptions(flags))
			})
		},
	}
	newTaskFlags(upgrade, &upgradeDryRun, &upgradeWait, &upgradePollInterval, &upgradeWaitTimeout)
	command.AddCommand(upgrade)
	return command
}

func newSkillCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "skill", Short: "管理 Skill", Args: cobra.NoArgs}
	install := &cobra.Command{Use: "install", Short: "安装 Skill", Args: cobra.NoArgs}
	var githubFile string
	var githubDryRun bool
	github := &cobra.Command{Use: "github", Short: "从 GitHub 安装 Skill", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := loadRequestBody(cmd.Context(), githubFile, flags.apiKeyStdin, deps.In)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.SkillInstallGitHub(cmd.Context(), body, githubDryRun, connectionOptions(flags))
			})
		},
	}
	github.Flags().StringVar(&githubFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	github.Flags().BoolVar(&githubDryRun, "dry-run", false, "使用服务端预览，不安装")
	install.AddCommand(github)

	var sourcePaths []string
	var uploadFile string
	var uploadDryRun bool
	upload := &cobra.Command{Use: "upload", Short: "从 ZIP 安装 Skill", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.SkillInstallUpload(cmd.Context(), uploadFile, sourcePaths, uploadDryRun, connectionOptions(flags))
			})
		},
	}
	upload.Flags().StringVar(&uploadFile, "file", "", "Skill ZIP 文件")
	upload.Flags().StringSliceVar(&sourcePaths, "source-path", nil, "Skill 包中的 source_paths，可重复指定")
	upload.Flags().BoolVar(&uploadDryRun, "dry-run", false, "使用服务端预览，不安装")
	install.AddCommand(upload)
	command.AddCommand(install)
	return command
}

func newMCPServerCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "mcp-server", Short: "测试 MCP Server", Args: cobra.NoArgs}
	var file string
	var dryRun, wait bool
	var pollInterval, waitTimeout time.Duration
	testCommand := &cobra.Command{Use: "test <server-name>", Short: "测试 MCP Server 连接", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := loadOptionalRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			if err := validateWaitFlags(dryRun, wait, pollInterval, waitTimeout); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.MCPServerTest(cmd.Context(), args[0], body, dryRun, wait, app.WaitOptions{Wait: wait, PollInterval: pollInterval, WaitTimeout: waitTimeout}, connectionOptions(flags))
			})
		},
	}
	testCommand.Flags().StringVar(&file, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取；省略时使用空对象")
	testCommand.Flags().BoolVar(&dryRun, "dry-run", false, "只检查前置条件，不执行测试")
	testCommand.Flags().BoolVar(&wait, "wait", false, "等待任务进入终态")
	testCommand.Flags().DurationVar(&pollInterval, "poll-interval", time.Second, "任务轮询间隔")
	testCommand.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "本地等待超时")
	command.AddCommand(testCommand)
	return command
}

func validateWaitFlags(dryRun, wait bool, pollInterval, waitTimeout time.Duration) error {
	if dryRun && wait {
		return result.New("input", "--dry-run 不能与 --wait 同时使用")
	}
	if wait && (pollInterval <= 0 || waitTimeout <= 0) {
		return result.New("input", "等待间隔和等待超时必须为正数")
	}
	return nil
}

func loadRequestBody(ctx context.Context, file string, apiKeyStdin bool, stdin io.Reader) (map[string]any, error) {
	if err := requestbody.ValidateSources(file, apiKeyStdin); err != nil {
		return nil, err
	}
	return requestbody.Load(ctx, file, stdin)
}

func loadOptionalRequestBody(ctx context.Context, file string, apiKeyStdin bool, stdin io.Reader) (map[string]any, error) {
	if strings.TrimSpace(file) == "" {
		return map[string]any{}, nil
	}
	return loadRequestBody(ctx, file, apiKeyStdin, stdin)
}

func connectionOptions(flags *globalFlags) app.CheckOptions {
	return app.CheckOptions{
		Context: flags.context, Endpoint: flags.endpoint, Timeout: flags.timeout,
		ContextSet: flags.contextSet, EndpointSet: flags.endpointSet,
		TimeoutSet: flags.timeoutSet, APIKeyStdin: flags.apiKeyStdin,
	}
}

func newContextCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	contextCommand := &cobra.Command{
		Use: "context", Short: "管理本地 context", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	contextCommand.AddCommand(newContextAdd(service, deps, flags))
	contextCommand.AddCommand(newContextUpdate(service, deps, flags))
	contextCommand.AddCommand(&cobra.Command{
		Use: "list", Short: "列出本地 context", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := rejectCredentialInput(flags); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) { return service.WithContext(cmd.Context()).ContextList() })
		},
	})
	contextCommand.AddCommand(newContextShow(service, deps, flags))
	contextCommand.AddCommand(newContextUse(service, deps, flags))
	contextCommand.AddCommand(newContextRemove(service, deps, flags))
	contextCommand.AddCommand(newContextCheck(service, deps, flags))
	return contextCommand
}

func newContextAdd(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var endpoint, apiKeyEnv, expected, timeout string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "新增本地 context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectCredentialInput(flags); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			if cmd.Flags().Changed("api-key-env") && strings.TrimSpace(apiKeyEnv) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "api-key-env 不能为空"))
			}
			if cmd.Flags().Changed("timeout") && strings.TrimSpace(timeout) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "timeout 不能为空"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.WithContext(cmd.Context()).ContextAdd(args[0], endpoint, apiKeyEnv, expected, timeout)
			})
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "服务地址")
	cmd.Flags().StringVar(&apiKeyEnv, "api-key-env", "", "API Key 环境变量名")
	cmd.Flags().StringVar(&expected, "expect-workspace", "", "预期 Workspace UUID")
	cmd.Flags().StringVar(&timeout, "timeout", "", "该 context 的默认请求超时时间")
	return cmd
}

func newContextUpdate(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var endpoint, apiKeyEnv, expected, timeout string
	var clearCredential, clearBinding bool
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "更新本地 context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectCredentialInput(flags); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			if cmd.Flags().Changed("api-key-env") && strings.TrimSpace(apiKeyEnv) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "api-key-env 不能为空"))
			}
			if cmd.Flags().Changed("timeout") && strings.TrimSpace(timeout) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "timeout 不能为空"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.WithContext(cmd.Context()).ContextUpdate(args[0], endpoint, apiKeyEnv, expected, timeout,
					cmd.Flags().Changed("endpoint"), cmd.Flags().Changed("api-key-env"),
					cmd.Flags().Changed("expect-workspace"), cmd.Flags().Changed("timeout"), clearCredential, clearBinding)
			})
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "服务地址")
	cmd.Flags().StringVar(&apiKeyEnv, "api-key-env", "", "API Key 环境变量名")
	cmd.Flags().StringVar(&expected, "expect-workspace", "", "预期 Workspace UUID")
	cmd.Flags().StringVar(&timeout, "timeout", "", "该 context 的默认请求超时时间")
	cmd.Flags().BoolVar(&clearCredential, "clear-credential", false, "清除已保存的凭据来源")
	cmd.Flags().BoolVar(&clearBinding, "clear-binding", false, "清除预期 Workspace 绑定")
	return cmd
}

func newContextShow(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use: "show <name>", Short: "查看 context 配置", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.WithContext(cmd.Context()).ContextShow(args[0], app.CheckOptions{
					Context: flags.context, Endpoint: flags.endpoint, Timeout: flags.timeout,
					ContextSet: flags.contextSet, EndpointSet: flags.endpointSet, TimeoutSet: flags.timeoutSet,
					APIKeyStdin: flags.apiKeyStdin,
				})
			})
		},
	}
}

func newContextUse(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use: "use <name>", Short: "设置后续命令的默认 context", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectCredentialInput(flags); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) { return service.WithContext(cmd.Context()).ContextUse(args[0]) })
		},
	}
}

func newContextRemove(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "remove <name>", Short: "删除本地 context", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectCredentialInput(flags); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) { return service.WithContext(cmd.Context()).ContextRemove(args[0], yes) })
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "确认删除")
	return cmd
}

func newContextCheck(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use: "check [name]", Short: "检查服务连接", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Check(cmd.Context(), app.CheckOptions{
					Name: name, All: all, Context: flags.context, Endpoint: flags.endpoint, Timeout: flags.timeout,
					ContextSet: flags.contextSet, EndpointSet: flags.endpointSet, TimeoutSet: flags.timeoutSet,
					APIKeyStdin: flags.apiKeyStdin,
				})
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "检查所有 context")
	return cmd
}

func newStatusCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "查看服务连接状态", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Status(cmd.Context(), app.CheckOptions{
					Context: flags.context, Endpoint: flags.endpoint, Timeout: flags.timeout,
					ContextSet: flags.contextSet, EndpointSet: flags.endpointSet, TimeoutSet: flags.timeoutSet,
					APIKeyStdin: flags.apiKeyStdin,
				})
			})
		},
	}
}

func newVersionCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var server bool
	cmd := &cobra.Command{
		Use: "version", Short: "查看 CLI 版本", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !server && hasConnectionOverride(flags) {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "离线 version 不接受连接覆盖参数"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Version(cmd.Context(), server, app.CheckOptions{
					Context: flags.context, Endpoint: flags.endpoint, Timeout: flags.timeout,
					ContextSet: flags.contextSet, EndpointSet: flags.endpointSet, TimeoutSet: flags.timeoutSet,
					APIKeyStdin: flags.apiKeyStdin,
				})
			})
		},
	}
	cmd.Flags().BoolVar(&server, "server", false, "同时查询服务端版本")
	return cmd
}

func newRawCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use: "raw [method path]", Short: "读取受控的基础服务信息", Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "raw 需要同时提供 method 和 path"))
			}
			if file == "-" && flags.apiKeyStdin {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "--file - 不能与 --api-key-stdin 同时使用"))
			}
			if cmd.Flags().Changed("file") {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "阶段 1A 的 raw 只支持无请求体的 GET"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				method, path := "GET", "/api/v1/system/info"
				if len(args) == 2 {
					method, path = args[0], args[1]
				}
				return service.RawInfo(cmd.Context(), app.CheckOptions{
					Context: flags.context, Endpoint: flags.endpoint, Timeout: flags.timeout,
					ContextSet: flags.contextSet, EndpointSet: flags.endpointSet, TimeoutSet: flags.timeoutSet,
					APIKeyStdin: flags.apiKeyStdin,
				}, method, path)
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "请求体输入；阶段 1A 不允许请求体")
	return cmd
}

func newAPICommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "api", Short: "调用已登记的 HTTP 操作", Args: cobra.NoArgs}
	get := &cobra.Command{
		Use: "get <path>", Short: "调用受控 GET 接口", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			operation, ok := api.ResolveOperation("GET", args[0])
			if !ok || operation.Method != http.MethodGet {
				return emitCommand(deps, flags, app.Result{}, result.New("incompatible", "GET 路径未登记"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.APIRequest(cmd.Context(), http.MethodGet, args[0], nil, connectionOptions(flags))
			})
		},
	}
	command.AddCommand(get)
	var file string
	post := &cobra.Command{
		Use: "post <path>", Short: "调用受控 POST 接口", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			operation, ok := api.ResolveOperation("POST", args[0])
			if !ok || !operation.ReadOnly {
				return emitCommand(deps, flags, app.Result{}, result.New("incompatible", "POST 路径未登记"))
			}
			body, err := loadOptionalRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.APIRequest(cmd.Context(), http.MethodPost, args[0], body, connectionOptions(flags))
			})
		},
	}
	post.Flags().StringVar(&file, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	command.AddCommand(post)
	return command
}

func newIdentityCommand(service *app.Service, deps Dependencies, flags *globalFlags, name string) *cobra.Command {
	return &cobra.Command{
		Use: name, Short: name + "（服务端发现）", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.WithContext(cmd.Context()).Identity(cmd.Context(), name, app.CheckOptions{
					Context: flags.context, Endpoint: flags.endpoint, Timeout: flags.timeout,
					ContextSet: flags.contextSet, EndpointSet: flags.endpointSet,
					TimeoutSet: flags.timeoutSet, APIKeyStdin: flags.apiKeyStdin,
				})
			})
		},
	}
}

func emitCommand(deps Dependencies, flags *globalFlags, value app.Result, err error) error {
	flags.exitCode = emit(deps, flags.output, value, err)
	return nil
}

func emitCall(deps Dependencies, flags *globalFlags, call func() (app.Result, error)) error {
	value, err := call()
	return emitCommand(deps, flags, value, err)
}

func emit(deps Dependencies, format string, value app.Result, callErr error) int {
	if _, err := output.NormalizeFormat(format); err != nil {
		format = output.FormatJSON
	}
	envelope := result.Envelope{OK: callErr == nil, Data: value.Data, Meta: value.Meta}
	if callErr != nil {
		envelope.Error = result.AsError(callErr)
	}
	if err := output.Render(deps.Out, envelope, format); err != nil {
		fmt.Fprintln(deps.Err, "输出失败")
		return 10
	}
	return result.ExitCode(callErr)
}

func rejectCredentialInput(flags *globalFlags) error {
	if flags.apiKeyStdin {
		return result.New("input", "该本地 context 命令不接受 --api-key-stdin")
	}
	if flags.endpointSet || flags.contextSet || flags.timeoutSet {
		return result.New("input", "该本地 context 命令不接受连接覆盖参数")
	}
	return nil
}

func hasConnectionOverride(flags *globalFlags) bool {
	return flags.contextSet || flags.endpointSet || flags.timeoutSet || flags.apiKeyStdin
}
