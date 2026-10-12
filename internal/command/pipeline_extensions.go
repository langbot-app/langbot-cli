package command

import (
	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/spf13/cobra"
	"net/http"
	"net/url"
)

func pipeline_extensionsGet(cmd *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags, path string) error {
	return emitCall(deps, flags, func() (app.Result, error) {
		return service.APIRequest(cmd.Context(), http.MethodGet, path, nil, connectionOptions(flags))
	})
}
func addPipelineExtensions(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	extensions := &cobra.Command{Use: "extensions", Short: "管理 Pipeline 扩展绑定", Args: cobra.NoArgs}
	extensions.AddCommand(&cobra.Command{Use: "get <uuid>", Short: "读取绑定、可选扩展与 enable_all 开关", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := api.ValidateResourceID(args[0]); err != nil {
			return err
		}
		return pipeline_extensionsGet(cmd, service, deps, flags, "/api/v1/pipelines/"+url.PathEscape(args[0])+"/extensions")
	}})
	var file string
	var dryRun bool
	update := &cobra.Command{Use: "update <uuid>", Short: "完整替换扩展绑定（所有绑定和开关字段必填）", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
		if err != nil {
			return err
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PipelineExtensionsUpdate(cmd.Context(), args[0], body, dryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&file, "file", "", "JSON/YAML 请求文件；- 读取 stdin")
	update.Flags().BoolVar(&dryRun, "dry-run", false, "只检查前置条件，不写入")
	extensions.AddCommand(update)
	root.AddCommand(extensions)
}
