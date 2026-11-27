package command

import (
	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
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
	extensions := &cobra.Command{Use: "extensions", Short: i18n.T("Manage Chat extension bindings"), Args: cobra.NoArgs}
	extensions.AddCommand(&cobra.Command{Use: "get <uuid>", Short: i18n.T("Read bindings, available extensions and enable_all toggles"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := api.ValidateResourceID(args[0]); err != nil {
			return err
		}
		return pipeline_extensionsGet(cmd, service, deps, flags, "/api/v1/pipelines/"+url.PathEscape(args[0])+"/extensions")
	}})
	var file string
	var dryRun bool
	update := &cobra.Command{Use: "update <uuid>", Short: i18n.T("Replace all extension bindings (all bindings and toggles required)"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		body, err := loadRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
		if err != nil {
			return err
		}
		return emitCall(deps, flags, func() (app.Result, error) {
			return service.PipelineExtensionsUpdate(cmd.Context(), args[0], body, dryRun, connectionOptions(flags))
		})
	}}
	update.Flags().StringVar(&file, "file", "", i18n.T("JSON/YAML request file; - reads stdin"))
	update.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check prerequisites without writing"))
	extensions.AddCommand(update)
	root.AddCommand(extensions)
}
