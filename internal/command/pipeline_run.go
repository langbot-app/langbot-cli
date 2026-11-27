package command

import (
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/spf13/cobra"
)

func addPipelineRun(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var message string
	var runDry bool
	run := &cobra.Command{Use: "run <uuid>", Short: i18n.T("Test Chat in a new session; invokes models and configured tools, does not retry on timeout"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PipelineRun(cmd.Context(), args[0], message, runDry, connectionOptions(flags))
		})
	}}
	run.Flags().StringVar(&message, "message", "", i18n.T("Input text; each call creates a new session without reusing history"))
	run.Flags().BoolVar(&runDry, "dry-run", false, i18n.T("Check prerequisites without executing the Chat"))
	root.AddCommand(run)
}
