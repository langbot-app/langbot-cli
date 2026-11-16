package command

import (
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/spf13/cobra"
)

func addPermissionCommand(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var capabilities, operations []string
	var yes bool
	cmd := &cobra.Command{Use: "confirm <author> <name>", Short: "确认插件声明权限并启用；默认全部，compat 模式总是全部", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
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
	cmd.Flags().StringArrayVar(&capabilities, "capability", nil, "授予 capability ID，可重复")
	cmd.Flags().StringArrayVar(&operations, "operation", nil, "授予 workspace operation ID，可重复")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认授予权限")
	commandGroup(root, []string{"plugin", "permissions"}).AddCommand(cmd)
}
