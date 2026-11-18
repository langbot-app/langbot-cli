package command

import (
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/spf13/cobra"
)

func newSystemConfigCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	group := &cobra.Command{Use: "sysconfig", Short: i18n.T("Manage local Core configuration")}
	var dir, name string
	group.PersistentFlags().StringVar(&dir, "dir", "", i18n.T("Local deployment directory"))
	group.PersistentFlags().StringVar(&name, "instance", "", i18n.T("Instance name"))
	group.PersistentFlags().StringVar(&name, "name", "", i18n.T("Instance name"))
	for _, spec := range []struct {
		action, use string
		args        cobra.PositionalArgs
	}{{"path", "path", cobra.NoArgs}, {"get", "get [key]", cobra.MaximumNArgs(1)}, {"set", "set <key> <value>", cobra.ExactArgs(2)}, {"unset", "unset <key>", cobra.ExactArgs(1)}, {"edit", "edit", cobra.NoArgs}} {
		var yes bool
		options := app.SystemConfigOptions{Action: spec.action}
		write := spec.action != "path" && spec.action != "get"
		command := &cobra.Command{Use: spec.use, Args: spec.args, Short: i18n.T("Manage local Core configuration"), RunE: func(cmd *cobra.Command, args []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				if write && !options.DryRun {
					if err := confirmOperation(deps, yes, flags.apiKeyStdin); err != nil {
						return app.Result{}, err
					}
				}
				options.CheckOptions = connectionOptions(flags)
				flags.showConfigSecrets = options.Secrets
				options.LocalDir, options.LocalName = dir, name
				if len(args) > 0 {
					options.Key = args[0]
				}
				if len(args) > 1 {
					options.Value = args[1]
				}
				return service.SystemConfig(cmd.Context(), options)
			})
		}}
		if write {
			command.Flags().BoolVar(&yes, "yes", false, i18n.T("Skip interactive prompts"))
			command.Flags().BoolVar(&options.DryRun, "dry-run", false, i18n.T("Preview configuration changes"))
			command.Flags().BoolVar(&options.Restart, "restart", false, i18n.T("Restart the running instance after saving"))
		}
		if spec.action == "get" {
			command.Flags().BoolVar(&options.Secrets, "show-secrets", false, i18n.T("Show unmasked configuration values"))
		}
		group.AddCommand(command)
	}
	return group
}
