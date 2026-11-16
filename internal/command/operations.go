package command

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

type operationCommand struct {
	path      string
	id        string
	arguments string
	stream    bool
}

var friendlyOperations = []operationCommand{
	{"agent list", "agent.list", "", false},
	{"agent get", "agent.get", "agent_uuid", false},
	{"agent create", "agent.create", "", false},
	{"agent update", "agent.update", "agent_uuid", false},
	{"agent delete", "agent.delete", "agent_uuid", false},
	{"agent metadata", "agent.metadata", "", false},
	{"agent runs list", "agent.runs.list", "processor_id", false},
	{"agent runs events", "agent.runs.events", "processor_id run_id", false},
	{"agent debug", "agent.debug.stream", "agent_uuid", true},
	{"plugin permissions get", "plugin.permissions.get", "author plugin_name", false},
	{"plugin search", "plugin.marketplace.search", "query", false},
	{"plugin readme", "plugin.readme", "author plugin_name", false},
	{"plugin debug-info", "plugin.debug_info", "", false},
	{"plugin config-file delete", "plugin.config_file.delete", "file_key", false},
	{"plugin github releases", "plugin.github.releases", "repo_url", false},
	{"plugin github assets", "plugin.github.assets", "owner repo", false},
	{"plugin page-action", "plugin.page_action", "author plugin_name page_id endpoint", false},
	{"bot routes preview", "bot.routes.preview", "bot_uuid", false},
	{"bot routes status", "bot.routes.status", "bot_uuid", false},
	{"bot message send", "bot.message.send", "bot_uuid", false},
	{"bot message test", "bot.message.test", "bot_uuid", false},
	{"bot logs", "bot.logs", "bot_uuid", false},
	{"bot runtime", "bot.runtime.get", "bot_uuid", false},
	{"bot admins list", "bot.admins.list", "bot_uuid", false},
	{"bot admins add", "bot.admins.add", "bot_uuid", false},
	{"bot admins delete", "bot.admins.delete", "bot_uuid", false},
	{"bot group-filter preview", "bot.group_filter.preview", "bot_uuid", false},
	{"bot group-filter apply", "bot.group_filter.apply", "bot_uuid", false},
	{"monitoring overview", "monitoring.overview", "", false},
	{"monitoring executions", "monitoring.executions", "", false},
	{"monitoring execution", "monitoring.execution.detail", "execution_id", false},
	{"monitoring tokens", "monitoring.tokens", "", false},
	{"monitoring inflight", "monitoring.inflight", "", false},
	{"monitoring feedback list", "monitoring.feedback.list", "", false},
	{"monitoring feedback stats", "monitoring.feedback.stats", "", false},
	{"monitoring session analysis", "monitoring.session.analysis", "session_id", false},
	{"monitoring session reset", "monitoring.session.reset", "bot_id session_id", false},
	{"pipeline metadata", "pipeline.metadata", "", false},
	{"pipeline defaults", "pipeline.defaults", "", false},
	{"pipeline chat history", "pipeline.chat.history", "pipeline_uuid session_type", false},
	{"pipeline chat reset", "pipeline.chat.reset", "pipeline_uuid session_type", false},
	{"pipeline chat connections", "pipeline.chat.connections", "pipeline_uuid", false},
	{"pipeline chat broadcast", "pipeline.chat.broadcast", "pipeline_uuid", false},
	{"pipeline migration preview", "pipeline.migration.preview", "", false},
	{"pipeline migration execute", "pipeline.migration.execute", "", false},
	{"model default get", "model.llm.default.get", "", false},
	{"model starred get", "model.llm.starred.get", "", false},
	{"model starred set", "model.llm.starred.set", "model_uuid", false},
	{"provider models count", "provider.models.count", "provider_uuid", false},
	{"provider codex start", "provider.codex.start", "provider", false},
	{"provider codex poll", "provider.codex.poll", "provider authorization_id", false},
	{"provider codex status", "provider.codex.status", "provider", false},
	{"provider codex cancel", "provider.codex.cancel", "provider authorization_id", false},
	{"provider codex disconnect", "provider.codex.disconnect", "provider", false},
	{"skill directory scan", "skill.directory.scan", "path", false},
	{"skill directory import", "skill.directory.import", "path", false},
	{"skill reload", "skill.reload", "", false},
	{"skill package get", "skill.package.get", "skill_name", false},
	{"skill github preview", "skill.github.preview", "", false},
	{"mcp-server runtime", "mcp.server.runtime", "server_name", false},
	{"adapter list", "adapter.list", "", false},
	{"adapter get", "adapter.get", "adapter_name", false},
	{"requester list", "requester.list", "", false},
	{"requester get", "requester.get", "requester_name", false},
	{"tool list", "tool.list", "", false},
	{"tool get", "tool.get", "tool_name", false},
	{"extension list", "extension.list", "", false},
	{"api-key list", "api_key.list", "", false},
	{"api-key get", "api_key.get", "key_id", false},
	{"api-key create", "api_key.create", "", false},
	{"api-key update", "api_key.update", "key_id", false},
	{"api-key delete", "api_key.delete", "key_id", false},
	{"webhook list", "webhook.list", "", false},
	{"webhook get", "webhook.get", "webhook_id", false},
	{"webhook create", "webhook.create", "", false},
	{"webhook update", "webhook.update", "webhook_id", false},
	{"webhook delete", "webhook.delete", "webhook_id", false},
	{"settings governance get", "settings.governance.get", "", false},
	{"settings governance update", "settings.governance.update", "", false},
	{"settings operation-logs list", "settings.operation_logs.list", "", false},
	{"settings operation-logs filters", "settings.operation_logs.filters", "", false},
	{"settings operation-logs export", "settings.operation_logs.export", "", false},
	{"workspace current", "workspace.current", "", false},
	{"workspace get", "workspace.get", "", false},
	{"workspace language get", "workspace.language.get", "", false},
	{"workspace language update", "workspace.language.update", "language", false},
	{"workspace member list", "workspace.member.list", "", false},
	{"workspace member update", "workspace.member.update", "account_uuid", false},
	{"workspace member remove", "workspace.member.remove", "account_uuid", false},
	{"workspace invitation create", "workspace.invitation.create", "", false},
	{"workspace invitation list", "workspace.invitation.list", "", false},
	{"workspace invitation revoke", "workspace.invitation.revoke", "invitation_uuid", false},
	{"assistant list", "assistant.list", "", false},
	{"assistant get", "assistant.get", "conversation_id", false},
	{"assistant create", "assistant.create", "", false},
	{"assistant turn", "assistant.turn", "conversation_id", false},
	{"assistant run", "assistant.run.start", "conversation_id", false},
	{"assistant stream", "assistant.stream", "conversation_id", true},
	{"assistant stop", "assistant.stop", "conversation_id", false},
	{"stats", "stats.basic", "", false},
	{"system storage-analysis", "system.storage.analysis", "", false},
	{"system model-availability", "system.model.availability", "", false},
	{"system model-recommended", "system.model.recommended", "", false},
	{"system plugin-status", "system.plugin.status", "", false},
}

func operationCommands() []operationCommand {
	commands := append([]operationCommand(nil), friendlyOperations...)
	for _, platform := range []string{"dingtalk", "lark", "qqofficial", "slack", "wecombot", "weixin"} {
		for _, action := range []string{"start", "status", "cancel"} {
			arguments := "session_id"
			if action == "start" {
				arguments = ""
			}
			commands = append(commands, operationCommand{"bot setup " + platform + " " + action, "platform.setup." + platform + "." + action, arguments, false})
		}
	}
	for _, kind := range []string{"messages", "sessions", "errors", "llm-calls", "embedding-calls", "feedback"} {
		commands = append(commands, operationCommand{"monitoring export " + kind, "monitoring.export." + strings.ReplaceAll(kind, "-", "_"), "", false})
	}
	return commands
}

func commandGroup(root *cobra.Command, path []string) *cobra.Command {
	for _, name := range path {
		var next *cobra.Command
		for _, child := range root.Commands() {
			if child.Name() == name {
				next = child
				break
			}
		}
		if next == nil {
			next = &cobra.Command{Use: name, Short: "管理 " + name, Args: cobra.NoArgs}
			root.AddCommand(next)
		}
		root = next
	}
	return root
}

func addOperationCommands(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	op := commandGroup(root, []string{"op"})
	var domain, search string
	list := &cobra.Command{Use: "list", Short: "列出当前身份可执行的操作", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			session, err := service.OpenOperations(cmd.Context(), connectionOptions(flags), false)
			if err != nil {
				return app.Result{}, err
			}
			value, err := session.Client.OperationList(cmd.Context(), session.Target, domain, search)
			return session.Result(value), err
		})
	}}
	list.Flags().StringVar(&domain, "domain", "", "按操作域筛选")
	list.Flags().StringVar(&search, "search", "", "搜索操作描述")
	op.AddCommand(list)
	op.AddCommand(&cobra.Command{Use: "describe <id>", Short: "显示操作的风险、权限和输入输出 schema", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			if err := api.ValidateOperationID(args[0]); err != nil {
				return app.Result{}, err
			}
			session, err := service.OpenOperations(cmd.Context(), connectionOptions(flags), false)
			if err != nil {
				return app.Result{}, err
			}
			value, err := session.Client.OperationDescribe(cmd.Context(), session.Target, args[0])
			return session.Result(value), err
		})
	}})
	for _, mode := range []string{"call", "stream"} {
		addOperationCommand(op, operationCommand{mode, "", "id", mode == "stream"}, service, deps, flags)
	}
	for _, spec := range operationCommands() {
		parts := strings.Fields(spec.path)
		parent := commandGroup(root, parts[:len(parts)-1])
		spec.path = parts[len(parts)-1]
		addOperationCommand(parent, spec, service, deps, flags)
	}
	addPermissionCommand(root, service, deps, flags)
	addOperationUploads(root, service, deps, flags)
}

func confirmOperation(deps Dependencies, yes bool, stdinUsed bool) error {
	if yes {
		return nil
	}
	file, ok := deps.In.(*os.File)
	if !ok || stdinUsed {
		return result.New("precondition", "写操作需要 --yes；非交互模式不会请求确认")
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return result.New("precondition", "写操作需要 --yes")
	}
	fmt.Fprint(deps.Err, "确认执行此写操作？[y/N] ")
	answer, err := bufio.NewReader(file).ReadString('\n')
	if err != nil || (strings.TrimSpace(strings.ToLower(answer)) != "y" && strings.TrimSpace(strings.ToLower(answer)) != "yes") {
		return result.New("precondition", "操作未确认")
	}
	return nil
}

func addOperationCommand(parent *cobra.Command, spec operationCommand, service *app.Service, deps Dependencies, flags *globalFlags) {
	var decodeField, destination string
	var file string
	var yes bool
	fields := strings.Fields(spec.arguments)
	use := spec.path
	for _, field := range fields {
		use += " <" + field + ">"
	}
	cmd := &cobra.Command{Use: use, Short: "调用 " + spec.id, Args: cobra.ExactArgs(len(fields))}
	if spec.id != "" {
		cmd.Long = "调用 " + spec.id + "。--file 接受操作输入对象，位置参数自动填入同名字段。\n输入字段：" + friendlyOperationDetails[spec.id].fields
	}
	cmd.Flags().StringVar(&file, "file", "", "操作输入 JSON/YAML 对象；- 从 stdin 读取；字段见 op describe")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认写操作")
	if spec.id == "" && !spec.stream {
		cmd.Flags().StringVar(&decodeField, "decode-binary", "", "将指定 base64 输出字段解码为文件")
		cmd.Flags().StringVarP(&destination, "out-file", "O", "", "解码后的文件路径，须与 --decode-binary 一起使用")
	}
	if spec.stream && spec.id == "" {
		_ = cmd.MarkFlagRequired("file")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			if (decodeField == "") != (destination == "") {
				return app.Result{}, result.New("input", "--decode-binary 与 -O 必须同时使用")
			}
			identifier := spec.id
			if identifier == "" {
				identifier = args[0]
			}
			if err := api.ValidateOperationID(identifier); err != nil {
				return app.Result{}, err
			}
			body := map[string]any{}
			if file != "" {
				var err error
				body, err = loadRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
				if err != nil {
					return app.Result{}, err
				}
			}
			if spec.id != "" {
				for index, field := range fields {
					if value, exists := body[field]; exists && value != args[index] {
						return app.Result{}, result.New("input", "请求体与位置参数不一致："+field)
					}
					body[field] = args[index]
				}
			}
			session, err := service.OpenOperations(cmd.Context(), connectionOptions(flags), spec.stream)
			if err != nil {
				return app.Result{}, err
			}
			var description map[string]any
			if spec.id != "" {
				description = friendlyOperationDetails[identifier].description()
			} else {
				description, err = session.Client.OperationDescribe(cmd.Context(), session.Target, identifier)
				if err != nil && result.AsError(err).HTTPStatus == 404 {
					if details, known := friendlyOperationDetails[identifier]; known {
						description, err = details.description(), nil
					}
				}
				if err != nil {
					return session.Result(nil), err
				}
			}
			streaming, _ := description["streaming"].(bool)
			if streaming != spec.stream {
				return session.Result(nil), result.New("input", "操作执行模式不匹配，请使用 op call 或 op stream")
			}
			if err := session.Authorize(description); err != nil {
				return session.Result(nil), err
			}
			if app.OperationWrites(description) {
				if err := confirmOperation(deps, yes, file == "-" || flags.apiKeyStdin); err != nil {
					return session.Result(nil), err
				}
			}
			if spec.stream {
				err = session.Client.OperationStream(cmd.Context(), session.Target, identifier, body, func(event any) error {
					if flags.output == "yaml" || flags.output == "yml" {
						fmt.Fprintln(deps.Out, "---")
					}
					return emitCommand(deps, flags, session.Result(event), nil)
				})
				if flags.output == "yaml" || flags.output == "yml" {
					fmt.Fprintln(deps.Out, "---")
				}
				return session.Result(map[string]any{"completed": err == nil}), err
			}
			value, err := session.Client.OperationCall(cmd.Context(), session.Target, identifier, body)
			if err == nil && decodeField != "" {
				object, _ := value.(map[string]any)
				encoded, ok := object[decodeField].(string)
				if !ok {
					return session.Result(nil), result.New("input", "输出中没有指定的 base64 字段")
				}
				decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
				if decodeErr != nil {
					return session.Result(nil), result.New("incompatible", "输出字段不是有效 base64")
				}
				if os.WriteFile(destination, decoded, 0o600) != nil {
					return session.Result(nil), result.New("input", "无法保存解码文件")
				}
				value = map[string]any{"file": destination, "bytes": len(decoded)}
			}
			return session.Result(value), err
		})
	}
	parent.AddCommand(cmd)
}
