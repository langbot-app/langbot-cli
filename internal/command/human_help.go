package command

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func helpHint(cmd *cobra.Command) string {
	path := "lbctl"
	if cmd != nil {
		path = cmd.CommandPath()
	}
	return i18n.T("Run ") + path + i18n.T(" --help for usage")
}

func humanHelpRequest(root, selected *cobra.Command, parseErr error) bool {
	if selected == nil || parseErr != nil {
		return false
	}
	if help, _ := selected.Flags().GetBool("help"); help {
		return true
	}
	args := selected.Flags().Args()
	if selected == root {
		return len(args) == 0
	}
	if selected.Name() != "help" {
		return false
	}
	_, remaining, err := root.Find(args)
	return err == nil && len(remaining) == 0
}

// Use static metadata, never raw parser errors, which can include secret values.
func commandInputError(cmd *cobra.Command) error {
	if cmd == nil {
		return result.New("input", i18n.T("Invalid arguments; ")+helpHint(cmd))
	}
	missing := []string{}
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		if required := flag.Annotations[cobra.BashCompOneRequiredFlag]; len(required) > 0 && required[0] == "true" && !flag.Changed {
			missing = append(missing, "--"+flag.Name)
		}
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return result.New("input", i18n.T("Missing required option ")+strings.Join(missing, i18n.Punct("、"))+i18n.Punct("；")+helpHint(cmd))
	}
	return result.New("input", i18n.T("Invalid arguments; ")+helpHint(cmd))
}

func commandFlagError(cmd *cobra.Command, cause error) error {
	message := i18n.T("Unknown option or invalid option format")
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		name := "--" + flag.Name
		if cause.Error() == "flag needs an argument: "+name || (flag.Shorthand != "" && cause.Error() == "flag needs an argument: -"+flag.Shorthand) {
			message = name + i18n.T(" requires a value")
		} else if strings.Contains(cause.Error(), `for "`+name+`" flag:`) {
			message = name + i18n.T(" has an invalid value (type:") + flag.Value.Type() + i18n.Punct("）")
		}
	})
	return result.New("input", message+i18n.Punct("；")+helpHint(cmd))
}

func configureHumanHelp(root *cobra.Command) {
	root.SetUsageTemplate(strings.NewReplacer(
		".LocalFlags.FlagUsages", "humanFlags . false", ".InheritedFlags.FlagUsages", "humanFlags . true",
		"Usage:", i18n.T("Usage:"), "Aliases:", i18n.T("Aliases:"), "Examples:", i18n.T("Examples:"),
		"Available Commands:", i18n.T("Available commands:"), "Additional Commands:", i18n.T("Other commands:"),
		"Global Flags:", i18n.T("Global options:"), "Flags:", i18n.T("Options:"),
		"Additional help topics:", i18n.T("Additional help topics:"),
		`Use "{{.CommandPath}} [command] --help" for more information about a command.`,
		i18n.T("Run \"{{.CommandPath}} <command> --help\" for command help."),
		"[command]", i18n.T("<command>"),
	).Replace(root.UsageTemplate()))
	root.SetHelpCommand(&cobra.Command{
		Use: "help [command]", Short: i18n.T("Show command help"), Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			target, remaining, err := root.Find(args)
			if err != nil || len(remaining) != 0 {
				return result.New("input", i18n.T("Unknown help topic; ")+helpHint(root))
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
			flag.Usage = i18n.T("Show help for this command")
		}
		switch cmd.Name() {
		case "help":
			cmd.Short = i18n.T("Show command help")
			cmd.Long = i18n.T("Show help for any command, e.g. lbctl help bot update.")
		case "completion":
			cmd.Short = i18n.T("Generate shell completion scripts")
			cmd.Long = i18n.T("Generate a completion script for the specified shell.")
		case "whoami":
			cmd.Short = i18n.T("Show current instance, Workspace and permissions")
		case "capabilities":
			cmd.Short = i18n.T("Show operation capabilities declared by Core")
		}
		if cmd.Parent() != nil && cmd.Parent().Name() == "completion" {
			cmd.Short = i18n.T("Generate ") + cmd.Name() + i18n.T(" completion script")
			cmd.Long = cmd.Short + i18n.T(". Save the output and load it in the corresponding shell.")
		}
		if original := cmd.Args; original != nil {
			cmd.Args = func(command *cobra.Command, args []string) error {
				if err := original(command, args); err != nil {
					var typed *result.Error
					if errors.As(err, &typed) {
						return err
					}
					message := i18n.T("Incorrect number of positional arguments")
					required := []string{}
					for _, field := range strings.Fields(command.Use)[1:] {
						if strings.HasPrefix(field, "<") && strings.HasSuffix(field, ">") {
							required = append(required, field)
						}
					}
					if len(args) < len(required) {
						message = i18n.T("Missing positional argument ") + strings.Join(required[len(args):], " ")
					}
					return result.New("input", message+i18n.T("; usage: ")+command.UseLine()+i18n.Punct("；")+helpHint(command))
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
			return "", result.New("input", i18n.T("LANGBOT_CONFIG must not be empty"))
		}
		return path, nil
	}
	return deps.DefaultConfigPath()
}

var helpGroups = []struct {
	title string
	names []string
}{
	{i18n.Source("Connection and diagnostics"), []string{"context", "status", "doctor", "whoami", "capabilities"}},
	{i18n.Source("Resources and content"), []string{"bot", "pipeline", "provider", "model", "knowledge-base", "knowledge-engine", "knowledge-parser", "plugin", "skill", "mcp-server"}},
	{i18n.Source("Operations and troubleshooting"), []string{"monitoring", "sandbox", "task", "logs"}},
	{i18n.Source("Local deployment"), []string{"list", "install", "adopt", "start", "stop", "restart", "upgrade", "uninstall", "update"}},
	{i18n.Source("Tools and automation"), []string{"schema", "api", "raw", "completion", "version", "help"}},
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
			cmd.Long += i18n.T("\n--dry-run performs only the declared checks without writing; passing preflight does not guarantee business validation or execution success.")
		}
		configureListFilter(cmd, flags)
		if example, exists := commandExamples[name]; exists {
			cmd.Example = i18n.T(example)
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
					return result.New("input", i18n.T("task ID must be a non-negative integer; run lbctl task list to find task IDs"))
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
		root.AddGroup(&cobra.Group{ID: id, Title: i18n.T(group.title) + i18n.Human(":")})
		for _, cmd := range root.Commands() {
			for _, name := range group.names {
				if cmd.Name() == name {
					cmd.GroupID = id
				}
			}
		}
	}
	root.Long = i18n.T("LangBot service management CLI. Default output is human-readable; use -o json or -o yaml for agents/scripts.\nFor existing instances, start with context list and status; adopt local deployments before managing their lifecycle.")
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
			copy.Usage += i18n.T(" (required)")
		}
		if condition := meta.FlagApplicability[flag.Name]; condition.Status == "conditional" {
			copy.Usage += " (" + i18n.T(condition.Reason) + ")"
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
		if strings.HasPrefix(command, "model.") {
			return []string{"llm", "embedding", "rerank", "asr", "tts"}
		}
		if command == "provider.scan-models" {
			return []string{"llm", "embedding", "rerank"}
		}
	case "active":
		return []string{"true", "false"}
	}
	return nil
}

var commandExamples = map[string]string{
	"agent.debug":    "  lbctl agent debug <agent-uuid> --message 'Hello' --yes\n  lbctl agent debug <agent-uuid> --file debug.json --yes",
	"context.list":   "  lbctl context list\n  lbctl context list --discover\n  lbctl --config ./config.yaml context list",
	"context.show":   "  lbctl context show\n  lbctl context show production",
	"context.add":    "  lbctl context add local --endpoint http://127.0.0.1:5300 --api-key-env LANGBOT_API_KEY\n  lbctl --context local whoami\n  lbctl context update local --expect-workspace <workspace-id>\n  lbctl context use local",
	"context.use":    "  lbctl context use local\n  lbctl status\n  lbctl bot list",
	"schema":         "  lbctl schema --summary\n  lbctl schema --command bot\n  lbctl schema --command 'bot update'",
	"bot.list":       "  lbctl bot list\n  lbctl bot list --filter LangBot\n  lbctl bot list -o json",
	"bot.get":        i18n.Source("  lbctl bot get <bot-id>\n  # Inspect event_bindings for the bound Agent or Pipeline\n  lbctl agent get <agent-id>\n  lbctl pipeline get <pipeline-id>"),
	"bot.update":     i18n.Source("  # Minimal request.yaml: {\"name\":\"my-bot\"}\n  lbctl bot update <bot-id> --file request.yaml --dry-run\n  lbctl bot update <bot-id> --file request.yaml\n  # Get adapter_config before editing to preserve needed values; *** is redacted."),
	"pipeline.apply": i18n.Source("  # Update example: {\"uuid\":\"<pipeline-id>\",\"name\":\"my-pipeline\"}\n  # Without uuid, creates a pipeline; config depends on the target Core version.\n  lbctl pipeline apply --file pipeline.yaml --dry-run\n  lbctl pipeline apply --file pipeline.yaml"),
	"model.list":     "  lbctl model list --filter deepseek\n  lbctl model list --type llm --provider <provider-id>\n  lbctl model list -o json",
	"task.get":       "  lbctl task get 42\n  lbctl task get 42 --wait --wait-timeout 5m",
}
