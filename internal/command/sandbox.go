package command

import (
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/spf13/cobra"
	"net/http"
)

func sandboxGet(cmd *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags, path string) error {
	return emitCall(deps, flags, func() (app.Result, error) {
		return service.APIRequest(cmd.Context(), http.MethodGet, path, nil, connectionOptions(flags))
	})
}
func newSandboxCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	root := &cobra.Command{Use: "sandbox", Short: "只读查询当前 Workspace sandbox", Args: cobra.NoArgs}
	for _, kind := range []string{"status", "sessions", "errors"} {
		root.AddCommand(&cobra.Command{Use: kind, Short: "查询 sandbox " + kind, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return sandboxGet(cmd, service, deps, flags, "/api/v1/box/"+kind)
		}})
	}
	return root
}
