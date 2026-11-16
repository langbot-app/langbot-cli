package command

import (
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

func newAutostartCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	group := &cobra.Command{Use: "autostart", Short: "管理开机/登录自启 / Manage startup"}
	for _, action := range []string{"status", "enable", "disable"} {
		var name, dir string
		command := &cobra.Command{Use: action, Short: "自启 / Autostart " + action, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return emitCall(deps, flags, func() (app.Result, error) {
					if flags.contextSet || flags.endpointSet || flags.apiKeyStdin {
						return app.Result{}, result.New("input", "autostart is local; use --name or --dir, not connection flags")
					}
					return service.Autostart(cmd.Context(), action, name, dir, flags.timeout)
				})
			},
		}
		command.Flags().StringVar(&name, "name", "", "本机实例名称 / Instance name")
		command.Flags().StringVar(&dir, "dir", "", "本机部署目录 / Instance directory")
		group.AddCommand(command)
	}
	return group
}
