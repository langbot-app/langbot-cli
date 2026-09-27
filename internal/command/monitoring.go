package command

import (
	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
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
	root := &cobra.Command{Use: "monitoring", Short: "查询 Workspace 运行记录及错误", Args: cobra.NoArgs}
	for _, kind := range []string{"messages", "llm-calls", "tool-calls", "embedding-calls", "sessions", "errors", "message", "session"} {
		var bots, pipelines, sessions []string
		var start, end, kb, user, active string
		var limit, offset int
		use, args := kind, cobra.NoArgs
		if kind == "message" || kind == "session" {
			use += " <id>"
			args = cobra.ExactArgs(1)
		}
		cmd := &cobra.Command{Use: use, Short: "读取 " + kind + " 运行记录", Args: args, RunE: func(cmd *cobra.Command, args []string) error {
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
					return result.New("input", "limit 必须为 1..500，offset 不能为负数")
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
			cmd.Flags().StringVar(&start, "start-time", "", "开始时间（ISO 8601）")
			cmd.Flags().StringVar(&end, "end-time", "", "结束时间（ISO 8601）")
		}
		if kind != "message" && kind != "session" {
			cmd.Flags().IntVar(&limit, "limit", 100, "返回条数：1..500（服务端可进一步限制）")
			cmd.Flags().IntVar(&offset, "offset", 0, "跳过记录数")
			if kind != "embedding-calls" {
				cmd.Flags().StringArrayVar(&pipelines, "pipeline", nil, "按 Pipeline 筛选，可重复")
			}
		}
		if kind != "message" && kind != "embedding-calls" {
			cmd.Flags().StringArrayVar(&bots, "bot", nil, "按 Bot 筛选；session 详情仅允许一个")
		}
		if kind == "messages" || kind == "tool-calls" {
			cmd.Flags().StringArrayVar(&sessions, "session", nil, "按会话筛选，可重复")
		}
		if kind == "embedding-calls" {
			cmd.Flags().StringVar(&kb, "knowledge-base", "", "按知识库筛选")
		}
		if kind == "sessions" {
			cmd.Flags().StringVar(&user, "user-query", "", "用户查询筛选")
			cmd.Flags().StringVar(&active, "active", "", "仅返回活跃/非活跃会话：true 或 false")
		}
		root.AddCommand(cmd)
	}
	return root
}
