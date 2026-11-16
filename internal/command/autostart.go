package command

import (
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

func newAutostartCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	group := &cobra.Command{Use: "autostart", Short: i18n.T("Manage startup")}
	for _, action := range []string{"status", "enable", "disable"} {
		var name, dir string
		command := &cobra.Command{Use: action, Short: i18n.T("Autostart ") + action, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return emitCall(deps, flags, func() (app.Result, error) {
					if flags.contextSet || flags.endpointSet || flags.apiKeyStdin {
						return app.Result{}, result.New("input", i18n.T("autostart is local; use --name or --dir, not connection flags"))
					}
					return service.Autostart(cmd.Context(), action, name, dir, flags.timeout)
				})
			},
		}
		command.Flags().StringVar(&name, "name", "", i18n.T("Instance name "))
		command.Flags().StringVar(&dir, "dir", "", i18n.T("Instance directory"))
		group.AddCommand(command)
	}
	return group
}
