package command

import (
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/spf13/cobra"
	"net/http"
	"net/url"
)

func ragGet(cmd *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags, path string) error {
	return emitCall(deps, flags, func() (app.Result, error) {
		return service.APIRequest(cmd.Context(), http.MethodGet, path, nil, connectionOptions(flags))
	})
}
func newKnowledgeEngineCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	root := &cobra.Command{Use: "knowledge-engine", Short: i18n.T("Query knowledge engines and configuration schema"), Args: cobra.NoArgs}
	root.AddCommand(&cobra.Command{Use: "list", Short: i18n.T("List knowledge engines and capabilities"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return ragGet(cmd, service, deps, flags, "/api/v1/knowledge/engines")
	}})
	for _, kind := range []string{"creation", "retrieval"} {
		root.AddCommand(&cobra.Command{Use: kind + "-schema <author/name>", Short: i18n.T("Read ") + kind + " schema", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			// The operation registry validates the two plugin ID segments.
			return ragGet(cmd, service, deps, flags, "/api/v1/knowledge/engines/"+args[0]+"/"+kind+"-schema")
		}})
	}
	return root
}
func newKnowledgeParserCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	root := &cobra.Command{Use: "knowledge-parser", Short: i18n.T("Query document parsers"), Args: cobra.NoArgs}
	var mime string
	list := &cobra.Command{Use: "list", Short: i18n.T("List document parsers"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		path := "/api/v1/knowledge/parsers"
		if mime != "" {
			path += "?" + url.Values{"mime_type": {mime}}.Encode()
		}
		return ragGet(cmd, service, deps, flags, path)
	}}
	list.Flags().StringVar(&mime, "mime-type", "", i18n.T("Filter by MIME type"))
	root.AddCommand(list)
	return root
}
