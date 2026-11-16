package command

import (
	"net/http"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

func sandboxGet(cmd *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags, path string) error {
	return emitCall(deps, flags, func() (app.Result, error) {
		return service.APIRequest(cmd.Context(), http.MethodGet, path, nil, connectionOptions(flags))
	})
}
func newSandboxCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	root := &cobra.Command{Use: "sandbox", Short: i18n.T("Query and manage the current Workspace sandbox"), Args: cobra.NoArgs}
	for _, kind := range []string{"status", "sessions", "errors"} {
		root.AddCommand(&cobra.Command{Use: kind, Short: i18n.T("Query sandbox ") + kind, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return sandboxGet(cmd, service, deps, flags, "/api/v1/box/"+kind)
		}})
	}
	root.AddCommand(newSandboxConfigCommand(service, deps, flags))
	return root
}

func newSandboxConfigCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	root := &cobra.Command{Use: "config", Short: i18n.T("Manage workspace sandbox backend, E2B connection and resource limits"), Args: cobra.NoArgs}
	var refresh bool
	get := &cobra.Command{Use: "get", Short: i18n.T("Query saved/effective configuration and backend capabilities"), Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.SandboxConfig(cmd.Context(), "get", nil, refresh, false, false, connectionOptions(flags))
			})
		}}
	get.Flags().BoolVar(&refresh, "refresh", false, i18n.T("Refresh backend availability without creating a sandbox"))
	root.AddCommand(get)
	for _, action := range []string{"update", "check", "test"} {
		var file, keyEnv string
		var dryRun, yes bool
		cmd := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			var body map[string]any
			var err error
			if action == "update" {
				body, err = loadRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
			} else {
				body, err = loadOptionalRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
			}
			if err != nil {
				return emitCommand(deps, flags, app.Result{}, err)
			}
			if cmd.Flags().Changed("e2b-api-key-env") {
				key, found := deps.LookupEnv(keyEnv)
				if !found || strings.TrimSpace(key) == "" {
					return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("E2B API Key environment variable is unset or empty")))
				}
				e2b := map[string]any{}
				if raw := body["e2b"]; raw != nil {
					var ok bool
					e2b, ok = raw.(map[string]any)
					if !ok {
						return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("e2b must be an object")))
					}
				}
				if _, exists := e2b["api_key"]; exists {
					return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("Cannot supply E2B credentials through both body api_key and --e2b-api-key-env")))
				}
				e2b["api_key"], body["e2b"] = strings.TrimSpace(key), e2b
			}
			return emitCall(deps, flags, func() (app.Result, error) {
				return service.SandboxConfig(cmd.Context(), action, body, false, dryRun, yes, connectionOptions(flags))
			})
		}}
		switch action {
		case "update":
			cmd.Short = i18n.T("Save workspace sandbox settings and verify by reading back")
			cmd.Long = i18n.T("Save backend, E2B connection and resource limits; requires --yes. Only new tasks are affected; existing tasks and MCP connections retain their snapshots. Core preserves omitted E2B credentials and validates whether changing URLs requires new credentials.")
			cmd.Example = i18n.Punct("  # sandbox.yaml：{\"backend\":\"docker\",\"limits\":{\"timeout_sec\":60}}\n  lbctl sandbox config update --file sandbox.yaml --dry-run\n  lbctl sandbox config update --file sandbox.yaml --yes")
		case "check":
			cmd.Short = i18n.T("Check draft E2B connectivity without saving or creating a sandbox")
			cmd.Long = i18n.T("Check connectivity using draft or current E2B settings without saving or creating a sandbox. Draft format: {e2b:{api_url,sandbox_url,template}}; inject credentials with --e2b-api-key-env.")
			cmd.Example = "  lbctl sandbox config check\n  lbctl sandbox config check --file e2b.yaml --e2b-api-key-env E2B_API_KEY"
		case "test":
			cmd.Short = i18n.T("Explicitly create, execute and clean up a temporary E2B sandbox")
			cmd.Long = i18n.T("Test consumes E2B quota and may incur charges; requires --yes. Core creates a temporary sandbox, executes verification and deletes it without saving configuration. Test or cleanup failure returns nonzero and preserves diagnostics. Use --timeout 2m to allow creation and cleanup; after a timeout inspect server state before retrying.")
			cmd.Example = "  lbctl --timeout 2m sandbox config test --file e2b.yaml --e2b-api-key-env E2B_API_KEY --yes"
		}
		cmd.Flags().StringVar(&file, "file", "", i18n.T("JSON/YAML request body file; use - to read stdin; optional for check/test"))
		cmd.Flags().StringVar(&keyEnv, "e2b-api-key-env", "", i18n.T("Read E2B API Key from the specified environment variable; do not save in CLI context"))
		if action != "check" {
			cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("Confirm saving configuration or running a potentially billable temporary sandbox test"))
			cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Check prerequisites only; do not save or test a temporary sandbox"))
		}
		root.AddCommand(cmd)
	}
	return root
}
