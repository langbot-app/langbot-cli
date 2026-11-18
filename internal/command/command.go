package command

import (
	"bytes"
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
	"github.com/langbot-app/langbot-cli/internal/clientidentity"
	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/output"
	"github.com/langbot-app/langbot-cli/internal/progress"
	"github.com/langbot-app/langbot-cli/internal/requestbody"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	NoticeHome        string
	Telemetry         *telemetry.Client
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
	notice         string
	noticeMachine  bool
	display        *progress.Display
	language       string
	verbose        bool
	operationError error

	asMember    bool
	listView    listViewOptions
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
			return selectedConfigPath(deps, flags)
		},
		Transport: deps.Transport, Version: deps.Version, Commit: deps.Commit, BuildDate: deps.BuildDate,
		LocalRunner: deps.LocalRunner, LocalHTTP: deps.LocalHTTP, LocalDir: deps.LocalDir,
		LocalLocatorPath: deps.LocalLocatorPath, LatestRelease: deps.LatestRelease, LocalLogOut: deps.Out, LocalLogErr: deps.Err,
	})
	root := newRoot(deps, flags, service)
	root.AddCommand(newUpdateCommand(deps, flags))
	selected, remaining, _ := root.Find(args)
	var parseErr error
	if selected != nil {
		parseErr = selected.ParseFlags(remaining)
	}
	_, formatErr := output.NormalizeFormat(flags.output)
	machine := (flags.output != "" && formatErr == nil) || (selected != nil && selected.Name() == "schema")
	if humanHelpRequest(root, selected, parseErr) {
		machine = false
	}
	lang := i18n.English
	if selected == nil || selected.Name() != "schema" || deps.NoticeHome != "" || !machine {
		lang = i18n.Detect(flags.language, deps.LookupEnv, i18n.SystemLanguage)
	}
	verbose := flags.verbose
	restoreLanguage := i18n.UseMode(lang, machine)
	defer restoreLanguage()
	// Help requests (`--help`, `-h`, `help`) are what install scripts use to
	// probe capabilities with stderr discarded; they must not consume the
	// one-time terms notice, or the user never sees it.
	if deps.NoticeHome != "" && !hasHelpFlag(args) && (selected == nil || selected.Name() != "help") {
		var notice bytes.Buffer
		_ = i18n.FirstRun(deps.NoticeHome, &notice, lang)
		flags.notice, flags.noticeMachine = notice.String(), machine
	}
	root = newRoot(deps, flags, service)
	root.AddCommand(newUpdateCommand(deps, flags))
	if _, file := deps.Err.(*os.File); file && selected != nil && !hasHelpFlag(args) && selected.Name() != "help" && selected.Name() != "schema" {
		flags.display = progress.New(deps.Err, deps.LookupEnv, machine, verbose)
		ctx = progress.WithContext(ctx, flags.display)
	}
	root.SetArgs(args)
	var operation *telemetry.Operation
	if selected, _, _ := root.Find(args); selected != nil && !hasHelpFlag(args) {
		if name := lifecycleTelemetryCommand(root, selected); name != "" {
			ctx, operation = deps.Telemetry.Begin(ctx, name)
		}
	}
	executed, err := root.ExecuteContextC(ctx)
	showNotice(deps, flags)
	if flags.display != nil {
		flags.display.Finish(err == nil && flags.exitCode == 0)
	}
	if operation != nil {
		operationErr := err
		if operationErr == nil {
			operationErr = flags.operationError
		}
		if operationErr == nil && flags.exitCode != 0 {
			operationErr = errors.New(i18n.T("output failed"))
		}
		code := ""
		if operationErr != nil {
			code = result.AsError(operationErr).Type
		}
		var typed *result.Error
		if err != nil && !errors.As(err, &typed) {
			code = "input"
		}
		operation.Finish(operationErr, code)
	}
	passiveUpdateNotice(deps, flags)
	if err != nil {
		selectOutput(executed, flags)
		var typed *result.Error
		if !errors.As(err, &typed) {
			err = commandInputError(executed)
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
		Short:         i18n.T("LangBot service management CLI"),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return result.New("input", i18n.T("Unknown command or argument; run lbctl --help for available commands"))
			}
			if flags.outputSet {
				return emitCommand(deps, flags, app.Result{Data: map[string]any{"command": "lbctl"}}, nil)
			}
			_, err := fmt.Fprintln(deps.Out, i18n.Human("LangBot · Production-grade platform for building agentic IM bots · Open source")+"\n\n"+i18n.Human("Deploy:    lbctl install · upgrade · uninstall")+"\n"+i18n.Human("Manage:    lbctl status · start · stop · restart · logs")+"\n"+i18n.Human("Configure: lbctl context · model · bot · pipeline")+"\n\n"+i18n.Human("All commands: lbctl --help"))
			return err
		},
	}
	root.SetOut(deps.Out)
	root.SetErr(deps.Err)
	root.SetFlagErrorFunc(commandFlagError)
	persistent := root.PersistentFlags()
	persistent.StringVar(&flags.language, "lang", "", i18n.T("Output language: en or zh-Hans (auto-detected by default)"))
	persistent.BoolVar(&flags.verbose, "verbose", false, i18n.T("Show full subprocess output"))
	persistent.BoolVar(&flags.asMember, "as-member", false, i18n.T("Use a member session instead of an API Key"))
	persistent.StringVar(&flags.config, "config", "", i18n.T("Configuration file path; otherwise LANGBOT_CONFIG, then ~/.config/langbot/config.yaml"))
	persistent.StringVar(&flags.context, "context", "", i18n.T("Use the specified context"))
	persistent.StringVar(&flags.endpoint, "endpoint", "", i18n.T("Temporary service URL"))
	persistent.StringVar(&flags.timeout, "timeout", "", i18n.T("Operation timeout"))
	persistent.BoolVar(&flags.apiKeyStdin, "api-key-stdin", false, i18n.T("Read the request API Key from stdin; bounded by --timeout"))
	persistent.StringVarP(&flags.output, "output", "o", "", i18n.T("Output format: json or yaml; defaults to concise output (schema defaults to JSON)"))
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		commandPath := strings.ReplaceAll(strings.TrimPrefix(cmd.CommandPath(), "lbctl "), " ", ".")
		cmd.SetContext(clientidentity.WithContext(cmd.Context(), deps.Version, commandPath))
		identifier := commandPath
		if (commandPath == "op.call" || commandPath == "op.stream" || commandPath == "op.describe") && len(args) > 0 {
			identifier = args[0]
		}
		if flags.asMember || memberOperation(identifier) {
			cmd.SetContext(config.AsMember(cmd.Context()))
		}
		selectOutput(cmd, flags)
		flags.configSet = cmd.InheritedFlags().Changed("config")
		if flags.configSet && strings.TrimSpace(flags.config) == "" {
			return result.New("input", i18n.T("config must not be empty"))
		}
		flags.contextSet = cmd.InheritedFlags().Changed("context")
		flags.endpointSet = cmd.InheritedFlags().Changed("endpoint")
		flags.timeoutSet = cmd.InheritedFlags().Changed("timeout")
		if flags.outputSet {
			if _, err := output.NormalizeFormat(flags.output); err != nil {
				return result.New("input", i18n.T("Invalid output format; use json or yaml"))
			}
		}
		return nil
	}
	root.AddCommand(newContextCommand(service, deps, flags))
	root.AddCommand(newAuthCommand(service, deps, flags))
	addImageDownload(root, service, deps, flags)
	root.AddCommand(newStatusCommand(service, deps, flags))
	root.AddCommand(newInstanceListCommand(service, deps, flags))
	root.AddCommand(newDoctorCommand(service, deps, flags))
	root.AddCommand(newInstallCommand(service, deps, flags))
	root.AddCommand(newAutostartCommand(service, deps, flags))
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
	root.AddCommand(newKnowledgeEngineCommand(service, deps, flags), newKnowledgeParserCommand(service, deps, flags))
	root.AddCommand(newMonitoringCommand(service, deps, flags))
	root.AddCommand(newSandboxCommand(service, deps, flags))
	root.AddCommand(newPluginCommand(service, deps, flags))
	root.AddCommand(newSkillCommand(service, deps, flags))
	root.AddCommand(newMCPServerCommand(service, deps, flags))
	addOperationCommands(root, service, deps, flags)
	configureHumanHelp(root)
	configureExperience(root, deps, flags)
	return root
}

func newProviderCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "provider", Short: i18n.T("Manage Providers"), Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{Use: "list", Short: i18n.T("List Providers"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.ProviderList(cmd.Context(), connectionOptions(flags)) })
	}})
	command.AddCommand(&cobra.Command{Use: "get <uuid>", Short: i18n.T("Read a Provider"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ProviderGet(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	var createFile string
	var createDryRun bool
	create := &cobra.Command{Use: "create", Short: i18n.T("Create a Provider"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := loadRequestBody(cmd.Context(), createFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ProviderCreate(cmd.Context(), body, createDryRun, connectionOptions(flags))
		})
	}}
	create.Flags().StringVar(&createFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	create.Flags().BoolVar(&createDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(create)
	var updateFile string
	var updateDryRun bool
	update := &cobra.Command{Use: "update <uuid>", Short: i18n.T("Update a Provider"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), updateFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ProviderUpdate(cmd.Context(), args[0], body, updateDryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&updateFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	update.Flags().BoolVar(&updateDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(update)
	var deleteYes, deleteDryRun bool
	deleteCommand := &cobra.Command{Use: "delete <uuid>", Short: i18n.T("Delete a Provider"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ProviderDelete(cmd.Context(), args[0], deleteYes, deleteDryRun, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().BoolVar(&deleteYes, "yes", false, i18n.T("Confirm deletion"))
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, i18n.T("Check prerequisites without deleting"))
	command.AddCommand(deleteCommand)
	var scanType string
	var scanDryRun bool
	scan := &cobra.Command{Use: "scan-models <uuid>", Short: i18n.T("Scan Provider models"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ProviderScanModels(cmd.Context(), args[0], strings.ToLower(strings.TrimSpace(scanType)), scanDryRun, connectionOptions(flags))
		})
	}}
	scan.Flags().StringVar(&scanType, "type", "", i18n.T("Model type: llm, embedding or rerank"))
	scan.Flags().BoolVar(&scanDryRun, "dry-run", false, i18n.T("Check prerequisites without calling the service"))
	command.AddCommand(scan)
	return command
}

func newModelCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "model", Short: i18n.T("Manage Models"), Args: cobra.NoArgs}
	var listType, providerUUID string
	list := &cobra.Command{Use: "list", Short: i18n.T("List Models"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelList(cmd.Context(), strings.ToLower(strings.TrimSpace(listType)), providerUUID, connectionOptions(flags))
		})
	}}
	list.Flags().StringVar(&listType, "type", "", i18n.T("Model type: llm, embedding, rerank, asr or tts; defaults to aggregating the three original types"))
	list.Flags().StringVar(&providerUUID, "provider", "", i18n.T("Filter by Provider UUID"))
	command.AddCommand(list)
	var getType string
	get := &cobra.Command{Use: "get <uuid>", Short: i18n.T("Read a Model"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(getType) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("model get requires --type")))
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelGet(cmd.Context(), strings.ToLower(strings.TrimSpace(getType)), args[0], connectionOptions(flags))
		})
	}}
	get.Flags().StringVar(&getType, "type", "", i18n.T("Model type: llm, embedding, rerank, asr or tts (required)"))
	command.AddCommand(get)
	var createType, createFile string
	var createDryRun bool
	create := &cobra.Command{Use: "create", Short: i18n.T("Create a Model"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(createType) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("model create requires --type")))
		}
		body, err := loadRequestBody(cmd.Context(), createFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelCreate(cmd.Context(), strings.ToLower(strings.TrimSpace(createType)), body, createDryRun, connectionOptions(flags))
		})
	}}
	create.Flags().StringVar(&createType, "type", "", i18n.T("Model type: llm, embedding, rerank, asr or tts (required)"))
	create.Flags().StringVar(&createFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	create.Flags().BoolVar(&createDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(create)
	var updateType, updateFile string
	var updateDryRun bool
	update := &cobra.Command{Use: "update <uuid>", Short: i18n.T("Update a Model"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(updateType) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("model update requires --type")))
		}
		body, err := loadRequestBody(cmd.Context(), updateFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelUpdate(cmd.Context(), strings.ToLower(strings.TrimSpace(updateType)), args[0], body, updateDryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&updateType, "type", "", i18n.T("Model type: llm, embedding, rerank, asr or tts (required)"))
	update.Flags().StringVar(&updateFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	update.Flags().BoolVar(&updateDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(update)
	var deleteType string
	var deleteYes, deleteDryRun bool
	deleteCommand := &cobra.Command{Use: "delete <uuid>", Short: i18n.T("Delete a Model"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(deleteType) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("model delete requires --type")))
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelDelete(cmd.Context(), strings.ToLower(strings.TrimSpace(deleteType)), args[0], deleteYes, deleteDryRun, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().StringVar(&deleteType, "type", "", i18n.T("Model type: llm, embedding, rerank, asr or tts (required)"))
	deleteCommand.Flags().BoolVar(&deleteYes, "yes", false, i18n.T("Confirm deletion"))
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, i18n.T("Check prerequisites without deleting"))
	command.AddCommand(deleteCommand)
	var testType, testFile string
	var testDryRun bool
	test := &cobra.Command{Use: "test <uuid>", Short: i18n.T("Test a Model"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(testType) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("model test requires --type")))
		}
		body, err := loadOptionalRequestBody(cmd.Context(), testFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.ModelTest(cmd.Context(), strings.ToLower(strings.TrimSpace(testType)), args[0], body, testDryRun, connectionOptions(flags))
		})
	}}
	test.Flags().StringVar(&testType, "type", "", i18n.T("Model type: llm, embedding, rerank, asr or tts (required)"))
	test.Flags().StringVar(&testFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin; defaults to an empty object"))
	test.Flags().BoolVar(&testDryRun, "dry-run", false, i18n.T("Check prerequisites without calling the service"))
	command.AddCommand(test)
	return command
}

func newBotCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "bot", Short: i18n.T("Manage Bot resources"), Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{
		Use: "list", Short: i18n.T("List Bots"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.BotList(cmd.Context(), connectionOptions(flags))
			})
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "get <id>", Short: i18n.T("Read a Bot"), Args: cobra.ExactArgs(1),
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
		Use: "create", Short: i18n.T("Create a Bot"), Args: cobra.NoArgs,
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
	command.Flags().StringVar(&file, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	command.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check target and operation prerequisites without writing"))
	return command
}

func newBotUpdateCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var file string
	var dryRun bool
	command := &cobra.Command{
		Use: "update <id>", Short: i18n.T("Update a Bot"), Args: cobra.ExactArgs(1),
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
	command.Flags().StringVar(&file, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	command.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check target and operation prerequisites without writing"))
	return command
}

func newBotDeleteCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var yes bool
	var dryRun bool
	command := &cobra.Command{
		Use: "delete <id>", Short: i18n.T("Delete a Bot"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.BotDelete(cmd.Context(), args[0], yes, dryRun, connectionOptions(flags))
			})
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, i18n.T("Confirm deletion"))
	command.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check target and operation prerequisites without writing"))
	return command
}

func newPipelineCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "pipeline", Short: i18n.T("Manage Pipeline resources"), Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{
		Use: "list", Short: i18n.T("List Pipelines"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PipelineList(cmd.Context(), connectionOptions(flags))
			})
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "get <id>", Short: i18n.T("Read a Pipeline"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PipelineGet(cmd.Context(), args[0], connectionOptions(flags))
			})
		},
	})
	command.AddCommand(newPipelineApplyCommand(service, deps, flags))
	command.AddCommand(newPipelineCopyCommand(service, deps, flags))
	command.AddCommand(newPipelineDeleteCommand(service, deps, flags))
	addPipelineExtensions(command, service, deps, flags)
	addPipelineRun(command, service, deps, flags)
	return command
}

func newPipelineApplyCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var file string
	var dryRun bool
	command := &cobra.Command{
		Use: "apply", Short: i18n.T("Create or update a Pipeline"), Args: cobra.NoArgs,
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
	command.Flags().StringVar(&file, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	command.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check target and operation prerequisites without writing"))
	return command
}

func newPipelineCopyCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use: "copy <id>", Short: i18n.T("Copy a Pipeline"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PipelineCopy(cmd.Context(), args[0], dryRun, connectionOptions(flags))
			})
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check target and operation prerequisites without writing"))
	return command
}

func newPipelineDeleteCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var yes bool
	var dryRun bool
	command := &cobra.Command{
		Use: "delete <id>", Short: i18n.T("Delete a Pipeline"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PipelineDelete(cmd.Context(), args[0], yes, dryRun, connectionOptions(flags))
			})
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, i18n.T("Confirm deletion"))
	command.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check target and operation prerequisites without writing"))
	return command
}

func newTaskCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "task", Short: i18n.T("Show an asynchronous task"), Args: cobra.NoArgs}
	var taskType, taskKind string
	list := &cobra.Command{
		Use: "list", Short: i18n.T("List asynchronous tasks"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.TaskList(cmd.Context(), taskType, taskKind, connectionOptions(flags))
			})
		},
	}
	list.Flags().StringVar(&taskType, "type", "", i18n.T("Filter by task type"))
	list.Flags().StringVar(&taskKind, "kind", "", i18n.T("Filter by task kind"))
	command.AddCommand(list)
	var wait bool
	var pollInterval time.Duration
	var waitTimeout time.Duration
	get := &cobra.Command{
		Use: "get <id>", Short: i18n.T("Read asynchronous task status"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if wait && (pollInterval <= 0 || waitTimeout <= 0) {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("Polling interval and wait timeout must be positive")))
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
	get.Flags().BoolVar(&wait, "wait", false, i18n.T("Wait for the task to reach a terminal state"))
	get.Flags().DurationVar(&pollInterval, "poll-interval", time.Second, i18n.T("Task polling interval"))
	get.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, i18n.T("Local wait timeout"))
	command.AddCommand(get)
	return command
}

func newKnowledgeBaseCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "knowledge-base", Aliases: []string{"kb"}, Short: i18n.T("Manage knowledge bases"), Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{Use: "list", Short: i18n.T("List knowledge bases"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.KnowledgeBaseList(cmd.Context(), connectionOptions(flags)) })
	}})
	command.AddCommand(&cobra.Command{Use: "get <id>", Short: i18n.T("Read a knowledge base"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseGet(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	var createFile string
	var createDryRun bool
	create := &cobra.Command{Use: "create", Short: i18n.T("Create a knowledge base"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := loadRequestBody(cmd.Context(), createFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseCreate(cmd.Context(), body, createDryRun, connectionOptions(flags))
		})
	}}
	create.Flags().StringVar(&createFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	create.Flags().BoolVar(&createDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(create)
	var updateFile string
	var updateDryRun bool
	update := &cobra.Command{Use: "update <id>", Short: i18n.T("Update a knowledge base"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), updateFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseUpdate(cmd.Context(), args[0], body, updateDryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&updateFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	update.Flags().BoolVar(&updateDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(update)
	var deleteYes, deleteDryRun bool
	deleteCommand := &cobra.Command{Use: "delete <id>", Short: i18n.T("Delete a knowledge base"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseDelete(cmd.Context(), args[0], deleteYes, deleteDryRun, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().BoolVar(&deleteYes, "yes", false, i18n.T("Confirm deletion"))
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(deleteCommand)
	var retrieveFile string
	retrieve := &cobra.Command{Use: "retrieve <id>", Short: i18n.T("Search a knowledge base"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), retrieveFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseRetrieve(cmd.Context(), args[0], body, connectionOptions(flags))
		})
	}}
	retrieve.Flags().StringVar(&retrieveFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	command.AddCommand(retrieve)
	files := &cobra.Command{Use: "file", Short: i18n.T("Manage knowledge-base files"), Args: cobra.NoArgs}
	files.AddCommand(&cobra.Command{Use: "list <id>", Short: i18n.T("List knowledge-base files"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseFileList(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	var fileYes, fileDryRun bool
	fileDelete := &cobra.Command{Use: "delete <id> <file-id>", Short: i18n.T("Delete a knowledge-base file"), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.KnowledgeBaseFileDelete(cmd.Context(), args[0], args[1], fileYes, fileDryRun, connectionOptions(flags))
		})
	}}
	fileDelete.Flags().BoolVar(&fileYes, "yes", false, i18n.T("Confirm deletion"))
	fileDelete.Flags().BoolVar(&fileDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	files.AddCommand(fileDelete)
	command.AddCommand(files)
	var filenames []string
	var directory string
	var parserPluginID string
	var dryRun bool
	var wait bool
	var pollInterval time.Duration
	var waitTimeout time.Duration
	ingest := &cobra.Command{
		Use: "ingest <knowledge-base-id>", Short: i18n.T("Upload one or more files or a directory for knowledge-base ingestion"), Args: cobra.ExactArgs(1),
		Example: "  lbctl knowledge-base ingest <id> --file guide.pdf --file faq.md --wait\n  lbctl knowledge-base ingest <id> --dir ./documents --dry-run\n  lbctl knowledge-base ingest <id> --dir ./documents --wait",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun && wait {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("--dry-run cannot be combined with --wait")))
			}
			if wait && (pollInterval <= 0 || waitTimeout <= 0) {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("Polling interval and wait timeout must be positive")))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.KnowledgeBaseIngestFiles(cmd.Context(), args[0], filenames, directory, parserPluginID, dryRun, app.WaitOptions{
					Wait: wait, PollInterval: pollInterval, WaitTimeout: waitTimeout,
				}, connectionOptions(flags))
			})
		},
	}
	ingest.Flags().StringArrayVar(&filenames, "file", nil, i18n.T("Local file; repeatable; provide this or --dir; stdin not supported"))
	ingest.Flags().StringVar(&directory, "dir", "", i18n.T("Recursively import a directory, skipping dotfiles and symbolic links; can combine with --file"))
	ingest.Flags().StringVar(&parserPluginID, "parser-plugin-id", "", i18n.T("Optional parser plugin ID"))
	ingest.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check prerequisites without uploading or submitting"))
	ingest.Flags().BoolVar(&wait, "wait", false, i18n.T("Wait for each input file ingestion task; batch wait does not support ZIP, extract first"))
	ingest.Flags().DurationVar(&pollInterval, "poll-interval", time.Second, i18n.T("Task polling interval"))
	ingest.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, i18n.T("Local wait timeout for each file"))
	command.AddCommand(ingest)
	return command
}

func newPluginCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: i18n.T("Manage Plugins"), Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{Use: "list", Short: i18n.T("List Plugins"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.PluginList(cmd.Context(), connectionOptions(flags)) })
	}})
	command.AddCommand(&cobra.Command{Use: "get <author> <name>", Short: i18n.T("Read a Plugin"), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PluginGet(cmd.Context(), args[0], args[1], connectionOptions(flags))
		})
	}})
	configCommand := &cobra.Command{Use: "config", Short: i18n.T("Manage Plugin configuration"), Args: cobra.NoArgs}
	configCommand.AddCommand(&cobra.Command{Use: "get <author> <name>", Short: i18n.T("Read Plugin configuration"), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PluginConfigGet(cmd.Context(), args[0], args[1], connectionOptions(flags))
		})
	}})
	var configFile string
	var configDryRun bool
	configUpdate := &cobra.Command{Use: "update <author> <name>", Short: i18n.T("Update Plugin configuration"), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), configFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PluginConfigUpdate(cmd.Context(), args[0], args[1], body, configDryRun, connectionOptions(flags))
		})
	}}
	configUpdate.Flags().StringVar(&configFile, "file", "", i18n.T("JSON/YAML configuration file; use - to read stdin"))
	configUpdate.Flags().BoolVar(&configDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	configCommand.AddCommand(configUpdate)
	command.AddCommand(configCommand)
	var logLevel string
	var logLimit int
	logs := &cobra.Command{Use: "logs <author> <name>", Short: i18n.T("Read Plugin logs"), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PluginLogs(cmd.Context(), args[0], args[1], logLevel, logLimit, connectionOptions(flags))
		})
	}}
	logs.Flags().StringVar(&logLevel, "level", "", i18n.T("Filter by log level"))
	logs.Flags().IntVar(&logLimit, "limit", 200, i18n.T("Number of log entries to return, maximum 500"))
	command.AddCommand(logs)
	var deleteData, deleteConfirmed, deleteDryRun, deleteWait bool
	var deletePollInterval, deleteWaitTimeout time.Duration
	deleteCommand := &cobra.Command{Use: "delete <author> <name>", Short: i18n.T("Delete a Plugin"), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateWaitFlags(deleteDryRun, deleteWait, deletePollInterval, deleteWaitTimeout); err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PluginDelete(cmd.Context(), args[0], args[1], deleteData, deleteConfirmed, deleteDryRun, deleteWait, app.WaitOptions{Wait: deleteWait, PollInterval: deletePollInterval, WaitTimeout: deleteWaitTimeout}, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().BoolVar(&deleteData, "delete-data", false, i18n.T("Also delete Plugin data"))
	deleteCommand.Flags().BoolVar(&deleteConfirmed, "yes", false, i18n.T("Confirm deletion"))
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, i18n.T("Check prerequisites without deleting"))
	deleteCommand.Flags().BoolVar(&deleteWait, "wait", false, i18n.T("Wait for deletion to finish and verify by reading back"))
	deleteCommand.Flags().DurationVar(&deletePollInterval, "poll-interval", time.Second, i18n.T("Task polling interval"))
	deleteCommand.Flags().DurationVar(&deleteWaitTimeout, "wait-timeout", 5*time.Minute, i18n.T("Local wait timeout"))
	command.AddCommand(deleteCommand)
	install := &cobra.Command{Use: "install", Short: i18n.T("Install a Plugin"), Args: cobra.NoArgs}
	newTaskFlags := func(cmd *cobra.Command, dryRun, wait *bool, pollInterval, waitTimeout *time.Duration) {
		cmd.Flags().BoolVar(dryRun, "dry-run", false, i18n.T("Check prerequisites without installing or upgrading"))
		cmd.Flags().BoolVar(wait, "wait", false, i18n.T("Wait for the task to reach a terminal state"))
		cmd.Flags().DurationVar(pollInterval, "poll-interval", time.Second, i18n.T("Task polling interval"))
		cmd.Flags().DurationVar(waitTimeout, "wait-timeout", 5*time.Minute, i18n.T("Local wait timeout"))
	}
	var githubFile string
	var githubDryRun, githubWait, githubConfirm bool
	var githubPollInterval, githubWaitTimeout time.Duration
	github := &cobra.Command{Use: "github", Short: i18n.T("Install a Plugin from GitHub"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := loadRequestBody(cmd.Context(), githubFile, flags.apiKeyStdin, deps.In)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			if err := validateWaitFlags(githubDryRun, githubWait, githubPollInterval, githubWaitTimeout); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PluginInstallGitHub(cmd.Context(), body, githubDryRun, githubWait, app.WaitOptions{ConfirmPermissions: githubConfirm, Wait: githubWait, PollInterval: githubPollInterval, WaitTimeout: githubWaitTimeout}, connectionOptions(flags))
			})
		},
	}
	github.Flags().StringVar(&githubFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	github.Flags().BoolVar(&githubConfirm, "confirm-permissions", false, i18n.T("Wait for installation, confirm all declared permissions and enable"))
	newTaskFlags(github, &githubDryRun, &githubWait, &githubPollInterval, &githubWaitTimeout)
	install.AddCommand(github)

	var marketplaceFile, marketplaceVersion string
	var marketplaceDryRun, marketplaceWait, marketplaceConfirm bool
	var marketplacePollInterval, marketplaceWaitTimeout time.Duration
	marketplace := &cobra.Command{Use: "marketplace [author name]", Short: i18n.T("Install a Plugin from Marketplace"), Args: func(_ *cobra.Command, args []string) error {
		if len(args) != 0 && len(args) != 2 {
			return result.New("input", i18n.T("Provide author name or use --file"))
		}
		return nil
	},
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if len(args) == 2 {
				if marketplaceFile != "" {
					return result.New("input", i18n.T("author name cannot be combined with --file"))
				}
				body["plugin_author"], body["plugin_name"] = args[0], args[1]
			} else {
				var err error
				body, err = loadRequestBody(cmd.Context(), marketplaceFile, flags.apiKeyStdin, deps.In)
				if err != nil {
					return emitCommand(deps, flags, app.Result{}, err)
				}
			}
			if marketplaceVersion != "" {
				body["plugin_version"] = marketplaceVersion
			}
			if err := validateWaitFlags(marketplaceDryRun, marketplaceWait, marketplacePollInterval, marketplaceWaitTimeout); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PluginInstallMarketplace(cmd.Context(), body, marketplaceDryRun, marketplaceWait, app.WaitOptions{ConfirmPermissions: marketplaceConfirm, UseOperations: len(args) == 2, Wait: marketplaceWait, PollInterval: marketplacePollInterval, WaitTimeout: marketplaceWaitTimeout}, connectionOptions(flags))
			})
		},
	}
	marketplace.Flags().StringVar(&marketplaceFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	marketplace.Flags().StringVar(&marketplaceVersion, "version", "", i18n.T("Specify Marketplace version"))
	marketplace.Flags().BoolVar(&marketplaceConfirm, "confirm-permissions", false, i18n.T("Wait for installation, confirm all declared permissions and enable"))
	newTaskFlags(marketplace, &marketplaceDryRun, &marketplaceWait, &marketplacePollInterval, &marketplaceWaitTimeout)
	install.AddCommand(marketplace)

	var localFile string
	var localDryRun, localWait, localConfirm bool
	var localPollInterval, localWaitTimeout time.Duration
	local := &cobra.Command{Use: "local", Short: i18n.T("Install a Plugin from a local package"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateWaitFlags(localDryRun, localWait, localPollInterval, localWaitTimeout); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.PluginInstallLocal(cmd.Context(), localFile, localDryRun, localWait, app.WaitOptions{ConfirmPermissions: localConfirm, Wait: localWait, PollInterval: localPollInterval, WaitTimeout: localWaitTimeout}, connectionOptions(flags))
			})
		},
	}
	local.Flags().StringVar(&localFile, "file", "", i18n.T("Local Plugin package file"))
	local.Flags().BoolVar(&localConfirm, "confirm-permissions", false, i18n.T("Wait for installation, confirm all declared permissions and enable"))
	newTaskFlags(local, &localDryRun, &localWait, &localPollInterval, &localWaitTimeout)
	install.AddCommand(local)
	command.AddCommand(install)

	var upgradeDryRun, upgradeWait bool
	var upgradePollInterval, upgradeWaitTimeout time.Duration
	upgrade := &cobra.Command{Use: "upgrade <author> <name>", Short: i18n.T("Upgrade an installed Plugin"), Args: cobra.ExactArgs(2),
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
	command := &cobra.Command{Use: "skill", Short: i18n.T("Manage Skills"), Args: cobra.NoArgs}
	command.AddCommand(newSkillPushCommand(service, deps, flags))
	command.AddCommand(&cobra.Command{Use: "list", Short: i18n.T("List Skills"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.SkillList(cmd.Context(), connectionOptions(flags)) })
	}})
	command.AddCommand(&cobra.Command{Use: "get <name>", Short: i18n.T("Read a Skill"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.SkillGet(cmd.Context(), args[0], connectionOptions(flags)) })
	}})
	var createFile string
	var createDryRun bool
	create := &cobra.Command{Use: "create", Short: i18n.T("Create a Skill"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := loadRequestBody(cmd.Context(), createFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillCreate(cmd.Context(), body, createDryRun, connectionOptions(flags))
		})
	}}
	create.Flags().StringVar(&createFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	create.Flags().BoolVar(&createDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(create)
	var updateFile string
	var updateDryRun bool
	update := &cobra.Command{Use: "update <name>", Short: i18n.T("Update a Skill"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), updateFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillUpdate(cmd.Context(), args[0], body, updateDryRun, connectionOptions(flags))
		})
	}}
	update.Example = i18n.T("  lbctl skill get demo -o json\n  # edit.yaml = {\"instructions\":\"Updated instructions\",\"base_revision\":\"<revision>\"}\n  lbctl skill update demo --file edit.yaml --dry-run\n  lbctl skill update demo --file edit.yaml")
	update.Flags().StringVar(&updateFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	update.Flags().BoolVar(&updateDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(update)
	var deleteConfirmed, deleteDryRun bool
	deleteCommand := &cobra.Command{Use: "delete <name>", Short: i18n.T("Delete a Skill"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillDelete(cmd.Context(), args[0], deleteConfirmed, deleteDryRun, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().BoolVar(&deleteConfirmed, "yes", false, i18n.T("Confirm deletion"))
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, i18n.T("Check prerequisites without deleting"))
	command.AddCommand(deleteCommand)
	var filesPath string
	var includeHidden bool
	fileCommand := &cobra.Command{Use: "file", Short: i18n.T("Manage Skill files"), Args: cobra.NoArgs}
	files := &cobra.Command{Use: "list <name>", Short: i18n.T("List Skill files"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillFiles(cmd.Context(), args[0], filesPath, includeHidden, connectionOptions(flags))
		})
	}}
	files.Flags().StringVar(&filesPath, "path", ".", i18n.T("Relative directory in the Skill package"))
	files.Flags().BoolVar(&includeHidden, "include-hidden", false, i18n.T("Include hidden files"))
	fileCommand.AddCommand(files)
	fileCommand.AddCommand(&cobra.Command{Use: "read <name> <path>", Short: i18n.T("Read a Skill file"), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillFileRead(cmd.Context(), args[0], args[1], connectionOptions(flags))
		})
	}})
	var contentFile, baseRevision string
	var fileDryRun bool
	fileWrite := &cobra.Command{Use: "write <name> <path>", Short: i18n.T("Write a Skill file"), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("base-revision") && strings.TrimSpace(baseRevision) == "" {
			return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("Skill base_revision must be a non-empty string")))
		}
		if err := requestbody.ValidateSources(contentFile, flags.apiKeyStdin); err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		content, err := requestbody.LoadText(cmd.Context(), contentFile, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillFileWrite(cmd.Context(), args[0], args[1], content, fileDryRun, connectionOptions(flags), baseRevision)
		})
	}}
	fileWrite.Example = "  lbctl skill file read demo SKILL.md -o json\n  lbctl skill file write demo SKILL.md --file content.md --base-revision <revision>"
	fileWrite.Flags().StringVar(&contentFile, "file", "", i18n.T("Content file; use - to read stdin"))
	fileWrite.Flags().StringVar(&baseRevision, "base-revision", "", i18n.T("Base revision from skill get or skill file read; omitted uses the current revision"))
	fileWrite.Flags().BoolVar(&fileDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	fileCommand.AddCommand(fileWrite)
	command.AddCommand(fileCommand)
	command.AddCommand(&cobra.Command{Use: "preview <name>", Short: i18n.T("Preview a Skill"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.SkillPreview(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	install := &cobra.Command{Use: "install", Short: i18n.T("Install a Skill"), Args: cobra.NoArgs}
	var githubFile string
	var githubDryRun bool
	github := &cobra.Command{Use: "github", Short: i18n.T("Install a Skill from GitHub"), Args: cobra.NoArgs,
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
	github.Flags().StringVar(&githubFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	github.Flags().BoolVar(&githubDryRun, "dry-run", false, i18n.T("Use server preview without installing"))
	install.AddCommand(github)

	var sourcePaths []string
	var uploadFile, uploadDirectory string
	var uploadDryRun bool
	upload := &cobra.Command{Use: "upload", Short: i18n.T("Install a Skill from ZIP or a local directory"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if (uploadFile == "") == (uploadDirectory == "") {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("Provide exactly one --file or --dir")))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				if uploadDirectory != "" {
					return service.SkillInstallDirectory(cmd.Context(), uploadDirectory, sourcePaths, uploadDryRun, connectionOptions(flags))
				}
				return service.SkillInstallUpload(cmd.Context(), uploadFile, sourcePaths, uploadDryRun, connectionOptions(flags))
			})
		},
	}
	upload.Example = "  lbctl skill install upload --dir ./my-skill --dry-run\n  lbctl skill install upload --dir ./my-skill\n  lbctl skill install upload --file skill.zip"
	upload.Flags().StringVar(&uploadFile, "file", "", i18n.T("Skill ZIP file"))
	upload.Flags().StringVar(&uploadDirectory, "dir", "", i18n.T("Local Skill directory; packaged as ZIP before upload"))
	upload.Flags().StringSliceVar(&sourcePaths, "source-path", nil, i18n.T("Skill package source_paths; repeatable"))
	upload.Flags().BoolVar(&uploadDryRun, "dry-run", false, i18n.T("Use server preview without installing"))
	install.AddCommand(upload)
	command.AddCommand(install)
	return command
}

func newMCPServerCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "mcp-server", Short: i18n.T("Manage MCP Servers"), Args: cobra.NoArgs}
	command.AddCommand(&cobra.Command{Use: "list", Short: i18n.T("List MCP Servers"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) { return service.MCPServerList(cmd.Context(), connectionOptions(flags)) })
	}})
	command.AddCommand(&cobra.Command{Use: "get <server-name>", Short: i18n.T("Read an MCP Server"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerGet(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	var createFile string
	var createDryRun bool
	create := &cobra.Command{Use: "create", Short: i18n.T("Create an MCP Server"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		body, err := loadRequestBody(cmd.Context(), createFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerCreate(cmd.Context(), body, createDryRun, connectionOptions(flags))
		})
	}}
	create.Flags().StringVar(&createFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	create.Flags().BoolVar(&createDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(create)
	var updateFile string
	var updateDryRun bool
	update := &cobra.Command{Use: "update <server-name>", Short: i18n.T("Update an MCP Server"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), updateFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerUpdate(cmd.Context(), args[0], body, updateDryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&updateFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	update.Flags().BoolVar(&updateDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(update)
	var deleteYes, deleteDryRun bool
	deleteCommand := &cobra.Command{Use: "delete <server-name>", Short: i18n.T("Delete an MCP Server"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerDelete(cmd.Context(), args[0], deleteYes, deleteDryRun, connectionOptions(flags))
		})
	}}
	deleteCommand.Flags().BoolVar(&deleteYes, "yes", false, i18n.T("Confirm deletion"))
	deleteCommand.Flags().BoolVar(&deleteDryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	command.AddCommand(deleteCommand)
	command.AddCommand(&cobra.Command{Use: "resources <server-name>", Short: i18n.T("List MCP resources"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerResources(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	command.AddCommand(&cobra.Command{Use: "resource-templates <server-name>", Short: i18n.T("List MCP resource templates"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerResourceTemplates(cmd.Context(), args[0], connectionOptions(flags))
		})
	}})
	var readFile string
	read := &cobra.Command{Use: "resource-read <server-name>", Short: i18n.T("Read an MCP resource"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), readFile, flags.apiKeyStdin, deps.In)
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerResourceRead(cmd.Context(), args[0], body, connectionOptions(flags))
		})
	}}
	read.Flags().StringVar(&readFile, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	command.AddCommand(read)
	var level string
	var limit int
	logs := &cobra.Command{Use: "logs <server-name>", Short: i18n.T("Read MCP logs"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.MCPServerLogs(cmd.Context(), args[0], level, limit, connectionOptions(flags))
		})
	}}
	logs.Flags().StringVar(&level, "level", "", i18n.T("Filter by log level"))
	logs.Flags().IntVar(&limit, "limit", 200, i18n.T("Number of log entries to return, maximum 500"))
	command.AddCommand(logs)
	var file string
	var dryRun, wait bool
	var pollInterval, waitTimeout time.Duration
	testCommand := &cobra.Command{Use: "test <server-name>", Short: i18n.T("Test MCP Server connection"), Args: cobra.ExactArgs(1),
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
	testCommand.Flags().StringVar(&file, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin; defaults to an empty object"))
	testCommand.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check prerequisites without running the test"))
	testCommand.Flags().BoolVar(&wait, "wait", false, i18n.T("Wait for the task to reach a terminal state"))
	testCommand.Flags().DurationVar(&pollInterval, "poll-interval", time.Second, i18n.T("Task polling interval"))
	testCommand.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, i18n.T("Local wait timeout"))
	command.AddCommand(testCommand)
	return command
}

func validateWaitFlags(dryRun, wait bool, pollInterval, waitTimeout time.Duration) error {
	if dryRun && wait {
		return result.New("input", i18n.T("--dry-run cannot be combined with --wait"))
	}
	if wait && (pollInterval <= 0 || waitTimeout <= 0) {
		return result.New("input", i18n.T("Polling interval and wait timeout must be positive"))
	}
	return nil
}

func loadRequestBody(ctx context.Context, file string, apiKeyStdin bool, stdin io.Reader) (map[string]any, error) {
	if strings.TrimSpace(file) == "" {
		return nil, result.New("input", i18n.T("Missing required option --file; use --file <file> or --file -, see --help for usage"))
	}
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
		Use: "context", Short: i18n.T("Manage local contexts"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	contextCommand.AddCommand(newContextAdd(service, deps, flags))
	contextCommand.AddCommand(newContextUpdate(service, deps, flags))
	var discover bool
	list := &cobra.Command{
		Use: "list", Short: i18n.T("List local contexts"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := rejectCredentialInput(flags); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) { return service.WithContext(cmd.Context()).ContextList(discover) })
		},
	}
	list.Flags().BoolVar(&discover, "discover", false, i18n.T("Also discover local LangBot Compose projects read-only"))
	contextCommand.AddCommand(list)
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
		Short: i18n.T("Add a local context"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectCredentialInput(flags); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			if cmd.Flags().Changed("api-key-env") && strings.TrimSpace(apiKeyEnv) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("api-key-env must not be empty")))
			}
			if cmd.Flags().Changed("timeout") && strings.TrimSpace(timeout) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("timeout must not be empty")))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.WithContext(cmd.Context()).ContextAdd(args[0], endpoint, apiKeyEnv, expected, timeout)
			})
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", i18n.T("Service URL"))
	cmd.Flags().StringVar(&apiKeyEnv, "api-key-env", "", i18n.T("API Key environment variable name"))
	cmd.Flags().StringVar(&expected, "expect-workspace", "", i18n.T("Expected Workspace UUID"))
	cmd.Flags().StringVar(&timeout, "timeout", "", i18n.T("Default request timeout for this context"))
	return cmd
}

func newContextUpdate(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var endpoint, apiKeyEnv, expected, timeout string
	var clearCredential, clearBinding bool
	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: i18n.T("Update a local context"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectCredentialInput(flags); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			if cmd.Flags().Changed("api-key-env") && strings.TrimSpace(apiKeyEnv) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("api-key-env must not be empty")))
			}
			if cmd.Flags().Changed("timeout") && strings.TrimSpace(timeout) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("timeout must not be empty")))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.WithContext(cmd.Context()).ContextUpdate(args[0], endpoint, apiKeyEnv, expected, timeout,
					cmd.Flags().Changed("endpoint"), cmd.Flags().Changed("api-key-env"),
					cmd.Flags().Changed("expect-workspace"), cmd.Flags().Changed("timeout"), clearCredential, clearBinding)
			})
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", i18n.T("Service URL"))
	cmd.Flags().StringVar(&apiKeyEnv, "api-key-env", "", i18n.T("API Key environment variable name"))
	cmd.Flags().StringVar(&expected, "expect-workspace", "", i18n.T("Expected Workspace UUID"))
	cmd.Flags().StringVar(&timeout, "timeout", "", i18n.T("Default request timeout for this context"))
	cmd.Flags().BoolVar(&clearCredential, "clear-credential", false, i18n.T("Clear the saved credential source"))
	cmd.Flags().BoolVar(&clearBinding, "clear-binding", false, i18n.T("Clear expected Workspace binding"))
	return cmd
}

func newContextShow(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use: "show [name]", Short: i18n.T("Show context configuration; defaults to current environment"), Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.WithContext(cmd.Context()).ContextShow(name, app.CheckOptions{
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
		Use: "use <name>", Short: i18n.T("Set the default context for subsequent commands"), Args: cobra.ExactArgs(1),
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
		Use: "remove <name>", Short: i18n.T("Delete a local context"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectCredentialInput(flags); err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			return emitCall(deps, flags, func() (app.Result, error) { return service.WithContext(cmd.Context()).ContextRemove(args[0], yes) })
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("Confirm deletion"))
	return cmd
}

func newContextCheck(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use: "check [name]", Short: i18n.T("Check service connectivity"), Args: cobra.MaximumNArgs(1),
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
	cmd.Flags().BoolVar(&all, "all", false, i18n.T("Check all contexts"))
	return cmd
}

func newStatusCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: i18n.T("Show current environment status"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Status(cmd.Context(), app.CheckOptions{
					Human:   !flags.outputSet,
					Context: flags.context, Endpoint: flags.endpoint, Timeout: flags.timeout,
					ContextSet: flags.contextSet, EndpointSet: flags.endpointSet, TimeoutSet: flags.timeoutSet,
					APIKeyStdin: flags.apiKeyStdin,
				})
			})
		},
	}
}

func newDoctorCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var region string
	cmd := &cobra.Command{
		Use: "doctor", Short: i18n.T("Diagnose the current environment"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Doctor(cmd.Context(), app.CheckOptions{
					Context: flags.context, Endpoint: flags.endpoint, Timeout: flags.timeout,
					ContextSet: flags.contextSet, EndpointSet: flags.endpointSet, TimeoutSet: flags.timeoutSet,
					APIKeyStdin: flags.apiKeyStdin, Region: region,
				})
			})
		},
	}
	cmd.Flags().StringVar(&region, "region", "", i18n.T("Region: auto, cn or global"))
	return cmd
}

func newInstallCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var dir, version, profile, project string
	var driver, name, region, pypiIndex, pythonMirror, imageRepository string
	var extras []string
	var yes bool
	var port, pluginDebugPort int
	var adapterPorts []string
	var dryRun bool
	cmd := &cobra.Command{
		Use: "install", Short: i18n.T("Install LangBot locally"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.contextSet || flags.endpointSet || flags.apiKeyStdin {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("install does not accept context, endpoint or API Key")))
			}
			selected, selectedPort, err := installSelection(cmd.Context(), deps, driver, port, yes)
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, result.New("input", err.Error()))
			}
			if dir == "" && deps.LocalDir == "" {
				dir, err = deploy.InstanceDir(name)
				if err != nil {
					return emitCommand(deps, flags, app.Result{}, result.New("input", err.Error()))
				}
			}
			if !cmd.Flags().Changed("project") && name != "default" {
				project = "langbot-" + strings.ToLower(name)
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Install(cmd.Context(), app.InstallOptions{
					Driver: selected, Name: name, Region: region, PyPIIndex: pypiIndex, PythonMirror: pythonMirror, ImageRepository: imageRepository, Extras: extras,
					Dir: dir, Version: version, Profile: profile, Port: selectedPort, DryRun: dryRun,
					Project: project, PluginDebugPort: &pluginDebugPort, AdapterPorts: adapterPorts,
					Timeout: flags.timeout, TimeoutSet: flags.timeoutSet,
				})
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", i18n.T("Local deployment directory"))
	cmd.Flags().StringVar(&driver, "driver", "", i18n.T("Deployment driver: native (recommended) or docker"))
	cmd.Flags().StringVar(&name, "name", "default", i18n.T("Instance name"))
	cmd.Flags().StringVar(&region, "region", "auto", i18n.T("Network region: auto, cn or global"))
	cmd.Flags().StringVar(&pypiIndex, "pypi-index", "", i18n.T("Python package index URL"))
	cmd.Flags().StringVar(&pythonMirror, "python-mirror", "", i18n.T("Managed Python download mirror"))
	cmd.Flags().StringVar(&imageRepository, "image-repository", "", i18n.T("Docker image registry"))
	cmd.Flags().StringSliceVar(&extras, "extra", nil, i18n.T("Optional dependencies: seekdb,milvus,valkey"))
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("Skip interactive prompts"))
	cmd.Flags().StringVar(&version, "version", "", i18n.T("LangBot stable Release tag; defaults to the latest release"))
	cmd.Flags().StringVar(&profile, "profile", "basic", i18n.T("Deployment profile: basic or all"))
	cmd.Flags().IntVar(&port, "port", 5300, i18n.T("LangBot HTTP host port"))
	cmd.Flags().StringVar(&project, "project", deploy.DefaultComposeProject, i18n.T("Unique Compose project name; use different names for multiple instances"))
	cmd.Flags().IntVar(&pluginDebugPort, "plugin-debug-port", 5401, i18n.T("Plugin Runtime debug host port; 0 disables publishing"))
	cmd.Flags().StringSliceVar(&adapterPorts, "adapter-ports", []string{"2280-2285:2280-2285"}, i18n.T("Adapter HOST:CONTAINER mappings, equal-length ranges and comma-separated values supported; none disables publishing"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check prerequisites and print the installation plan only"))
	return cmd
}

func newAdoptCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var dir, file, project, profile string
	cmd := &cobra.Command{
		Use: "adopt", Short: i18n.T("Adopt an existing local Compose deployment"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.contextSet {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("adopt uses an explicit endpoint and does not accept context")))
			}
			if !flags.endpointSet || strings.TrimSpace(file) == "" || strings.TrimSpace(project) == "" {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("adopt requires --endpoint, --file and --project")))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.Adopt(cmd.Context(), app.AdoptOptions{
					Dir: dir, ComposeFile: file, ComposeProject: project, Endpoint: flags.endpoint, Profile: profile,
					Timeout: flags.timeout, TimeoutSet: flags.timeoutSet, APIKeyStdin: flags.apiKeyStdin,
				})
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", i18n.T("lbctl deployment record directory"))
	cmd.Flags().StringVar(&file, "file", "", i18n.T("Compose file path"))
	cmd.Flags().StringVar(&project, "project", "", i18n.T("Compose project name"))
	cmd.Flags().StringVar(&profile, "profile", "basic", i18n.T("Deployment profile: basic or all"))
	return cmd
}

func newLifecycleCommand(service *app.Service, deps Dependencies, flags *globalFlags, action string) *cobra.Command {
	var name, dir string
	short := map[string]string{"start": i18n.T("Start a managed local deployment"), "stop": i18n.T("Stop a managed local deployment"), "restart": i18n.T("Restart a managed local deployment")}[action]
	command := &cobra.Command{
		Use: action, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options := app.LifecycleOptions{CheckOptions: connectionOptions(flags)}
			options.LocalName, options.LocalDir = name, dir
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
	command.Flags().StringVar(&name, "name", "", i18n.T("Local instance name"))
	command.Flags().StringVar(&dir, "dir", "", i18n.T("Local deployment directory"))
	return command
}

func newUpgradeCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var name, dir string
	var version string
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use: "upgrade", Short: i18n.T("Safely upgrade a managed local deployment"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun && !yes {
				return emitCommand(deps, flags, app.Result{}, result.New("precondition", i18n.T("Upgrade stops and modifies the local deployment; explicitly pass --yes")))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				options := connectionOptions(flags)
				options.LocalName, options.LocalDir = name, dir
				return service.Upgrade(cmd.Context(), app.UpgradeOptions{
					CheckOptions: options, Version: version, DryRun: dryRun, Yes: yes,
				})
			})
		},
	}
	cmd.Flags().StringVar(&version, "version", "", i18n.T("Target LangBot stable Release tag"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check prerequisites and print the upgrade plan only"))
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("Confirm shutdown, backup and upgrade"))
	cmd.Flags().StringVar(&name, "name", "", i18n.T("Local instance name"))
	cmd.Flags().StringVar(&dir, "dir", "", i18n.T("Local deployment directory"))
	return cmd
}

func newUninstallCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var purgeData, yes bool
	var name, dir string
	cmd := &cobra.Command{
		Use: "uninstall", Short: i18n.T("Uninstall a managed local deployment"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return emitCommand(deps, flags, app.Result{}, result.New("precondition", i18n.T("Uninstall deletes managed containers and networks; explicitly pass --yes")))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				options := connectionOptions(flags)
				options.LocalName, options.LocalDir = name, dir
				return service.Uninstall(cmd.Context(), app.UninstallOptions{
					CheckOptions: options, PurgeData: purgeData, Yes: yes,
				})
			})
		},
	}
	cmd.Flags().BoolVar(&purgeData, "purge-data", false, i18n.T("Also remove lbctl-managed data directories"))
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("Confirm uninstall"))
	cmd.Flags().StringVar(&name, "name", "", i18n.T("Local instance name"))
	cmd.Flags().StringVar(&dir, "dir", "", i18n.T("Local deployment directory"))
	return cmd
}

func newLocalLogsCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var serviceName, since string
	var tail int
	var follow bool
	cmd := &cobra.Command{
		Use: "logs", Short: i18n.T("Read managed local deployment logs"), Args: cobra.NoArgs,
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
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("logs --follow does not support YAML output")))
			}
			err := service.FollowLocalLogs(cmd.Context(), options)
			flags.exitCode = result.ExitCode(err)
			if err == nil {
				return nil
			}
			if format == output.FormatJSON {
				envelope := result.Envelope{OK: false, Error: result.AsError(err)}
				if encodeErr := json.NewEncoder(deps.Out).Encode(envelope); encodeErr != nil {
					fmt.Fprintln(deps.Err, i18n.T("Output failed"))
					flags.exitCode = 10
				}
				return nil
			}
			return emitCommand(deps, flags, app.Result{}, err)
		},
	}
	cmd.Flags().StringVar(&serviceName, "service", "", i18n.T("Service name: langbot, langbot_plugin_runtime or langbot_box"))
	cmd.Flags().IntVar(&tail, "tail", 200, i18n.T("Number of trailing log lines, maximum 5000"))
	cmd.Flags().StringVar(&since, "since", "", i18n.T("Read logs only after the specified time"))
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, i18n.T("Follow logs"))
	return cmd
}

func newVersionCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var server bool
	cmd := &cobra.Command{
		Use: "version", Short: i18n.T("Show CLI version"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !server && hasConnectionOverride(flags) {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("Offline version does not accept connection overrides")))
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
	cmd.Flags().BoolVar(&server, "server", false, i18n.T("Also query the server version"))
	return cmd
}

func newRawCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use: "raw [method path]", Short: i18n.T("Read controlled basic service information"), Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("raw requires both method and path")))
			}
			if file == "-" && flags.apiKeyStdin {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("--file - cannot be combined with --api-key-stdin")))
			}
			if cmd.Flags().Changed("file") {
				return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("Stage 1A raw supports only GET without a request body")))
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
	cmd.Flags().StringVar(&file, "file", "", i18n.T("Request body input; stage 1A does not allow request bodies"))
	return cmd
}

func newAPICommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	command := &cobra.Command{Use: "api", Short: i18n.T("Call a registered HTTP operation"), Args: cobra.NoArgs}
	get := &cobra.Command{
		Use: "get <path>", Short: i18n.T("Call a controlled GET endpoint"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			operation, ok := api.ResolveOperation("GET", args[0])
			if !ok || operation.Method != http.MethodGet {
				return emitCommand(deps, flags, app.Result{}, result.New("incompatible", i18n.T("GET path is not registered")))
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.APIRequest(cmd.Context(), http.MethodGet, args[0], nil, connectionOptions(flags))
			})
		},
	}
	command.AddCommand(get)
	var file string
	post := &cobra.Command{
		Use: "post <path>", Short: i18n.T("Call a controlled POST endpoint"), Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			operation, ok := api.ResolveOperation("POST", args[0])
			if !ok || !operation.ReadOnly {
				return emitCommand(deps, flags, app.Result{}, result.New("incompatible", i18n.T("POST path is not registered")))
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
	post.Flags().StringVar(&file, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin"))
	command.AddCommand(post)
	return command
}

func newIdentityCommand(service *app.Service, deps Dependencies, flags *globalFlags, name string) *cobra.Command {
	return &cobra.Command{
		Use: name, Short: name + i18n.T(" (server-discovered)"), Args: cobra.NoArgs,
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
	showNotice(deps, flags)
	flags.operationError = err
	flags.exitCode = emit(deps, flags, value, err)
	return nil
}

func emitCall(deps Dependencies, flags *globalFlags, call func() (app.Result, error)) error {
	noBrand, _ := deps.LookupEnv("LBCTL_NO_BRAND")
	if noBrand != "1" && flags.output != "json" && flags.output != "yaml" && (flags.commandPath == "install" || flags.commandPath == "upgrade") {
		progress.Brand(deps.Err)
	}
	showNotice(deps, flags)
	if flags.display != nil {
		flags.display.Step("Working")
	}
	value, err := call()
	if flags.display != nil {
		flags.display.Finish(err == nil)
	}
	return emitCommand(deps, flags, value, err)
}

func showNotice(deps Dependencies, flags *globalFlags) {
	if flags.notice == "" {
		return
	}
	if flags.noticeMachine {
		fmt.Fprint(deps.Err, flags.notice)
	} else {
		for _, line := range strings.Split(strings.TrimSpace(flags.notice), "\n") {
			progress.Dim(deps.Err, "ℹ "+line)
		}
		fmt.Fprintln(deps.Err)
	}
	flags.notice = ""
}

func emit(deps Dependencies, flags *globalFlags, value app.Result, callErr error) int {
	if callErr == nil {
		value, callErr = applyListView(value, flags.listView)
	}
	value = enrichResult(deps, flags, value, callErr)
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
		fmt.Fprintln(deps.Err, i18n.T("Output failed"))
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
		return result.New("input", i18n.T("This local context command does not accept --api-key-stdin"))
	}
	if flags.endpointSet || flags.contextSet || flags.timeoutSet {
		return result.New("input", i18n.T("This local context command does not accept connection overrides"))
	}
	return nil
}

func hasConnectionOverride(flags *globalFlags) bool {
	return flags.contextSet || flags.endpointSet || flags.timeoutSet || flags.apiKeyStdin
}

type listViewOptions struct{ key, filter string }

func resourceListKey(command string) string {
	return map[string]string{"bot.list": "bots", "pipeline.list": "pipelines", "provider.list": "providers", "model.list": "models", "knowledge-base.list": "bases", "plugin.list": "plugins", "skill.list": "skills", "mcp-server.list": "servers", "task.list": "tasks"}[command]
}

func configureListFilter(cmd *cobra.Command, flags *globalFlags) {
	key := resourceListKey(commandPath(cmd))
	if key == "" {
		return
	}
	var filter string
	cmd.Flags().StringVar(&filter, "filter", "", i18n.T("Filter by name or ID substring (client-side; does not reduce server transfer)"))
	original := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		flags.listView = listViewOptions{key: key, filter: filter}
		return original(c, args)
	}
}

func applyListView(value app.Result, options listViewOptions) (app.Result, error) {
	if options.key == "" || options.filter == "" || value.Data == nil {
		return value, nil
	}
	encoded, err := json.Marshal(value.Data)
	if err != nil {
		return value, result.New("internal", i18n.T("Unable to organize resource list"))
	}
	var data map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err = decoder.Decode(&data); err != nil {
		return value, result.New("internal", i18n.T("Invalid resource list format"))
	}
	total, matched, shown := 0, 0, 0
	selectRows := func(items []any) []any {
		rows := []any{}
		for _, item := range items {
			total++
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			match := options.filter == ""
			for _, field := range []string{"name", "display_name", "uuid", "id", "file_name"} {
				if raw, exists := row[field]; exists && strings.Contains(strings.ToLower(fmt.Sprint(raw)), strings.ToLower(options.filter)) {
					match = true
				}
			}
			if !match {
				continue
			}
			matched++
			rows = append(rows, item)
			shown++
		}
		return rows
	}
	switch items := data[options.key].(type) {
	case []any:
		data[options.key] = selectRows(items)
	case map[string]any:
		for _, kind := range []string{"llm", "embedding", "rerank"} {
			if rows, ok := items[kind].([]any); ok {
				items[kind] = selectRows(rows)
			}
		}
	default:
		return value, nil
	}
	if value.Meta == nil {
		value.Meta = map[string]any{}
	}
	value.Meta["list_selection"] = map[string]any{"scope": "client", "total": total, "matched": matched, "shown": shown}
	value.Data = data
	return value, nil
}

func enrichResult(deps Dependencies, flags *globalFlags, value app.Result, err error) app.Result {
	if flags.commandPath == "schema" || flags.commandPath == "version" || strings.HasPrefix(flags.commandPath, "completion") {
		return value
	}
	meta := map[string]any{}
	for key, item := range value.Meta {
		meta[key] = item
	}
	path, pathErr := selectedConfigPath(deps, flags)
	if strings.HasPrefix(flags.commandPath, "context.") || flags.commandPath == "status" || flags.commandPath == "doctor" {
		if pathErr == nil {
			meta["config_file"] = path
		}
	}
	base := []string{"lbctl"}
	if pathErr == nil && (flags.config != "" || hasEnvironmentConfig(deps)) {
		base = append(base, "--config", path)
	}
	connect := append([]string(nil), base...)
	if flags.context != "" {
		connect = append(connect, "--context", flags.context)
	} else if name, ok := meta["context"].(string); ok && name != "" {
		connect = append(connect, "--context", name)
	}
	if endpoint, ok := meta["endpoint"].(string); flags.endpoint != "" && ok && endpoint != "" {
		connect = append(connect, "--endpoint", endpoint)
	}
	if _, err := time.ParseDuration(flags.timeout); flags.timeout != "" && err == nil {
		connect = append(connect, "--timeout", flags.timeout)
	}
	steps := []map[string]any{}
	add := func(label string, prefix []string, args ...string) {
		argv := append(append([]string(nil), prefix...), args...)
		steps = append(steps, map[string]any{"description": label, "argv": argv})
	}
	data, _ := value.Data.(map[string]any)
	if err != nil {
		e := result.AsError(err)
		switch e.Kind {
		case "auth":
			if e.Type == "requires_member_session" || e.Type == "password_login_disabled" {
				add(i18n.T("Import a valid member session"), connect, "auth", "set-token", "--stdin")
			} else {
				add(i18n.T("Verify credentials for the current target"), connect, "whoami")
			}
		case "permission", "incompatible":
			add(i18n.T("Show capabilities and permissions supported by current target"), connect, "capabilities")
		case "precondition":
			if flags.commandPath == "install" {
				label := i18n.T("Check installation prerequisites and arguments")
				if data["step"] == "check_ports" {
					label = i18n.T("Free conflicting ports, or change the HTTP port with --port")
					if port := data["conflict_port"]; port == 5401 || port == 5410 {
						label = i18n.T("Free ports 5401/5410, or use --driver docker")
					}
				}
				add(label, base, "install", "--help")
			} else if e.Message == i18n.T("Server has not explicitly confirmed support for this operation") || e.Message == i18n.T("Server explicitly does not support this operation") {
				add(i18n.T("Show capabilities supported by current target"), connect, "capabilities")
			} else {
				add(i18n.T("Check target and Workspace binding"), connect, "context", "show")
			}
		case "network", "result_unknown":
			if id, ok := data["task_id"].(string); ok && id != "" {
				add(i18n.T("Query a submitted task without resubmitting"), connect, "task", "get", id)
			} else if id, ok := data["uuid"].(string); ok && id != "" && strings.HasPrefix(flags.commandPath, "bot.") {
				add(i18n.T("Read back resource to confirm the result"), connect, "bot", "get", id)
			} else {
				add(i18n.T("Diagnose the current target"), connect, "doctor")
			}
		case "input":
			path := flags.commandPath
			if path == "lbctl" {
				path = ""
			}
			add(i18n.T("Show argument and request body examples"), base, append(strings.Fields(strings.ReplaceAll(path, ".", " ")), "--help")...)
		case "not_found":
			family := strings.Split(flags.commandPath, ".")[0]
			if resourceListKey(family+".list") != "" {
				add(i18n.T("List resources on the current target"), connect, family, "list")
			}
		}
		if flags.commandPath == "install" {
			if retry, ok := data["retry_argv"].([]string); ok {
				add(i18n.T("Fix the failed step and retry installation"), base, retry...)
			}
			if rollback, ok := data["rollback_argv"].([]string); ok {
				add(i18n.T("Uninstall services and preserve data"), base, rollback...)
			}
		}
		if flags.commandPath == "upgrade" {
			steps = nil
			step, _ := data["step"].(string)
			label, command := upgradeRecovery(step)
			args := []string{command}
			prefix := base
			if endpoint, ok := data["recovery_endpoint"].(string); ok && endpoint != "" && command != "upgrade" {
				prefix = append(append([]string(nil), base...), "--endpoint", endpoint)
			}
			if command == "upgrade" {
				args = append(args, "--help")
			}
			add(i18n.T(label), prefix, args...)
		}
	} else {
		switch flags.commandPath {
		case "context.add", "context.update":
			if name, ok := data["name"].(string); ok {
				add(i18n.T("Select this environment"), base, "context", "use", name)
			}
		case "context.use":
			add(i18n.T("Check the selected instance"), base, "status")
		case "context.remove":
			add(i18n.T("Show remaining environments"), base, "context", "list")
		}
		if data["submitted"] == true && data["step"] != "completed" {
			if id, ok := data["task_id"].(string); ok && id != "" {
				add(i18n.T("Track task progress"), connect, "task", "get", id, "--wait")
			}
		}
	}
	if len(steps) > 0 {
		meta["next_steps"] = steps
	}
	if len(meta) > 0 {
		value.Meta = meta
	}
	return value
}

func hasEnvironmentConfig(deps Dependencies) bool {
	_, ok := deps.LookupEnv("LANGBOT_CONFIG")
	return ok
}

func newSkillPushCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	var directory, revision string
	var dryRun bool
	command := &cobra.Command{
		Use: "push <name>", Short: i18n.T("Publish a complete local Skill directory"), Args: cobra.ExactArgs(1),
		Example: "  lbctl skill get demo -o json\n  lbctl skill push demo --dir ./demo --base-revision <revision> --dry-run\n  lbctl skill push demo --dir ./demo --base-revision <revision>",
		RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.SkillPush(cmd.Context(), args[0], directory, revision, dryRun, connectionOptions(flags))
			})
		},
	}
	command.Flags().StringVar(&directory, "dir", "", i18n.T("Local Skill directory; packaged as ZIP before upload"))
	command.Flags().StringVar(&revision, "base-revision", "", i18n.T("Reviewed revision required for complete package replacement"))
	command.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Validate the package and preview added, modified and deleted files"))
	_ = command.MarkFlagRequired("dir")
	_ = command.MarkFlagRequired("base-revision")
	return command
}
