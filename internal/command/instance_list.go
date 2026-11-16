package command

import (
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

func newInstanceListCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	return &cobra.Command{Use: "list", Short: i18n.T("List all local LangBot instances"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				if flags.contextSet || flags.endpointSet || flags.apiKeyStdin {
					return app.Result{}, result.New("input", i18n.T("list reads local instances; connection overrides are not supported"))
				}
				return service.ListInstances(cmd.Context(), flags.timeout)
			})
		},
	}
}
