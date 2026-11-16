package command

import (
	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
	"net/http"
	"net/url"
	"strconv"
)

func monitoringGet(cmd *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags, path string) error {
	return emitCall(deps, flags, func() (app.Result, error) {
		return service.APIRequest(cmd.Context(), http.MethodGet, path, nil, connectionOptions(flags))
	})
}
func newMonitoringCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	root := &cobra.Command{Use: "monitoring", Short: i18n.T("Query Workspace run history and errors"), Args: cobra.NoArgs}
	for _, kind := range []string{"messages", "llm-calls", "tool-calls", "embedding-calls", "sessions", "errors", "message", "session"} {
		var bots, pipelines, sessions []string
		var start, end, kb, user, active string
		var limit, offset int
		use, args := kind, cobra.NoArgs
		if kind == "message" || kind == "session" {
			use += " <id>"
			args = cobra.ExactArgs(1)
		}
		cmd := &cobra.Command{Use: use, Short: i18n.T("Read ") + kind + i18n.T(" run history"), Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			query := url.Values{}
			path := "/api/v1/monitoring/" + kind
			if kind == "message" || kind == "session" {
				if err := api.ValidateResourceID(args[0]); err != nil {
					return err
				}
				suffix := "details"
				if kind == "session" {
					suffix = "analysis"
				}
				path += "s/" + url.PathEscape(args[0]) + "/" + suffix
			} else {
				if limit < 1 || limit > 500 || offset < 0 {
					return result.New("input", i18n.T("limit must be 1..500; offset must not be negative"))
				}
				query.Set("limit", strconv.Itoa(limit))
				query.Set("offset", strconv.Itoa(offset))
			}
			for key, values := range map[string][]string{"botId": bots, "pipelineId": pipelines, "sessionId": sessions} {
				for _, v := range values {
					query.Add(key, v)
				}
			}
			for key, value := range map[string]string{"startTime": start, "endTime": end, "knowledgeBaseId": kb, "userQuery": user, "isActive": active} {
				if value != "" {
					query.Set(key, value)
				}
			}
			if len(query) > 0 {
				path += "?" + query.Encode()
			}
			return monitoringGet(cmd, service, deps, flags, path)
		}}
		if kind != "message" {
			cmd.Flags().StringVar(&start, "start-time", "", i18n.T("Start time (ISO 8601)"))
			cmd.Flags().StringVar(&end, "end-time", "", i18n.T("End time (ISO 8601)"))
		}
		if kind != "message" && kind != "session" {
			cmd.Flags().IntVar(&limit, "limit", 100, i18n.T("Number of records: 1..500 (server may impose a lower limit)"))
			cmd.Flags().IntVar(&offset, "offset", 0, i18n.T("Number of records to skip"))
			if kind != "embedding-calls" {
				cmd.Flags().StringArrayVar(&pipelines, "pipeline", nil, i18n.T("Filter by Pipeline; repeatable"))
			}
		}
		if kind != "message" && kind != "embedding-calls" {
			cmd.Flags().StringArrayVar(&bots, "bot", nil, i18n.T("Filter by Bot; session details allow only one"))
		}
		if kind == "messages" || kind == "tool-calls" {
			cmd.Flags().StringArrayVar(&sessions, "session", nil, i18n.T("Filter by session; repeatable"))
		}
		if kind == "embedding-calls" {
			cmd.Flags().StringVar(&kb, "knowledge-base", "", i18n.T("Filter by knowledge base"))
		}
		if kind == "sessions" {
			cmd.Flags().StringVar(&user, "user-query", "", i18n.T("Filter by user query"))
			cmd.Flags().StringVar(&active, "active", "", i18n.T("Filter active/inactive sessions: true or false"))
		}
		root.AddCommand(cmd)
	}
	return root
}
