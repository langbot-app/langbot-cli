package command

import (
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/spf13/cobra"
)

func addPipelineRun(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var message string
	var runDry bool
	run := &cobra.Command{Use: "run <uuid>", Short: "新会话试运行 Pipeline；会调用模型和已配置工具，超时不自动重发", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PipelineRun(cmd.Context(), args[0], message, runDry, connectionOptions(flags))
		})
	}}
	run.Flags().StringVar(&message, "message", "", "本轮输入文本；每次调用新建会话，不复用历史")
	run.Flags().BoolVar(&runDry, "dry-run", false, "只检查前置条件，不执行 Pipeline")
	root.AddCommand(run)
}
