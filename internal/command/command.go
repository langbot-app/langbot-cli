package command

import (
	"context"
	"encoding/json"
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
	"github.com/langbot-app/langbot-cli/internal/deploy"
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
	LocalRunner       deploy.Runner
	LocalHTTP         *http.Client
	LocalDir          string
	LocalLocatorPath  string
	LatestRelease     func(context.Context) (string, error)
}

type globalFlags struct {
	config      string
	configSet   bool
	context     string
	endpoint    string
	timeout     string
	apiKeyStdin bool
	output      string
	outputSet   bool
	commandPath string
	contextSet  bool
	endpointSet bool
	timeoutSet  bool
	exitCode    int
}

func Execute(ctx context.Context, args []string, deps Dependencies) int {
	deps = normalizeDependencies(deps)
	flags := &globalFlags{}
	service := app.New(app.Dependencies{
		In: deps.In, LookupEnv: deps.LookupEnv, DefaultConfigPath: func() (string, error) {
			if flags.config != "" {
				return flags.config, nil
			}
			return deps.DefaultConfigPath()
		},
		Transport: deps.Transport, Version: deps.Version, Commit: deps.Commit, BuildDate: deps.BuildDate,
		LocalRunner: deps.LocalRunner, LocalHTTP: deps.LocalHTTP, LocalDir: deps.LocalDir,
		LocalLocatorPath: deps.LocalLocatorPath, LatestRelease: deps.LatestRelease, LocalLogOut: deps.Out, LocalLogErr: deps.Err,
	})
	root := newRoot(deps, flags, service)
	root.SetArgs(args)
	executed, err := root.ExecuteContextC(ctx)
	if err != nil {
		selectOutput(executed, flags)
		var typed *result.Error
		if !errors.As(err, &typed) {
			err = result.New("input", "参数无效；请运行 lbctl --help 查看用法")
		}
		return emit(deps, flags, app.Result{}, err)
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
				return result.New("input", "未知命令或参数；请运行 lbctl --help 查看可用命令")
			}
			return cmd.Help()
		},
	}
	root.SetOut(deps.Out)
	root.SetErr(deps.Err)
	root.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return result.New("input", "参数无效；请运行 lbctl --help 查看用法")
	})
	persistent := root.PersistentFlags()
	persistent.StringVar(&flags.config, "config", "", "配置文件路径")
	persistent.StringVar(&flags.context, "context", "", "使用指定 context")
	persistent.StringVar(&flags.endpoint, "endpoint", "", "临时服务地址")
	persistent.StringVar(&flags.timeout, "timeout", "", "操作超时时间")
	persistent.BoolVar(&flags.apiKeyStdin, "api-key-stdin", false, "从 stdin 读取本次请求的 API Key")
	persistent.StringVarP(&flags.output, "output", "o", "", "输出格式：json 或 yaml；默认简洁模式（schema 默认 JSON）")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		selectOutput(cmd, flags)
		flags.configSet = cmd.InheritedFlags().Changed("config")
		if flags.configSet && strings.TrimSpace(flags.config) == "" {
			return result.New("input", "config 不能为空")
		}
		flags.contextSet = cmd.InheritedFlags().Changed("context")
		flags.endpointSet = cmd.InheritedFlags().Changed("endpoint")
		flags.timeoutSet = cmd.InheritedFlags().Changed("timeout")
		if flags.outputSet {
			if _, err := output.NormalizeFormat(flags.output); err != nil {
				return result.New("input", "输出格式无效，请使用 json 或 yaml")
			}
		}
		return nil
	}
	root.AddCommand(newContextCommand(service, deps, flags))
	root.AddCommand(newStatusCommand(service, deps, flags))
	root.AddCommand(newDoctorCommand(service, deps, flags))
	root.AddCommand(newInstallCommand(service, deps, flags))
	root.AddCommand(newAdoptCommand(service, deps, flags))
	root.AddCommand(newLifecycleCommand(service, deps, flags, "start"))
	root.AddCommand(newLifecycleCommand(service, deps, flags, "stop"))
	root.AddCommand(newLifecycleCommand(service, deps, flags, "restart"))
	root.AddCommand(newUpgradeCommand(service, deps, flags))
	root.AddCommand(newUninstallCommand(service, deps, flags))
	root.AddCommand(newLocalLogsCommand(service, deps, flags))
	root.AddCommand(newVersionCommand(service, deps, flags))
	root.AddCommand(newRawCommand(service, deps, flags))
	root.AddCommand(newAPICommand(service, deps, flags))
	root.AddCommand(newSchemaCommand(deps, flags))
	root.AddCommand(newIdentityCommand(service, deps, flags, "whoami"))
	root.AddCommand(newIdentityCommand(service, deps, flags, "capabilities"))
	root.AddCommand(newBotCommand(service, deps, flags))
	root.AddCommand(newPipelineCommand(service, deps, flags))
	root.AddCommand(newProviderCommand(service, deps, flags))
	root.AddCommand(newModelCommand(service, deps, flags))
	root.AddCommand(newTaskCommand(service, deps, flags))
	root.AddCommand(newKnowledgeBaseCommand(service, deps, flags))
	root.AddCommand(newPluginCommand(service, deps, flags))
	root.AddCommand(newSkillCommand(service, deps, flags))
	root.AddCommand(newMCPServerCommand(service, deps, flags))
	return root
}

func newProviderCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "provider", Short: "管理 Provider", Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{Use: "list", Short: "列出 Provider", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.ProviderList(cmd.Context(), connectionOptions(flags)) })
	}})
	command.AddCommand(&cobra.Command{Use: "get <uuid>", Short: "读取 Provider", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ProviderGet(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	var createFile string
	var createDryRun bool
	create := &cobra.Command{Use: "create", Short: "创建 Provider", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := loadRequestBody(cmd.Context(), createFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ProviderCreate(cmd.Context(), body, createDryRun, connectionOptions(flags))
		})
	}}
	create.Flags().StringVar(&createFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	create.Flags().BoolVar(&createDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(create)
	var updateFile string
	var updateDryRun bool
	update := &cobra.Command{Use: "update <uuid>", Short: "更新 Provider", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), updateFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ProviderUpdate(cmd.Context(), args[0], body, updateDryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&updateFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	update.Flags().BoolVar(&updateDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(update)
	var deleteYes, deleteDryRun bool
	deleteCommand := &cobra.Command{Use: "delete <uuid>", Short: "删除 Provider", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ProviderDelete(cmd.Context(), args[0], deleteYes, deleteDryRun, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().BoolVar(&deleteYes, "yes", false, "确认删除")
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, "只检查前置条件，不删除")
	command.AddCommand(deleteCommand)
	var scanType string
	var scanDryRun bool
	scan := &cobra.Command{Use: "scan-models <uuid>", Short: "扫描 Provider 模型", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ProviderScanModels(cmd.Context(), args[0], strings.ToLower(strings.TrimSpace(scanType)), scanDryRun, connectionOptions(flags))
		})
	}}
	scan.Flags().StringVar(&scanType, "type", "", "模型类型：llm、embedding 或 rerank")
	scan.Flags().BoolVar(&scanDryRun, "dry-run", false, "只检查前置条件，不调用服务")
	command.AddCommand(scan)
	return command
}

func newModelCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "model", Short: "管理 Model", Args: cobra.NoArgs}
	var listType, providerUUID string
	list := &cobra.Command{Use: "list", Short: "列出 Model", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelList(cmd.Context(), strings.ToLower(strings.TrimSpace(listType)), providerUUID, connectionOptions(flags))
		})
	}}
	list.Flags().StringVar(&listType, "type", "", "模型类型：llm、embedding 或 rerank；不指定时聚合全部类型")
	list.Flags().StringVar(&providerUUID, "provider", "", "按 Provider UUID 筛选")
	command.AddCommand(list)
	var getType string
	get := &cobra.Command{Use: "get <uuid>", Short: "读取 Model", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(getType) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", "model get 必须指定 --type"))
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelGet(cmd.Context(), strings.ToLower(strings.TrimSpace(getType)), args[0], connectionOptions(flags))
		})
	}}
	get.Flags().StringVar(&getType, "type", "", "模型类型：llm、embedding 或 rerank（必填）")
	command.AddCommand(get)
	var createType, createFile string
	var createDryRun bool
	create := &cobra.Command{Use: "create", Short: "创建 Model", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(createType) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", "model create 必须指定 --type"))
		}
		body, err := loadRequestBody(cmd.Context(), createFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelCreate(cmd.Context(), strings.ToLower(strings.TrimSpace(createType)), body, createDryRun, connectionOptions(flags))
		})
	}}
	create.Flags().StringVar(&createType, "type", "", "模型类型：llm、embedding 或 rerank（必填）")
	create.Flags().StringVar(&createFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	create.Flags().BoolVar(&createDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(create)
	var updateType, updateFile string
	var updateDryRun bool
	update := &cobra.Command{Use: "update <uuid>", Short: "更新 Model", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(updateType) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", "model update 必须指定 --type"))
		}
		body, err := loadRequestBody(cmd.Context(), updateFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelUpdate(cmd.Context(), strings.ToLower(strings.TrimSpace(updateType)), args[0], body, updateDryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&updateType, "type", "", "模型类型：llm、embedding 或 rerank（必填）")
	update.Flags().StringVar(&updateFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	update.Flags().BoolVar(&updateDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(update)
	var deleteType string
	var deleteYes, deleteDryRun bool
	deleteCommand := &cobra.Command{Use: "delete <uuid>", Short: "删除 Model", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(deleteType) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", "model delete 必须指定 --type"))
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelDelete(cmd.Context(), strings.ToLower(strings.TrimSpace(deleteType)), args[0], deleteYes, deleteDryRun, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().StringVar(&deleteType, "type", "", "模型类型：llm、embedding 或 rerank（必填）")
	deleteCommand.Flags().BoolVar(&deleteYes, "yes", false, "确认删除")
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, "只检查前置条件，不删除")
	command.AddCommand(deleteCommand)
	var testType, testFile string
	var testDryRun bool
	test := &cobra.Command{Use: "test <uuid>", Short: "测试 Model", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(testType) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", "model test 必须指定 --type"))
		}
		body, err := loadOptionalRequestBody(cmd.Context(), testFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelTest(cmd.Context(), strings.ToLower(strings.TrimSpace(testType)), args[0], body, testDryRun, connectionOptions(flags))
		})
	}}
	test.Flags().StringVar(&testType, "type", "", "模型类型：llm、embedding 或 rerank（必填）")
	test.Flags().StringVar(&testFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取；省略时使用空对象")
	test.Flags().BoolVar(&testDryRun, "dry-run", false, "只检查前置条件，不调用服务")
	command.AddCommand(test)
	return command
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
	command.AddCommand(&cobra.Command{Use: "list", Short: "列出知识库", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.KnowledgeBaseList(cmd.Context(), connectionOptions(flags)) })
	}})
	command.AddCommand(&cobra.Command{Use: "get <id>", Short: "读取知识库", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseGet(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	var createFile string
	var createDryRun bool
	create := &cobra.Command{Use: "create", Short: "创建知识库", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := loadRequestBody(cmd.Context(), createFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseCreate(cmd.Context(), body, createDryRun, connectionOptions(flags))
		})
	}}
	create.Flags().StringVar(&createFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	create.Flags().BoolVar(&createDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(create)
	var updateFile string
	var updateDryRun bool
	update := &cobra.Command{Use: "update <id>", Short: "更新知识库", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), updateFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseUpdate(cmd.Context(), args[0], body, updateDryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&updateFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	update.Flags().BoolVar(&updateDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(update)
	var deleteYes, deleteDryRun bool
	deleteCommand := &cobra.Command{Use: "delete <id>", Short: "删除知识库", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseDelete(cmd.Context(), args[0], deleteYes, deleteDryRun, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().BoolVar(&deleteYes, "yes", false, "确认删除")
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(deleteCommand)
	var retrieveFile string
	retrieve := &cobra.Command{Use: "retrieve <id>", Short: "检索知识库", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), retrieveFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseRetrieve(cmd.Context(), args[0], body, connectionOptions(flags))
		})
	}}
	retrieve.Flags().StringVar(&retrieveFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	command.AddCommand(retrieve)
	files := &cobra.Command{Use: "file", Short: "管理知识库文件", Args: cobra.NoArgs}
	files.AddCommand(&cobra.Command{Use: "list <id>", Short: "列出知识库文件", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseFileList(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	var fileYes, fileDryRun bool
	fileDelete := &cobra.Command{Use: "delete <id> <file-id>", Short: "删除知识库文件", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseFileDelete(cmd.Context(), args[0], args[1], fileYes, fileDryRun, connectionOptions(flags))
		})
	}}
	fileDelete.Flags().BoolVar(&fileYes, "yes", false, "确认删除")
	fileDelete.Flags().BoolVar(&fileDryRun, "dry-run", false, "只检查前置条件，不写入")
	files.AddCommand(fileDelete)
	command.AddCommand(files)
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
	command.AddCommand(&cobra.Command{Use: "list", Short: "列出 Plugin", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.PluginList(cmd.Context(), connectionOptions(flags)) })
	}})
	command.AddCommand(&cobra.Command{Use: "get <author> <name>", Short: "读取 Plugin", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PluginGet(cmd.Context(), args[0], args[1], connectionOptions(flags))
		})
	}})
	configCommand := &cobra.Command{Use: "config", Short: "管理 Plugin 配置", Args: cobra.NoArgs}
	configCommand.AddCommand(&cobra.Command{Use: "get <author> <name>", Short: "读取 Plugin 配置", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PluginConfigGet(cmd.Context(), args[0], args[1], connectionOptions(flags))
		})
	}})
	var configFile string
	var configDryRun bool
	configUpdate := &cobra.Command{Use: "update <author> <name>", Short: "更新 Plugin 配置", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), configFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PluginConfigUpdate(cmd.Context(), args[0], args[1], body, configDryRun, connectionOptions(flags))
		})
	}}
	configUpdate.Flags().StringVar(&configFile, "file", "", "JSON/YAML 配置文件，使用 - 从 stdin 读取")
	configUpdate.Flags().BoolVar(&configDryRun, "dry-run", false, "只检查前置条件，不写入")
	configCommand.AddCommand(configUpdate)
	command.AddCommand(configCommand)
	var logLevel string
	var logLimit int
	logs := &cobra.Command{Use: "logs <author> <name>", Short: "读取 Plugin 日志", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PluginLogs(cmd.Context(), args[0], args[1], logLevel, logLimit, connectionOptions(flags))
		})
	}}
	logs.Flags().StringVar(&logLevel, "level", "", "按日志级别筛选")
	logs.Flags().IntVar(&logLimit, "limit", 200, "返回日志条数，上限 500")
	command.AddCommand(logs)
	var deleteData, deleteConfirmed, deleteDryRun, deleteWait bool
	var deletePollInterval, deleteWaitTimeout time.Duration
	deleteCommand := &cobra.Command{Use: "delete <author> <name>", Short: "删除 Plugin", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateWaitFlags(deleteDryRun, deleteWait, deletePollInterval, deleteWaitTimeout); err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PluginDelete(cmd.Context(), args[0], args[1], deleteData, deleteConfirmed, deleteDryRun, deleteWait, app.WaitOptions{Wait: deleteWait, PollInterval: deletePollInterval, WaitTimeout: deleteWaitTimeout}, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().BoolVar(&deleteData, "delete-data", false, "同时删除 Plugin 数据")
	deleteCommand.Flags().BoolVar(&deleteConfirmed, "yes", false, "确认删除")
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, "只检查前置条件，不删除")
	deleteCommand.Flags().BoolVar(&deleteWait, "wait", false, "等待删除任务进入终态并回读验证")
	deleteCommand.Flags().DurationVar(&deletePollInterval, "poll-interval", time.Second, "任务轮询间隔")
	deleteCommand.Flags().DurationVar(&deleteWaitTimeout, "wait-timeout", 5*time.Minute, "本地等待超时")
	command.AddCommand(deleteCommand)
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
	command.AddCommand(&cobra.Command{Use: "list", Short: "列出 Skill", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.SkillList(cmd.Context(), connectionOptions(flags)) })
	}})
	command.AddCommand(&cobra.Command{Use: "get <name>", Short: "读取 Skill", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.SkillGet(cmd.Context(), args[0], connectionOptions(flags)) })
	}})
	var createFile string
	var createDryRun bool
	create := &cobra.Command{Use: "create", Short: "创建 Skill", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := loadRequestBody(cmd.Context(), createFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillCreate(cmd.Context(), body, createDryRun, connectionOptions(flags))
		})
	}}
	create.Flags().StringVar(&createFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	create.Flags().BoolVar(&createDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(create)
	var updateFile string
	var updateDryRun bool
	update := &cobra.Command{Use: "update <name>", Short: "更新 Skill", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), updateFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillUpdate(cmd.Context(), args[0], body, updateDryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&updateFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	update.Flags().BoolVar(&updateDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(update)
	var deleteConfirmed, deleteDryRun bool
	deleteCommand := &cobra.Command{Use: "delete <name>", Short: "删除 Skill", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillDelete(cmd.Context(), args[0], deleteConfirmed, deleteDryRun, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().BoolVar(&deleteConfirmed, "yes", false, "确认删除")
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, "只检查前置条件，不删除")
	command.AddCommand(deleteCommand)
	var filesPath string
	var includeHidden bool
	fileCommand := &cobra.Command{Use: "file", Short: "管理 Skill 文件", Args: cobra.NoArgs}
	files := &cobra.Command{Use: "list <name>", Short: "列出 Skill 文件", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillFiles(cmd.Context(), args[0], filesPath, includeHidden, connectionOptions(flags))
		})
	}}
	files.Flags().StringVar(&filesPath, "path", ".", "Skill 包内的相对目录")
	files.Flags().BoolVar(&includeHidden, "include-hidden", false, "包含隐藏文件")
	fileCommand.AddCommand(files)
	fileCommand.AddCommand(&cobra.Command{Use: "read <name> <path>", Short: "读取 Skill 文件", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillFileRead(cmd.Context(), args[0], args[1], connectionOptions(flags))
		})
	}})
	var contentFile string
	var fileDryRun bool
	fileWrite := &cobra.Command{Use: "write <name> <path>", Short: "写入 Skill 文件", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := requestbody.ValidateSources(contentFile, flags.apiKeyStdin); err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		content, err := requestbody.LoadText(cmd.Context(), contentFile, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillFileWrite(cmd.Context(), args[0], args[1], content, fileDryRun, connectionOptions(flags))
		})
	}}
	fileWrite.Flags().StringVar(&contentFile, "file", "", "写入内容文件，使用 - 从 stdin 读取")
	fileWrite.Flags().BoolVar(&fileDryRun, "dry-run", false, "只检查前置条件，不写入")
	fileCommand.AddCommand(fileWrite)
	command.AddCommand(fileCommand)
	command.AddCommand(&cobra.Command{Use: "preview <name>", Short: "预览 Skill", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillPreview(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
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
	command := &cobra.Command{Use: "mcp-server", Short: "管理 MCP Server", Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{Use: "list", Short: "列出 MCP Server", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.MCPServerList(cmd.Context(), connectionOptions(flags)) })
	}})
	command.AddCommand(&cobra.Command{Use: "get <server-name>", Short: "读取 MCP Server", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerGet(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	var createFile string
	var createDryRun bool
	create := &cobra.Command{Use: "create", Short: "创建 MCP Server", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := loadRequestBody(cmd.Context(), createFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerCreate(cmd.Context(), body, createDryRun, connectionOptions(flags))
		})
	}}
	create.Flags().StringVar(&createFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	create.Flags().BoolVar(&createDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(create)
	var updateFile string
	var updateDryRun bool
	update := &cobra.Command{Use: "update <server-name>", Short: "更新 MCP Server", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), updateFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerUpdate(cmd.Context(), args[0], body, updateDryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&updateFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	update.Flags().BoolVar(&updateDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(update)
	var deleteYes, deleteDryRun bool
	deleteCommand := &cobra.Command{Use: "delete <server-name>", Short: "删除 MCP Server", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerDelete(cmd.Context(), args[0], deleteYes, deleteDryRun, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().BoolVar(&deleteYes, "yes", false, "确认删除")
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, "只检查前置条件，不写入")
	command.AddCommand(deleteCommand)
	command.AddCommand(&cobra.Command{Use: "resources <server-name>", Short: "列出 MCP 资源", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerResources(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	command.AddCommand(&cobra.Command{Use: "resource-templates <server-name>", Short: "列出 MCP 资源模板", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerResourceTemplates(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	var readFile string
	read := &cobra.Command{Use: "resource-read <server-name>", Short: "读取 MCP 资源", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), readFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerResourceRead(cmd.Context(), args[0], body, connectionOptions(flags))
		})
	}}
	read.Flags().StringVar(&readFile, "file", "", "JSON/YAML 请求体文件，使用 - 从 stdin 读取")
	command.AddCommand(read)
	var level string
	var limit int
	logs := &cobra.Command{Use: "logs <server-name>", Short: "读取 MCP 日志", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerLogs(cmd.Context(), args[0], level, limit, connectionOptions(flags))
		})
	}}
	logs.Flags().StringVar(&level, "level", "", "按日志级别筛选")
	logs.Flags().IntVar(&limit, "limit", 200, "返回日志条数，上限 500")
	command.AddCommand(logs)
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
		Use: "status", Short: "查看 Active Environment 状态", Args: cobra.NoArgs,
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

func newDoctorCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use: "doctor", Short: "诊断 Active Environment", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Doctor(cmd.Context(), app.CheckOptions{
					Context: flags.context, Endpoint: flags.endpoint, Timeout: flags.timeout,
					ContextSet: flags.contextSet, EndpointSet: flags.endpointSet, TimeoutSet: flags.timeoutSet,
					APIKeyStdin: flags.apiKeyStdin,
				})
			})
		},
	}
}

func newInstallCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var dir, version, profile string
	var port int
	var dryRun bool
	cmd := &cobra.Command{
		Use: "install", Short: "安装本机 LangBot", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.contextSet || flags.endpointSet || flags.apiKeyStdin {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "install 不接受 context、endpoint 或 API Key"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Install(cmd.Context(), app.InstallOptions{
					Dir: dir, Version: version, Profile: profile, Port: port, DryRun: dryRun,
					Timeout: flags.timeout, TimeoutSet: flags.timeoutSet,
				})
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "本机部署目录")
	cmd.Flags().StringVar(&version, "version", "", "LangBot 稳定 Release tag，默认最新版")
	cmd.Flags().StringVar(&profile, "profile", "basic", "部署 profile：basic 或 all")
	cmd.Flags().IntVar(&port, "port", 5300, "LangBot HTTP 宿主机端口")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只执行前置检查并输出安装计划")
	return cmd
}

func newAdoptCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var dir, file, project, profile string
	cmd := &cobra.Command{
		Use: "adopt", Short: "接管已有本机 Compose 部署", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.contextSet {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "adopt 使用显式 endpoint，不接受 context"))
			}
			if !flags.endpointSet || strings.TrimSpace(file) == "" || strings.TrimSpace(project) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "adopt 需要 --endpoint、--file 和 --project"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Adopt(cmd.Context(), app.AdoptOptions{
					Dir: dir, ComposeFile: file, ComposeProject: project, Endpoint: flags.endpoint, Profile: profile,
					Timeout: flags.timeout, TimeoutSet: flags.timeoutSet, APIKeyStdin: flags.apiKeyStdin,
				})
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "lbctl 部署记录目录")
	cmd.Flags().StringVar(&file, "file", "", "Compose 文件路径")
	cmd.Flags().StringVar(&project, "project", "", "Compose project 名称")
	cmd.Flags().StringVar(&profile, "profile", "basic", "部署 profile：basic 或 all")
	return cmd
}

func newLifecycleCommand(service *app.Service, deps Dependencies, flags *globalFlags, action string) *cobra.Command {
	short := map[string]string{"start": "启动本机受管部署", "stop": "停止本机受管部署", "restart": "重启本机受管部署"}[action]
	return &cobra.Command{
		Use: action, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options := app.LifecycleOptions{CheckOptions: connectionOptions(flags)}
			return emitCall(deps, flags, func() (app.Result, error) {
				switch action {
				case "start":
					return service.Start(cmd.Context(), options)
				case "stop":
					return service.Stop(cmd.Context(), options)
				default:
					return service.Restart(cmd.Context(), options)
				}
			})
		},
	}
}

func newUpgradeCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var version string
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use: "upgrade", Short: "安全升级本机受管部署", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(version) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "upgrade 需要 --version <tag>"))
			}
			if !dryRun && !yes {
				return emitCommand(deps, flags, app.Result{}, result.New("precondition", "升级会停止并修改本机部署，请显式传入 --yes"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Upgrade(cmd.Context(), app.UpgradeOptions{
					CheckOptions: connectionOptions(flags), Version: version, DryRun: dryRun, Yes: yes,
				})
			})
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "目标 LangBot 稳定 Release tag")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只验证前置条件并输出升级计划")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认停服、备份和升级")
	return cmd
}

func newUninstallCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var purgeData, yes bool
	cmd := &cobra.Command{
		Use: "uninstall", Short: "卸载本机受管部署", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return emitCommand(deps, flags, app.Result{}, result.New("precondition", "卸载会删除受管容器和网络，请显式传入 --yes"))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Uninstall(cmd.Context(), app.UninstallOptions{
					CheckOptions: connectionOptions(flags), PurgeData: purgeData, Yes: yes,
				})
			})
		},
	}
	cmd.Flags().BoolVar(&purgeData, "purge-data", false, "同时清理 lbctl 受管数据目录")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认卸载")
	return cmd
}

func newLocalLogsCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var serviceName, since string
	var tail int
	var follow bool
	cmd := &cobra.Command{
		Use: "logs", Short: "读取本机受管部署日志", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format := output.FormatDefault
			if flags.outputSet {
				format, _ = output.NormalizeFormat(flags.output)
			}
			options := app.LocalLogsOptions{
				CheckOptions: connectionOptions(flags), Service: serviceName, Tail: tail, Since: since, Follow: follow, Format: format,
			}
			if !follow {
				return emitCall(deps, flags, func() (app.Result, error) { return service.LocalLogs(cmd.Context(), options) })
			}
			if format == output.FormatYAML {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "logs --follow 不支持 YAML 输出"))
			}
			err := service.FollowLocalLogs(cmd.Context(), options)
			flags.exitCode = result.ExitCode(err)
			if err == nil {
				return nil
			}
			if format == output.FormatJSON {
				envelope := result.Envelope{OK: false, Error: result.AsError(err)}
				if encodeErr := json.NewEncoder(deps.Out).Encode(envelope); encodeErr != nil {
					fmt.Fprintln(deps.Err, "输出失败")
					flags.exitCode = 10
				}
				return nil
			}
			return emitCommand(deps, flags, app.Result{}, err)
		},
	}
	cmd.Flags().StringVar(&serviceName, "service", "", "服务名：langbot、langbot_plugin_runtime 或 langbot_box")
	cmd.Flags().IntVar(&tail, "tail", 200, "返回最后日志行数，上限 5000")
	cmd.Flags().StringVar(&since, "since", "", "只读取指定时间之后的日志")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "持续跟踪日志")
	return cmd
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
	flags.exitCode = emit(deps, flags, value, err)
	return nil
}

func emitCall(deps Dependencies, flags *globalFlags, call func() (app.Result, error)) error {
	value, err := call()
	return emitCommand(deps, flags, value, err)
}

func emit(deps Dependencies, flags *globalFlags, value app.Result, callErr error) int {
	format := output.FormatDefault
	if flags.outputSet {
		if normalized, err := output.NormalizeFormat(flags.output); err == nil {
			format = normalized
		}
	} else if flags.commandPath == "schema" {
		format = output.FormatJSON
	}
	envelope := result.Envelope{OK: callErr == nil, Data: value.Data, Meta: value.Meta}
	if callErr != nil {
		envelope.Error = result.AsError(callErr)
	}
	if err := output.RenderCommand(deps.Out, envelope, format, flags.commandPath); err != nil {
		fmt.Fprintln(deps.Err, "输出失败")
		return 10
	}
	return result.ExitCode(callErr)
}

func commandPath(cmd *cobra.Command) string {
	path := strings.TrimSpace(cmd.CommandPath())
	path = strings.TrimPrefix(path, "lbctl ")
	return strings.ReplaceAll(path, " ", ".")
}

func selectOutput(cmd *cobra.Command, flags *globalFlags) {
	if cmd == nil {
		return
	}
	flags.commandPath = commandPath(cmd)
	flags.outputSet = cmd.Flags().Changed("output")
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
