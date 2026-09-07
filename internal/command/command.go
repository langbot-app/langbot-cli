package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/output"
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
	root.AddCommand(newIdentityCommand(service, deps, flags, "whoami"))
	root.AddCommand(newIdentityCommand(service, deps, flags, "capabilities"))
	return root
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
