package command

import (
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/spf13/cobra"
)

func addPermissionCommand(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var capabilities, operations []string
	var yes bool
	cmd := &cobra.Command{Use: "confirm <author> <name>", Short: i18n.T("Confirm declared plugin permissions and enable; defaults to all, compat mode always grants all"), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			if err := confirmOperation(deps, yes, flags.apiKeyStdin); err != nil {
				return app.Result{}, err
			}
			session, err := service.OpenOperations(cmd.Context(), connectionOptions(flags), false)
			if err != nil {
				return app.Result{}, err
			}
			value, err := session.ConfirmPlugin(cmd.Context(), args[0], args[1], capabilities, operations)
			return session.Result(value), err
		})
	}}
	cmd.Flags().StringArrayVar(&capabilities, "capability", nil, i18n.T("Grant capability ID; repeatable"))
	cmd.Flags().StringArrayVar(&operations, "operation", nil, i18n.T("Grant workspace operation ID; repeatable"))
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("Confirm permission grants"))
	commandGroup(root, []string{"plugin", "permissions"}).AddCommand(cmd)
}
